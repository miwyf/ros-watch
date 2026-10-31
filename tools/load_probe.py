"""Finite synthetic UDP -> SQLite -> HTTP probe. Run only against a test instance.

APP_PASSWORD is read from the environment, never printed. Resource readings, when
available, refer to the local roswatch process, not the probe process.
"""

import argparse
import http.cookiejar
import json
import os
import re
import socket
import time
import urllib.parse
import urllib.request
from pathlib import Path

from send_sample import sample


def resource_reader():
    if not Path("/proc").exists():
        return lambda: None
    for directory in Path("/proc").iterdir():
        if not directory.name.isdigit():
            continue
        try:
            command = (directory / "cmdline").read_bytes()
            executable = command.split(b"\x00", 1)[0].rsplit(b"/", 1)[-1]
            if (
                not executable.startswith(b"python")
                or b"-m\x00roswatch\x00" not in command
            ):
                continue

            def read(directory=directory):
                text = (directory / "stat").read_text().split(")", 1)[1].split()
                ticks = int(text[11]) + int(text[12])
                status = (directory / "status").read_text()
                rss = int(re.search(r"VmRSS:\s+(\d+)", status)[1]) * 1024
                return {
                    "cpu_seconds": ticks / os.sysconf("SC_CLK_TCK"),
                    "rss_bytes": rss,
                }

            return read
        except (OSError, ValueError, TypeError):
            continue
    return lambda: None


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--username", default="admin")
    p.add_argument("--url", default="http://127.0.0.1:8080")
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=5514)
    p.add_argument("--seconds", type=int, default=15)
    p.add_argument("--rate", type=int, default=100)
    a = p.parse_args()
    if not 1 <= a.seconds <= 60 or not 1 <= a.rate <= 1000:
        p.error("seconds 1–60, rate 1–1000")
    password = os.environ.get("APP_PASSWORD", "")
    if not password:
        p.error("Set APP_PASSWORD to the test dashboard password")
    op = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
    )

    def get():
        with op.open(a.url + "/api/summary?hours=1", timeout=8) as r:
            return json.load(r)

    page = op.open(a.url + "/login", timeout=5).read().decode()
    csrf = re.search(r'name="csrf" value="([^"]+)"', page)[1]
    op.open(
        a.url + "/login",
        data=urllib.parse.urlencode(
            {"csrf": csrf, "username": a.username, "password": password}
        ).encode(),
        timeout=5,
    ).close()
    initial = get()["status"]
    read = resource_reader()
    first = read()
    peak = first["rss_bytes"] if first else None
    started = time.monotonic()
    latencies = []
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        for i in range(a.seconds * a.rate):
            sock.sendto(sample(i), (a.host, a.port))
            if i % (a.rate * 2) == 0:
                t = time.monotonic()
                get()
                latencies.append(time.monotonic() - t)
                r = read()
                if r:
                    peak = max(peak or 0, r["rss_bytes"])
            delay = started + (i + 1) / a.rate - time.monotonic()
            if delay > 0:
                time.sleep(delay)
    sent = a.seconds * a.rate
    until = time.monotonic() + 4
    while time.monotonic() < until:
        final = get()["status"]
        if (
            final["counters"].get("written", 0) - initial["counters"].get("written", 0)
            >= sent
        ):
            break
        time.sleep(0.2)
    elapsed = time.monotonic() - started
    last = read()
    counters = {
        k: final["counters"].get(k, 0) - initial["counters"].get(k, 0)
        for k in set(final["counters"]) | set(initial["counters"])
    }
    report = {
        "sent": sent,
        "duration_seconds": round(elapsed, 2),
        "counters_delta": counters,
        "summary_max_ms": round(max(latencies, default=0) * 1000, 1),
        "peak_rss_mb": round(peak / 1048576, 1) if peak else None,
        "cpu_percent_one_core": round(
            (last["cpu_seconds"] - first["cpu_seconds"]) / elapsed * 100, 1
        )
        if first and last
        else None,
        "db_bytes": final["db_bytes"],
    }
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if counters.get("written", 0) != sent:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
