import ipaddress
import logging
import queue
import socket
import sqlite3
import threading
import time
from collections import Counter

from .parser import parse_message


class Collector:
    def __init__(self, config, store):
        self.config, self.store = config, store
        self.networks = config.validate()
        self.queue = queue.Queue(maxsize=config.queue_size)
        self.stop_event = threading.Event()
        self.counts = Counter()
        self.mutex = threading.Lock()
        self.started_at = time.time()
        self.last_received = None
        self.last_written = None
        self.error = ""
        self.threads = []
        self.sock = None
        self.tokens = float(config.ingest_per_second)
        self.last_token_time = time.monotonic()

    def increment(self, key, n=1):
        with self.mutex:
            self.counts[key] += n

    def accept(self, data, sender, now=None):
        self.increment("received")
        address = ipaddress.ip_address(sender)
        if not any(address in network for network in self.networks):
            self.increment("untrusted")
            return
        if len(data) > 4096:
            self.increment("oversized")
            return
        tick = time.monotonic()
        self.tokens = min(
            self.config.ingest_per_second,
            self.tokens + (tick - self.last_token_time) * self.config.ingest_per_second,
        )
        self.last_token_time = tick
        if self.tokens < 1:
            self.increment("rate_dropped")
            return
        self.tokens -= 1
        event = parse_message(data, sender, self.config.wan_map, now)
        if not event:
            self.increment("ignored")
            return
        self.last_received = event["ts"]
        try:
            self.queue.put_nowait(event)
            self.increment("parsed")
        except queue.Full:
            self.increment("queue_dropped")

    def start(self):
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 1024 * 1024)
        self.sock.bind((self.config.syslog_host, self.config.syslog_port))
        self.bound_port = self.sock.getsockname()[1]
        self.sock.settimeout(0.5)
        self.store.maintenance(force=True)
        for target in (self.receive_loop, self.write_loop):
            thread = threading.Thread(target=target, daemon=True, name=target.__name__)
            thread.start()
            self.threads.append(thread)

    def receive_loop(self):
        while not self.stop_event.is_set():
            try:
                data, addr = self.sock.recvfrom(4097)
                self.accept(data, addr[0])
            except socket.timeout:
                continue
            except OSError:
                if not self.stop_event.is_set():
                    self.error = "receiver_failed"
                    logging.exception("Syslog socket failed")
                break

    def write_loop(self):
        while not self.stop_event.is_set() or not self.queue.empty():
            batch = []
            try:
                batch.append(self.queue.get(timeout=0.5))
                deadline = time.monotonic() + 0.25
                while len(batch) < 250 and time.monotonic() < deadline:
                    try:
                        batch.append(
                            self.queue.get(
                                timeout=max(0.001, deadline - time.monotonic())
                            )
                        )
                    except queue.Empty:
                        break
                if self.store.disk_ok():
                    self.store.append(batch)
                    self.last_written = time.time()
                    self.increment("written", len(batch))
                    self.error = ""
                else:
                    self.increment("storage_dropped", len(batch))
                    self.error = "disk_low"
            except queue.Empty:
                pass
            except (sqlite3.Error, OSError):
                self.increment("storage_dropped", len(batch))
                self.error = "storage_failed"
                # No raw log contents or source addresses are sent to stdout.
                logging.warning(
                    "Storage write failed; records dropped, see dashboard status"
                )
                time.sleep(0.2)
            finally:
                for _ in batch:
                    self.queue.task_done()
            try:
                self.store.maintenance()
            except (sqlite3.Error, OSError):
                self.error = "maintenance_failed"
                logging.warning("Storage maintenance failed")
                time.sleep(1)

    def status(self):
        with self.mutex:
            counters = dict(self.counts)
        return {
            "counters": counters,
            "started_at": self.started_at,
            "last_received": self.last_received,
            "last_written": self.last_written,
            "queue": self.queue.qsize(),
            "error": self.error,
            "db_bytes": self.store.disk_usage(),
            "pruned": self.store.pruned,
            "sampling": self.config.sampling,
            "demo": self.config.demo,
            "scan_ports": self.config.scan_ports,
            "scan_window": self.config.scan_window,
            "retention_days": self.config.retention_days,
            "max_db_mb": self.config.max_db_mb,
        }

    def stop(self):
        self.stop_event.set()
        if self.sock:
            self.sock.close()
        for thread in self.threads:
            thread.join(timeout=5)
