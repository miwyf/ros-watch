import pytest

from roswatch.parser import parse_message
from conftest import event


def test_ipv4_bsd():
    e = event()
    assert (e["src"], e["dport"], e["wan"], e["scope"], e["outcome"]) == (
        "198.51.100.8",
        8291,
        "WAN1",
        "router",
        "blocked",
    )


def test_forward_is_attempt_not_success():
    e = event(port=1883, proto="UDP", prefix="RW-FWD", chain="forward")
    assert e["scope"] == "forward" and e["outcome"] == "attempt" and e["dport"] == 1883


def test_ipv6_and_icmp():
    raw = "input: in:pppoe-out1 out:(none), proto TCP (SYN), [2001:db8::1]:4000->[2001:db8::2]:443, len 80"
    assert (
        parse_message(raw, "127.0.0.1", {"pppoe-out1": "WAN1"})["src"] == "2001:db8::1"
    )
    raw = "input: in:pppoe-out1 out:(none), proto ICMP (type 8, code 0), 198.51.100.1->192.0.2.1, len 60"
    e = parse_message(raw, "127.0.0.1", {"pppoe-out1": "WAN1"})
    assert e["dport"] is None and e["proto"] == "ICMP"


def test_lan_ignored_and_custom_wan_accepted():
    assert (
        parse_message(
            "input: in:bridge out:(none), proto TCP, 198.51.100.1:9->192.0.2.1:80, len 60",
            "127.0.0.1",
            {},
        )
        is None
    )
    assert event(wan="ether1")["wan"] == "ether1"


@pytest.mark.parametrize(
    "raw",
    [
        "login failure",
        "input: in:ether1 out:(none), proto TCP, garbage->bad",
        "RW-DROP input: in:ether1 out:(none), proto TCP, 198.51.100.1:9->192.0.2.1:99999",
    ],
)
def test_bad_messages(raw):
    assert parse_message(raw, "127.0.0.1", {}) is None


def test_raw_sanitized_bounded():
    e = event()
    raw = e["raw"] + "\x00\n" + "x" * 5000
    e = parse_message(raw, "127.0.0.1", {"pppoe-out1": "WAN1"})
    assert len(e["raw"]) == 1200 and "\x00" not in e["raw"] and "\n" not in e["raw"]


def test_nat_suffix_does_not_replace_first_flow():
    raw = event()["raw"] + " NAT 192.0.2.1:11883->192.0.2.2:1883"
    assert parse_message(raw, "127.0.0.1", {"pppoe-out1": "WAN1"})["dport"] == 8291
