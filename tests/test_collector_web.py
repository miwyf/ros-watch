from dataclasses import replace
import socket
import os
import time

import pytest

from conftest import event, login


def test_auth_and_csrf(client):
    assert client.get("/api/summary").status_code == 401
    assert client.get("/").status_code == 302
    assert (
        client.post("/login", data={"password": "test-password-12345"}).status_code
        == 400
    )
    assert login(client).status_code == 302
    assert client.get("/api/summary").status_code == 200
    assert client.post("/logout").status_code == 400
    with client.session_transaction() as s:
        csrf = s["csrf"]
    assert client.post("/logout", data={"csrf": csrf}).status_code == 302
    assert client.get("/api/summary").status_code == 401


def test_login_throttle(client):
    client.get("/login")
    with client.session_transaction() as s:
        csrf = s["csrf"]
    for _ in range(5):
        assert (
            client.post("/login", data={"csrf": csrf, "password": "wrong"}).status_code
            == 401
        )
    assert (
        client.post("/login", data={"csrf": csrf, "password": "wrong"}).status_code
        == 429
    )


def test_filters_export_and_escape(client, store):
    login(client)
    store.append([event()])
    assert client.get("/api/events?src=198.51.100.8").json["rows"][0]["dport"] == 8291
    for query in (
        "hours=-1",
        "dport=99999",
        "src=' OR 1=1",
        "scope=bad",
        "hours=999999",
    ):
        assert client.get("/api/events?" + query).status_code == 400
    response = client.get("/api/export")
    assert (
        response.status_code == 200
        and "attachment" in response.headers["Content-Disposition"]
    )
    assert "198.51.100.8" in response.text
    assert client.get("/").headers["X-Frame-Options"] == "DENY"


def test_real_udp_to_sqlite_and_api(collector, store, client):
    # Some Windows hosts filter even local UDP; establish that boundary with an
    # independent socket pair rather than hiding a collector regression.
    if os.name == "nt":
        with (
            socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe,
            socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sender,
        ):
            probe.bind(("127.0.0.1", 0))
            probe.settimeout(0.3)
            sender.sendto(b"probe", probe.getsockname())
            try:
                probe.recvfrom(16)
            except socket.timeout:
                pytest.skip(
                    "Windows host filters baseline UDP loopback; run this integration test in Linux"
                )
    collector.start()
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        sock.sendto(event()["raw"].encode(), ("127.0.0.1", collector.bound_port))
        until = time.monotonic() + 3
        while collector.counts["written"] < 1 and time.monotonic() < until:
            time.sleep(0.03)
        login(client)
        assert client.get("/api/summary").json["totals"]["observations"] == 1
        assert client.get("/api/events").json["rows"][0]["src"] == "198.51.100.8"
        assert collector.status()["counters"]["written"] == 1
    finally:
        sock.close()


def test_queue_and_sender_protection(config, store):
    from roswatch.collector import Collector

    config.settings = replace(config.settings, queue_size=1)
    c = Collector(config, store)
    raw = event()["raw"].encode()
    c.accept(raw, "203.0.113.1")
    c.accept(raw, "127.0.0.1")
    c.accept(raw, "127.0.0.1")
    c.accept(b"x" * 4097, "127.0.0.1")
    assert (
        c.counts["untrusted"] == 1
        and c.counts["queue_dropped"] == 1
        and c.counts["oversized"] == 1
    )


def test_disk_low_is_visible(collector, store, monkeypatch):
    monkeypatch.setattr(store, "disk_ok", lambda: False)
    collector.start()
    collector.accept(event()["raw"].encode(), "127.0.0.1")
    until = time.monotonic() + 3
    while not collector.counts["storage_dropped"] and time.monotonic() < until:
        time.sleep(0.03)
    assert collector.error == "disk_low" and collector.counts["written"] == 0


def test_invalid_configuration_fails(config):
    config.settings = replace(config.settings, max_db_mb=1)
    with pytest.raises(ValueError):
        config.validate()
    config.settings = replace(config.settings, max_db_mb=512)
    config.demo = True
    config.http_host = "0.0.0.0"
    with pytest.raises(ValueError):
        config.validate()
