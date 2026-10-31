import math
import shutil
import sqlite3
import threading
import time
from contextlib import contextmanager


COLUMNS = "ts,sender,src,sport,dst,dport,proto,flags,wan,interface,scope,outcome,raw"


class Store:
    def __init__(self, config):
        self.config = config
        config.data_dir.mkdir(parents=True, exist_ok=True)
        self.path = (config.data_dir / "events.sqlite3").resolve()
        self.lock = threading.Lock()
        self.last_maintenance = 0
        self.pruned = 0
        with self.connect() as db:
            db.execute("PRAGMA journal_mode=WAL")
            db.execute("PRAGMA auto_vacuum=INCREMENTAL")
            db.executescript("""
              CREATE TABLE IF NOT EXISTS events (
                id INTEGER PRIMARY KEY, ts REAL NOT NULL, sender TEXT NOT NULL,
                src TEXT NOT NULL, sport INTEGER, dst TEXT NOT NULL, dport INTEGER,
                proto TEXT NOT NULL, flags TEXT NOT NULL, wan TEXT NOT NULL,
                interface TEXT NOT NULL, scope TEXT NOT NULL, outcome TEXT NOT NULL, raw TEXT NOT NULL
              );
              CREATE INDEX IF NOT EXISTS events_time ON events(ts);
              CREATE INDEX IF NOT EXISTS events_source_time ON events(src,ts);
            """)

    @contextmanager
    def connect(self, readonly=False):
        if readonly:
            db = sqlite3.connect(self.path.as_uri() + "?mode=ro", uri=True, timeout=3)
        else:
            db = sqlite3.connect(self.path, timeout=3)
        db.row_factory = sqlite3.Row
        try:
            db.execute("PRAGMA busy_timeout=3000")
            if not readonly:
                db.execute("PRAGMA synchronous=NORMAL")
                db.execute("PRAGMA wal_autocheckpoint=256")
                db.execute(
                    f"PRAGMA max_page_count={self.config.max_db_mb * 1024 * 1024 // db.execute('PRAGMA page_size').fetchone()[0]}"
                )
            # A malformed query/filter cannot hold the reader indefinitely.
            deadline = time.monotonic() + 4
            if readonly:
                db.set_progress_handler(lambda: int(time.monotonic() > deadline), 10000)
            yield db
            if not readonly:
                db.commit()
        except Exception:
            if not readonly:
                db.rollback()
            raise
        finally:
            db.close()

    def append(self, events):
        if not events:
            return
        with self.lock:
            with self.connect() as db:
                db.executemany(
                    f"INSERT INTO events ({COLUMNS}) VALUES ({','.join('?' for _ in COLUMNS.split(','))})",
                    [tuple(e[k] for k in COLUMNS.split(",")) for e in events],
                )

    def maintenance(self, force=False):
        now = time.time()
        if not force and now - self.last_maintenance < 30:
            return
        with self.lock:
            with self.connect() as db:
                deleted = db.execute(
                    "DELETE FROM events WHERE ts < ?",
                    (now - self.config.retention_days * 86400,),
                ).rowcount
                # Sequential integer ids make the row cap inexpensive without COUNT(*).
                threshold = db.execute(
                    "SELECT id FROM events ORDER BY id DESC LIMIT 1 OFFSET ?",
                    (self.config.max_rows,),
                ).fetchone()
                if threshold:
                    deleted += db.execute(
                        "DELETE FROM events WHERE id <= ?", (threshold[0],)
                    ).rowcount
                pages = db.execute("PRAGMA page_count").fetchone()[0]
                free = db.execute("PRAGMA freelist_count").fetchone()[0]
                max_pages = (
                    self.config.max_db_mb
                    * 1024
                    * 1024
                    // db.execute("PRAGMA page_size").fetchone()[0]
                )
                # Leave headroom before SQLite's hard file size limit.
                if pages - free > max_pages * 0.8:
                    boundary = db.execute(
                        "SELECT id FROM events ORDER BY id LIMIT 1 OFFSET (SELECT COUNT(*) / 5 FROM events)"
                    ).fetchone()
                    if boundary:
                        deleted += db.execute(
                            "DELETE FROM events WHERE id <= ?", (boundary[0],)
                        ).rowcount
                self.pruned += deleted
            with self.connect() as db:
                db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
                db.execute("PRAGMA incremental_vacuum(256)")
            self.last_maintenance = now

    def disk_usage(self):
        return sum(
            p.stat().st_size
            for p in self.config.data_dir.glob("events.sqlite3*")
            if p.is_file()
        )

    def disk_ok(self):
        return (
            shutil.disk_usage(self.config.data_dir).free
            >= self.config.min_free_mb * 1024 * 1024
        )

    def query(self, filters, limit=50, before=None):
        where, params = build_where(filters)
        if before:
            where += " AND id < ?"
            params.append(before)
        with self.connect(True) as db:
            return [
                dict(r)
                for r in db.execute(
                    f"SELECT * FROM events WHERE {where} ORDER BY id DESC LIMIT ?",
                    (*params, limit),
                )
            ]

    def summary(self, filters):
        where, params = build_where(filters)
        since, until = filters["since"], filters["until"]
        bucket = max(60, math.ceil((until - since) / 60 / 60) * 60)
        with self.connect(True) as db:
            totals = dict(
                db.execute(
                    f"SELECT COUNT(*) observations,COUNT(DISTINCT src) sources,COUNT(DISTINCT dport) ports,COALESCE(SUM(outcome='blocked'),0) blocked FROM events WHERE {where}",
                    params,
                ).fetchone()
            )
            trend = [
                dict(r)
                for r in db.execute(
                    f"SELECT CAST(ts / ? AS INTEGER) * ? t,COUNT(*) n FROM events WHERE {where} GROUP BY t ORDER BY t",
                    (bucket, bucket, *params),
                )
            ]
            sources = [
                dict(r)
                for r in db.execute(
                    f"SELECT src,COUNT(*) n,COUNT(DISTINCT dport) ports,MAX(ts) last FROM events WHERE {where} GROUP BY src ORDER BY n DESC LIMIT 10",
                    params,
                )
            ]
            ports = [
                dict(r)
                for r in db.execute(
                    f"SELECT dport,proto,COUNT(*) n FROM events WHERE {where} GROUP BY dport,proto ORDER BY n DESC LIMIT 8",
                    params,
                )
            ]
            wans = [
                dict(r)
                for r in db.execute(
                    f"SELECT wan,COUNT(*) n FROM events WHERE {where} GROUP BY wan ORDER BY n DESC",
                    params,
                )
            ]
            # Fixed UTC windows: fast/bounded; slower scans and boundary-spanning
            # scans can be missed. This is a suspicion heuristic, never blocking.
            scans = [
                dict(r)
                for r in db.execute(
                    f"""
              SELECT src,MAX(ports) ports,SUM(n) n,MAX(last) last FROM (
                SELECT src,CAST(ts / ? AS INTEGER) window,COUNT(DISTINCT dport) ports,
                       COUNT(*) n,MAX(ts) last
                FROM events WHERE {where} AND dport IS NOT NULL
                GROUP BY src,window HAVING ports >= ?
              ) GROUP BY src ORDER BY ports DESC,n DESC LIMIT 20
            """,
                    (self.config.scan_window, *params, self.config.scan_ports),
                )
            ]
            coverage = dict(
                db.execute(
                    "SELECT MIN(ts) oldest,MAX(ts) newest,COUNT(*) stored FROM events"
                ).fetchone()
            )
        indexed = {r["t"]: r["n"] for r in trend}
        times = range(
            int(since // bucket * bucket), int(until // bucket * bucket) + 1, bucket
        )
        return {
            "totals": totals,
            "trend": [{"t": t, "n": indexed.get(t, 0)} for t in times],
            "sources": sources,
            "ports": ports,
            "wans": wans,
            "scans": scans,
            "coverage": coverage,
            "bucket_seconds": bucket,
        }


def build_where(filters):
    terms, params = ["ts >= ?", "ts <= ?"], [filters["since"], filters["until"]]
    for field in ("wan", "proto", "scope", "outcome", "src", "dport"):
        if filters.get(field) not in (None, ""):
            terms.append(field + " = ?")
            params.append(filters[field])
    return " AND ".join(terms), params
