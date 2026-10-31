import json
from dataclasses import replace

import pytest

from conftest import event, login
from roswatch.config import Config
from roswatch.control import Control
from roswatch.web import create_app
from roswatch.manage import import_v01


def csrf(client):
    with client.session_transaction() as session:
        return {"X-CSRF-Token": session["csrf"]}


def test_account_hash_and_restart_ignore_environment(config, monkeypatch):
    control = Control(config)
    control.bootstrap("owner", "a-good-initial-password")
    assert control.account()["password_hash"].startswith("$argon2id$")
    assert b"a-good-initial-password" not in control.path.read_bytes()
    monkeypatch.setenv("APP_PASSWORD", "different-env-password")
    monkeypatch.setenv("ALLOWED_SENDERS", "0.0.0.0/0")
    restarted = Control(Config(data_dir=config.data_dir))
    assert restarted.bootstrap("attacker", "different-env-password") is False
    assert restarted.authenticate("owner", "a-good-initial-password")
    assert not restarted.authenticate("owner", "different-env-password")
    assert restarted.load_settings().allowed_senders == "127.0.0.1/32"


def test_first_run_random_password_removed_after_change(config):
    control = Control(config)
    assert control.bootstrap()
    values = dict(
        line.split(": ", 1)
        for line in control.initial_file.read_text().splitlines()[:2]
    )
    assert len(values["Password"]) >= 24
    account = control.authenticate(values["Username"], values["Password"])
    assert account
    assert control.change_account(
        account["revision"], values["Password"], "my-admin", "an-even-better-password"
    )
    assert not control.initial_file.exists()
    assert control.authenticate("my-admin", "an-even-better-password")


def test_change_account_requires_csrf_and_current_password(client):
    login(client)
    body = {
        "username": "new-owner",
        "current_password": "wrong",
        "new_password": "updated-strong-password",
        "confirm_password": "updated-strong-password",
    }
    assert client.post("/api/account", json=body).status_code == 400
    assert (
        client.post("/api/account", json=body, headers=csrf(client)).status_code == 403
    )
    body.update(current_password="test-password-12345", confirm_password="mismatch")
    assert (
        client.post("/api/account", json=body, headers=csrf(client)).status_code == 400
    )
    assert client.get("/api/account").json == {"username": "admin"}
    assert "password_hash" not in json.dumps(client.get("/api/account").json)


def test_account_change_revokes_all_sessions_and_keeps_logs(
    client, store, collector, config
):
    login(client)
    other = client.application.test_client()
    login(other)
    store.append([event()])
    response = client.post(
        "/api/account",
        json={
            "username": "new-owner",
            "current_password": "test-password-12345",
            "new_password": "updated-strong-password",
            "confirm_password": "updated-strong-password",
        },
        headers=csrf(client),
    )
    assert response.status_code == 200
    assert other.get("/api/summary").status_code == 401
    assert client.get("/api/summary").status_code == 401
    control = client.application.extensions["control"]
    assert not control.authenticate("admin", "test-password-12345")
    assert control.authenticate("new-owner", "updated-strong-password")
    restarted = create_app(config, store, collector, Control(config)).test_client()
    restarted.get("/login")
    with restarted.session_transaction() as s:
        token = s["csrf"]
    assert (
        restarted.post(
            "/login",
            data={
                "csrf": token,
                "username": "new-owner",
                "password": "updated-strong-password",
            },
        ).status_code
        == 302
    )
    assert restarted.get("/api/summary").json["totals"]["observations"] == 1


def test_username_only_change_and_local_recovery(client):
    login(client)
    control = client.application.extensions["control"]
    rev = control.account()["revision"]
    assert control.change_account(rev, "test-password-12345", "owner")
    assert client.get("/api/summary").status_code == 401
    assert control.authenticate("owner", "test-password-12345")
    control.reset_password("local-recovery-password", "recovered")
    assert control.authenticate("recovered", "local-recovery-password")
    assert not control.authenticate("owner", "test-password-12345")


