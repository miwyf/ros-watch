"""Deterministic synthetic records using RFC5737 documentation addresses only."""

import random
import time

from .parser import parse_message


def seed(store):
    with store.connect(True) as db:
        if db.execute("SELECT 1 FROM events LIMIT 1").fetchone():
            return
    rng, now, events = random.Random(42), time.time(), []
    for i in range(2400):
        src = f"198.51.100.{rng.choice([8, 8, 8, 21, 21, 47, 89, 102, 155, 201])}"
        port = rng.choice([22, 80, 443, 8291, 8728, 3389, 8080, 23])
        proto = "TCP (SYN)" if rng.random() < 0.9 else "UDP"
        interface = rng.choice(["pppoe-out1", "pppoe-out1", "pppoe-out2"])
        scope = "forward" if port in (80, 443) else "input"
        prefix = "RW-FWD" if scope == "forward" else "RW-DROP"
        message = f"<134>Oct 31 12:00:00 demo {prefix} {scope}: in:{interface} out:(none), proto {proto}, {src}:{40000 + i}->192.0.2.1:{port}, len 60"
        events.append(
            parse_message(
                message, "127.0.0.1", store.config.wan_map, now - rng.random() * 86400
            )
        )
    for i in range(32):
        message = f"RW-DROP input: in:pppoe-out1 out:(none), proto TCP (SYN), 203.0.113.45:{50000 + i}->192.0.2.1:{1000 + i}, len 60"
        events.append(
            parse_message(message, "127.0.0.1", store.config.wan_map, now - 60)
        )
    events.sort(key=lambda e: e["ts"])
    store.append(events)
