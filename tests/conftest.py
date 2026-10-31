import pytest

from roswatch.collector import Collector
from roswatch.config import Config
from roswatch.parser import parse_message
from roswatch.storage import Store
from roswatch.web import create_app


def event(
    src="198.51.100.8",
    port=8291,
    ts=None,
    proto="TCP (SYN)",
    wan="pppoe-out1",
    prefix="RW-DROP",
    chain="input",
):
    return parse_message(
        f"<134>Oct 31 12:00:00 router {prefix} {chain}: in:{wan} out:(none), proto {proto}, {src}:45000->192.0.2.1:{port}, len 60",
        "127.0.0.1",
        {"pppoe-out1": "WAN1", "pppoe-out2": "WAN2"},
        ts,
    )


@pytest.fixture
def config(tmp_path):
    return Config(
        data_dir=tmp_path,
        password="test-password-12345",
        allowed_senders="127.0.0.1/32",
        syslog_host="127.0.0.1",
        syslog_port=0,
        min_free_mb=0,
    )


@pytest.fixture
def store(config):
    return Store(config)


@pytest.fixture
def collector(config, store):
    c = Collector(config, store)
    yield c
    c.stop()


@pytest.fixture
def client(config, store, collector):
    app = create_app(config, store, collector)
    app.config["TESTING"] = True
    return app.test_client()


def login(client):
    client.get("/login")
    with client.session_transaction() as s:
        csrf = s["csrf"]
    return client.post("/login", data={"password": "test-password-12345", "csrf": csrf})
