import csv
import hmac
import io
import ipaddress
import secrets
import sqlite3
import threading
import time
from collections import OrderedDict
from datetime import timedelta
from dataclasses import asdict
from functools import wraps

from flask import (
    Flask,
    Response,
    g,
    jsonify,
    redirect,
    render_template,
    request,
    session,
)

from . import __version__
from .config import Settings
from .control import Control


def create_app(config, store, collector, control=None):
    config.validate()
    control = control or Control(config)
    control.bootstrap()
    collector.apply_settings(control.load_settings())
    app = Flask(__name__)
    app.extensions["control"] = control
    secret_file = config.data_dir / "session.key"
    if not secret_file.exists():
        with secret_file.open("xb") as f:
            f.write(secrets.token_bytes(32))
        secret_file.chmod(0o600)
    app.config.update(
        SECRET_KEY=secret_file.read_bytes(),
        MAX_CONTENT_LENGTH=16384,
        MAX_FORM_MEMORY_SIZE=16384,
        MAX_FORM_PARTS=20,
        SESSION_COOKIE_HTTPONLY=True,
        SESSION_COOKIE_SAMESITE="Strict",
        SESSION_COOKIE_SECURE=config.cookie_secure,
        PERMANENT_SESSION_LIFETIME=timedelta(hours=12),
    )
    failures = OrderedDict()
    rate_lock, query_lock = threading.Lock(), threading.Lock()
    settings_lock = threading.Lock()
    cache = OrderedDict()

    @app.after_request
    def headers(response):
        response.headers.update(
            {
                "X-Content-Type-Options": "nosniff",
                "X-Frame-Options": "DENY",
                "Referrer-Policy": "no-referrer",
                "Cache-Control": "no-store",
                "Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'",
            }
        )
        return response

    def auth_required(fn):
        @wraps(fn)
        def wrapped(*args, **kwargs):
            account = control.account()
            if not hmac.compare_digest(
                str(session.get("authenticated", "")), account["revision"]
            ):
                if request.path.startswith("/api/"):
                    return jsonify(error="请先登录"), 401
                return redirect("/login")
            g.account = account
            return fn(*args, **kwargs)

        return wrapped

    def csrf_ok():
        return hmac.compare_digest(
            session.get("csrf", "_missing"),
            request.headers.get("X-CSRF-Token", "") or request.form.get("csrf", ""),
        )

    def reserve_auth_attempt():
        now, key = time.monotonic(), request.remote_addr
        with rate_lock:
            attempts = [t for t in failures.get(key, []) if now - t < 300]
            total = [t for t in failures.get("global", []) if now - t < 60]
            if len(attempts) >= 5 or len(total) >= 20:
                return False
            failures[key], failures["global"] = attempts + [now], total + [now]
            while len(failures) > 1024:
                failures.popitem(last=False)
        return True

    @app.route("/login", methods=["GET", "POST"])
    def login():
        session.setdefault("csrf", secrets.token_urlsafe(24))
        error, code = "", 200
        if request.method == "POST":
            if not csrf_ok():
                return "请从登录页面提交", 400
            if not reserve_auth_attempt():
                error, code = "尝试过于频繁，请稍后重试", 429
            if not error:
                account = control.authenticate(
                    request.form.get("username", "").strip(),
                    request.form.get("password", ""),
                )
                if account:
                    session.clear()
                    session.update(
                        authenticated=account["revision"],
                        csrf=secrets.token_urlsafe(24),
                    )
                    session.permanent = True
                    with rate_lock:
                        failures.pop(request.remote_addr, None)
                    return redirect("/")
                error, code = "用户名或密码不正确", 401
        return render_template(
            "login.html", csrf=session["csrf"], error=error, version=__version__
        ), code

    @app.post("/logout")
    @auth_required
    def logout():
        if not csrf_ok():
            return "Invalid CSRF token", 400
        session.clear()
        return redirect("/login")

    @app.get("/")
    @auth_required
    def index():
        return render_template(
            "index.html",
            csrf=session["csrf"],
            username=g.account["username"],
            version=__version__,
        )

    @app.route("/api/account", methods=["GET", "POST"])
    @auth_required
    def account_settings():
        if request.method == "GET":
            return jsonify(username=g.account["username"])
        if not csrf_ok():
            return jsonify(error="页面验证已过期，请刷新后重试"), 400
        if not reserve_auth_attempt():
            return jsonify(error="尝试过于频繁，请稍后重试"), 429
        body = request.get_json()
        if not isinstance(body, dict) or not all(
            isinstance(body.get(k, ""), str)
            for k in (
                "username",
                "current_password",
                "new_password",
                "confirm_password",
            )
        ):
            return jsonify(error="账号设置格式无效"), 400
        password = body.get("new_password", "")
        if password != body.get("confirm_password", ""):
            return jsonify(error="两次新密码不一致"), 400
        try:
            changed = control.change_account(
                g.account["revision"],
                body.get("current_password", ""),
                body.get("username", "").strip(),
                password or None,
            )
        except ValueError as exc:
            return jsonify(error=str(exc)), 400
        if not changed:
            return jsonify(error="当前密码不正确，请重新输入"), 403
        with rate_lock:
            failures.pop(request.remote_addr, None)
        session.clear()
        return jsonify(
            message="账号已更新，请使用新账号信息重新登录", redirect="/login?changed=1"
        )

    @app.route("/api/settings", methods=["GET", "POST"])
    @auth_required
    def runtime_settings():
        if request.method == "GET":
            return jsonify(settings=asdict(config.settings))
        if not csrf_ok():
            return jsonify(error="页面验证已过期，请刷新后重试"), 400
        try:
            body = request.get_json()
            if not isinstance(body, dict) or set(body) != set(
                Settings.__dataclass_fields__
            ):
                raise ValueError("请提交完整的运行设置")
            settings = Settings.from_dict(body)
        except (ValueError, TypeError) as exc:
            return jsonify(error=str(exc)), 400
        with settings_lock:
            control.save_settings(settings)
            collector.apply_settings(settings)
            with query_lock:
                cache.clear()
        return jsonify(
            settings=asdict(settings),
            message="设置已保存并生效；保留策略由后台清理任务执行",
        )

    @app.get("/healthz")
    def health():
        healthy = not collector.error
        if collector.threads:
            healthy = healthy and all(t.is_alive() for t in collector.threads)
        return jsonify(
            status="ok" if healthy else "degraded", version=__version__
        ), 200 if healthy else 503

    def filters():
        now = time.time()
        hours = float(request.args.get("hours", 24))
        if not 0 < hours <= min(config.settings.retention_days * 24, 31 * 24):
            raise ValueError("时间范围超出保留期限")
        f = {"since": now - hours * 3600, "until": now}
        choices = {
            "scope": {"router", "forward"},
            "outcome": {"blocked", "attempt"},
            "proto": {"TCP", "UDP", "ICMP", "ICMPV6", "IPV6-ICMP", "GRE", "ESP"},
        }
        for field in ("src", "dport", "wan", *choices):
            value = request.args.get(field, "").strip()
            if not value:
                continue
            if field == "src":
                value = str(ipaddress.ip_address(value))
            elif field == "dport":
                value = int(value)
                if not 0 <= value <= 65535:
                    raise ValueError("端口应为 0–65535")
            elif field in choices and value not in choices[field]:
                raise ValueError("筛选值无效")
            elif field == "wan" and len(value) > 64:
                raise ValueError("WAN 名称过长")
            f[field] = value
        return f

    @app.errorhandler(ValueError)
    def bad_filter(exc):
        return jsonify(error="筛选条件无效，请检查 IP、端口和时间范围"), 400

    @app.errorhandler(sqlite3.Error)
    def query_error(exc):
        return jsonify(error="查询暂不可用，请缩小时间范围后重试"), 503

    @app.get("/api/summary")
    @auth_required
    def summary():
        f = filters()
        # Cache by exact filters + rounded time, so an idle dashboard does no
        # repeated expensive GROUP BY work. Bounded cache and query concurrency.
        key = tuple(
            sorted(
                (k, int(v // 10) if k in {"since", "until"} else v)
                for k, v in f.items()
            )
        )
        with query_lock:
            cached = cache.get(key)
            if cached and time.monotonic() - cached[0] < 10:
                result = dict(cached[1])
            else:
                result = store.summary(f)
                cache[key] = (time.monotonic(), result)
                while len(cache) > 32:
                    cache.popitem(last=False)
        return jsonify(**result, status=collector.status(), generated_at=time.time())

    @app.get("/api/events")
    @auth_required
    def events():
        f = filters()
        before = int(request.args["before"]) if request.args.get("before") else None
        rows = store.query(f, limit=51, before=before)
        return jsonify(rows=rows[:50], next=rows[49]["id"] if len(rows) > 50 else None)

    @app.get("/api/export")
    @auth_required
    def export():
        rows = store.query(filters(), limit=10000)
        out = io.StringIO(newline="")
        writer = csv.writer(out)
        columns = [
            "ts",
            "src",
            "sport",
            "dst",
            "dport",
            "proto",
            "wan",
            "scope",
            "outcome",
        ]
        writer.writerow(columns)
        for row in rows:
            # Spreadsheet formula injection protection for interface/host values.
            writer.writerow(
                [
                    ("'" + str(row[k]))
                    if str(row[k]).startswith(("=", "+", "-", "@"))
                    else row[k]
                    for k in columns
                ]
            )
        return Response(
            "\ufeff" + out.getvalue(),
            mimetype="text/csv",
            headers={"Content-Disposition": "attachment; filename=ros-watch.csv"},
        )

    return app
