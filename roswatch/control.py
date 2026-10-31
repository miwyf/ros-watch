"""Persist account and runtime settings independently of disposable log data."""

import hmac
import json
import os
import re
import secrets
import sqlite3
import threading
import time
from contextlib import contextmanager
from dataclasses import asdict

from argon2 import PasswordHasher
from argon2.exceptions import VerificationError, InvalidHashError

from .config import Settings


# OWASP Argon2id minimum. Serialize verification so simultaneous WSGI requests
# cannot multiply hashing memory beyond the small container's budget.
HASHER = PasswordHasher(
    time_cost=2, memory_cost=19456, parallelism=1, hash_len=32, salt_len=16
)


def validate_username(username):
    if not isinstance(username, str) or not re.fullmatch(
        r"[A-Za-z0-9][A-Za-z0-9_.-]{2,31}", username
    ):
        raise ValueError("用户名须为 3–32 位字母、数字、点、下划线或短横线")


class Control:
    def __init__(self, config):
        self.config = config
        config.data_dir.mkdir(parents=True, exist_ok=True)
        self.path = config.data_dir / "control.sqlite3"
        self.initial_file = config.data_dir / "initial-admin.txt"
        self.auth_lock = threading.Lock()
        with self.connect() as db:
            db.executescript("""
              CREATE TABLE IF NOT EXISTS account (
                id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL,
                password_hash TEXT NOT NULL, revision TEXT NOT NULL, updated_at REAL NOT NULL
              );
              CREATE TABLE IF NOT EXISTS settings (
                id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL
              );
            """)
        self.path.chmod(0o600)

    @contextmanager
    def connect(self):
        db = sqlite3.connect(self.path, timeout=5)
        db.row_factory = sqlite3.Row
        try:
            yield db
            db.commit()
        except Exception:
            db.rollback()
            raise
        finally:
            db.close()

    def bootstrap(self, username="admin", password=None, settings=None):
        with self.auth_lock:
            with self.connect() as db:
                if db.execute("SELECT 1 FROM account WHERE id=1").fetchone():
                    return False
                validate_username(username)
                generated = password is None
                password = (
                    password if password is not None else secrets.token_urlsafe(24)
                )
                if not isinstance(password, str) or not 12 <= len(password) <= 128:
                    raise ValueError("初始密码须为 12–128 位")
                settings = Settings.from_dict(asdict(settings or self.config.settings))
                db.execute(
                    "INSERT INTO account VALUES (1,?,?,?,?)",
                    (
                        username,
                        HASHER.hash(password),
                        secrets.token_hex(32),
                        time.time(),
                    ),
                )
                db.execute(
                    "INSERT INTO settings VALUES (1,?)", (json.dumps(asdict(settings)),)
                )
                if generated:
                    fd = os.open(
                        self.initial_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600
                    )
                    with os.fdopen(fd, "w", encoding="utf-8") as f:
                        f.write(
                            f"Username: {username}\nPassword: {password}\nChange these in Account Settings after signing in.\n"
                        )
                return True

    def account(self):
        with self.connect() as db:
            row = db.execute("SELECT * FROM account WHERE id=1").fetchone()
            if row is None:
                raise RuntimeError("Account has not been initialized")
            return dict(row)

    def _verify(self, account, username, password):
        if not isinstance(password, str) or len(password) > 128:
            return False
        try:
            valid = HASHER.verify(account["password_hash"], password)
        except (VerificationError, InvalidHashError):
            valid = False
        return valid and hmac.compare_digest(
            account["username"].encode(), username.encode()
        )

    def authenticate(self, username, password):
        with self.auth_lock:
            account = self.account()
            return account if self._verify(account, username, password) else None

    def change_account(self, revision, current_password, username, password=None):
        validate_username(username)
        if password is not None and not 15 <= len(password) <= 128:
            raise ValueError("新密码须为 15–128 位，可以使用长密码或口令")
        with self.auth_lock:
            account = self.account()
            if account["revision"] != revision or not self._verify(
                account, account["username"], current_password
            ):
                return False
            if password is None and username == account["username"]:
                raise ValueError("请填写新的用户名或密码")
            password_hash = (
                HASHER.hash(password)
                if password is not None
                else account["password_hash"]
            )
            with self.connect() as db:
                changed = db.execute(
                    "UPDATE account SET username=?,password_hash=?,revision=?,updated_at=? WHERE id=1 AND revision=?",
                    (
                        username,
                        password_hash,
                        secrets.token_hex(32),
                        time.time(),
                        revision,
                    ),
                ).rowcount
            if changed:
                self.initial_file.unlink(missing_ok=True)
            return bool(changed)

    def reset_password(self, password, username=None):
        if not 15 <= len(password) <= 128:
            raise ValueError("新密码须为 15–128 位")
        with self.auth_lock:
            current = self.account()
            username = username or current["username"]
            validate_username(username)
            with self.connect() as db:
                db.execute(
                    "UPDATE account SET username=?,password_hash=?,revision=?,updated_at=? WHERE id=1",
                    (
                        username,
                        HASHER.hash(password),
                        secrets.token_hex(32),
                        time.time(),
                    ),
                )
            self.initial_file.unlink(missing_ok=True)

    def load_settings(self):
        with self.connect() as db:
            row = db.execute("SELECT value FROM settings WHERE id=1").fetchone()
        return Settings.from_dict(json.loads(row["value"]))

    def save_settings(self, settings):
        with self.connect() as db:
            db.execute(
                "UPDATE settings SET value=? WHERE id=1",
                (json.dumps(asdict(settings)),),
            )
