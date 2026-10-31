import time

from conftest import event


def filters(**kw):
    return {"since": time.time() - 86400, "until": time.time() + 1, **kw}


def test_summary_and_port_scan(store):
    ts = int(time.time() // 300) * 300 + 1
    store.append(
        [event(port=i, ts=ts) for i in range(20)] + [event(src="198.51.100.21", ts=ts)]
    )
    r = store.summary({"since": ts - 1, "until": ts + 1})
    assert r["totals"]["observations"] == 21
    assert r["totals"]["sources"] == 2
    assert len(r["scans"]) == 1 and r["scans"][0]["ports"] == 20
    assert sum(r["n"] for r in r["trend"]) == 21


def test_scan_fixed_windows_do_not_merge(store):
    ts = int(time.time() // 300) * 300
    store.append(
        [event(port=i, ts=ts - 1) for i in range(10)]
        + [event(port=i, ts=ts + 1) for i in range(10, 20)]
    )
    assert not store.summary({"since": ts - 10, "until": ts + 10})["scans"]


def test_filters_paging_and_icmp(store):
    store.append([event(port=i) for i in range(70)])
    page = store.query(filters(), 50)
    older = store.query(filters(), 50, page[-1]["id"])
    assert len(page) == 50 and len(older) == 20
    assert not set(r["id"] for r in page) & set(r["id"] for r in older)
    assert store.query(filters(dport=6))[0]["dport"] == 6
    assert not store.query(filters(src="203.0.113.4"))


def test_retention_and_row_cap(store):
    store.config.max_rows = 10
    store.append(
        [event(ts=time.time() - 8 * 86400)] + [event(port=i) for i in range(20)]
    )
    store.maintenance(True)
    with store.connect(True) as db:
        assert db.execute("SELECT COUNT(*) FROM events").fetchone()[0] == 10
        assert db.execute("SELECT MIN(dport) FROM events").fetchone()[0] == 10
    assert store.pruned == 11


def test_budget_prunes_and_continues(store):
    store.config.max_db_mb = 8
    store.config.max_rows = 50000
    for _ in range(4):
        rows = [event(port=i) for i in range(1000)]
        for e in rows:
            e["raw"] = "x" * 1200
        store.append(rows)
        store.maintenance(True)
    # A later cleanup under a smaller row cap must reclaim without losing writer.
    store.config.max_rows = 100
    store.maintenance(True)
    store.append([event(port=65535)])
    assert store.query(filters(), 1)[0]["dport"] == 65535