def test_wrong_username_unicode_is_generic_failure(client):
    client.get("/login")
    with client.session_transaction() as s:
        token = s["csrf"]
    response = client.post(
        "/login",
        data={
            "csrf": token,
            "username": "不存在的用户",
            "password": "test-password-12345",
        },
    )
    assert response.status_code == 401
    assert "用户名或密码不正确" in response.text


def test_settings_hot_apply_and_persist(client, collector, config):
    login(client)
    original = client.get("/api/settings").json["settings"]
    updated = {
        **original,
        "allowed_senders": "203.0.113.1",
        "wan_map": {"pppoe-out1": "主线"},
        "queue_size": 2,
        "retention_days": 3,
        "ingest_per_second": 20,
    }
    assert client.post("/api/settings", json=updated).status_code == 400
    response = client.post("/api/settings", json=updated, headers=csrf(client))
    assert response.status_code == 200
    raw = event()["raw"].encode()
    collector.accept(raw, "127.0.0.1")
    collector.accept(raw, "203.0.113.1")
    assert collector.counts["untrusted"] == 1
    assert collector.queue.get_nowait()["wan"] == "主线"
    assert collector.queue.maxsize == 2
    assert config.settings.retention_days == 3
    assert Control(config).load_settings() == config.settings
    # Clearing the whitelist pauses reception instead of allowing everybody.
    updated["allowed_senders"] = ""
    assert (
        client.post("/api/settings", json=updated, headers=csrf(client)).status_code
        == 200
    )
    collector.accept(raw, "203.0.113.1")
    assert collector.counts["untrusted"] == 2


def test_settings_refreshes_cached_scan_result(client, store):
    login(client)
    store.append([event(port=i) for i in range(3)])
    assert not client.get("/api/summary").json["scans"]
    settings = client.get("/api/settings").json["settings"]
    settings["scan_ports"] = 2
    assert (
        client.post("/api/settings", json=settings, headers=csrf(client)).status_code
        == 200
    )
    assert len(client.get("/api/summary").json["scans"]) == 1


@pytest.mark.parametrize(
    "bad",
    [
        {"allowed_senders": "0.0.0.0/0"},
        {"allowed_senders": "not-an-address"},
        {"max_db_mb": 1},
        {"retention_days": 0},
        {"queue_size": 100000},
        {"scan_window": True},
        {"wan_map": {"x": 1}},
        {"unknown": True},
    ],
)
def test_invalid_settings_leave_current_configuration(client, bad):
    login(client)
    original = client.get("/api/settings").json["settings"]
    assert (
        client.post(
            "/api/settings", json={**original, **bad}, headers=csrf(client)
        ).status_code
        == 400
    )
    assert client.get("/api/settings").json["settings"] == original


def test_log_cleanup_does_not_remove_account(config, store):
    control = Control(config)
    control.bootstrap("owner", "a-good-initial-password")
    config.settings = replace(config.settings, max_rows=1)
    store.append([event(port=i) for i in range(10)])
    store.maintenance(True)
    assert control.authenticate("owner", "a-good-initial-password")
    assert control.load_settings().allowed_senders


def test_v01_migration_preserves_credentials_settings_and_is_idempotent(config):
    control = Control(config)
    values = {
        "APP_PASSWORD": "legacy-strong-password",
        "ALLOWED_SENDERS": "192.0.2.1",
        "WAN_MAP": '{"pppoe-out1":"主线"}',
        "RETENTION_DAYS": "3",
        "MIN_FREE_MB": "1024",
        "SAMPLING_MODE": "limited",
    }
    assert import_v01(control, values)
    assert control.authenticate("admin", "legacy-strong-password")
    assert control.load_settings().allowed_senders == "192.0.2.1/32"
    assert control.load_settings().wan_map == {"pppoe-out1": "主线"}
    assert control.load_settings().min_free_mb == 1024
    assert (
        import_v01(control, {**values, "APP_PASSWORD": "another-strong-password"})
        is False
    )
    assert control.authenticate("admin", "legacy-strong-password")
