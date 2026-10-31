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
from functools import wraps

from flask import Flask, Response, jsonify, redirect, render_template, request, session
from werkzeug.security import check_password_hash, generate_password_hash

from . import __version__


def create_app(config, store, collector):
    config.validate()
    app = Flask(__name__)
    secret_file = config.data_dir / "session.key"
    if not secret_file.exists():
        with secret_file.open("xb") as f:
            f.write(secrets.token_bytes(32))
        secret_file.chmod(0o600)
    app.config.update(
        SECRET_KEY=secret_file.read_bytes(),
        MAX_CONTENT_LENGTH=4096,
        SESSION_COOKIE_HTTPONLY=True,
        SESSION_COOKIE_SAMESITE="Strict",
        SESSION_COOKIE_SECURE=config.cookie_secure,
        PERMANENT_SESSION_LIFETIME=timedelta(hours=12),
    )
    password_hash = generate_password_hash(config.password)
    auth_marker = hmac.new(
        app.secret_key, config.password.encode(), "sha256"
    ).hexdigest()
    failures = OrderedDict()
    rate_lock, query_lock = threading.Lock(), threading.Lock()
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
            if session.get("authenticated") != auth_marker:
                if request.path.startswith("/api/"):
                    return jsonify(error="请先登录"), 401
                return redirect("/login")
            return fn(*args, **kwargs)

        return wrapped

    def csrf_ok():
        return hmac.compare_digest(
            session.get("csrf", "_missing"), request.form.get("csrf", "")
        )

    @app.route("/login", methods=["GET", "POST"])
    def login():
        session.setdefault("csrf", secrets.token_urlsafe(24))
        error, code = "", 200
        if request.method == "POST":
            if not csrf_ok():
                return "请从登录页面提交", 400
            now, key = time.monotonic(), request.remote_addr
            with rate_lock:
                attempts = [t for t in failures.get(key, []) if now - t < 300]
                global_attempts = [
                    t for t in failures.get("global", []) if now - t < 60
                ]
                if len(attempts) >= 5 or len(global_attempts) >= 20:
                    error, code = "尝试过于频繁，请稍后重试", 429
                else:
                    failures[key] = attempts + [now]
                    failures["global"] = global_attempts + [now]
                    while len(failures) > 1024:
                        failures.popitem(last=False)
            if not error:
                if check_password_hash(password_hash, request.form.get("password", "")):
                    session.clear()
                    session.update(
                        authenticated=auth_marker, csrf=secrets.token_urlsafe(24)
                    )
                    session.permanent = True
                    with rate_lock:
                        failures.pop(key, None)
                    return redirect("/")
                error, code = "密码不正确", 401
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
        return render_template("index.html", csrf=session["csrf"], version=__version__)

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
        if not 0 < hours <= min(config.retention_days * 24, 31 * 24):
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
