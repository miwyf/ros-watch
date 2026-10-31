import ipaddress
import json
import os
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class Config:
    data_dir: Path = field(default_factory=lambda: Path(os.getenv("DATA_DIR", "data")))
    password: str = field(default_factory=lambda: os.getenv("APP_PASSWORD", ""))
    http_host: str = field(default_factory=lambda: os.getenv("HTTP_HOST", "0.0.0.0"))
    http_port: int = field(default_factory=lambda: int(os.getenv("HTTP_PORT", "8080")))
    syslog_host: str = field(
        default_factory=lambda: os.getenv("SYSLOG_HOST", "0.0.0.0")
    )
    syslog_port: int = field(
        default_factory=lambda: int(os.getenv("SYSLOG_PORT", "5514"))
    )
    allowed_senders: str = field(
        default_factory=lambda: os.getenv("ALLOWED_SENDERS", "")
    )
    wan_map: dict = field(
        default_factory=lambda: json.loads(
            os.getenv("WAN_MAP") or '{"pppoe-out1":"WAN1","pppoe-out2":"WAN2"}'
        )
    )
    retention_days: int = field(
        default_factory=lambda: int(os.getenv("RETENTION_DAYS", "7"))
    )
    max_rows: int = field(default_factory=lambda: int(os.getenv("MAX_ROWS", "500000")))
    max_db_mb: int = field(default_factory=lambda: int(os.getenv("MAX_DB_MB", "512")))
    min_free_mb: int = field(
        default_factory=lambda: int(os.getenv("MIN_FREE_MB", "256"))
    )
    queue_size: int = field(
        default_factory=lambda: int(os.getenv("QUEUE_SIZE", "4096"))
    )
    ingest_per_second: int = field(
        default_factory=lambda: int(os.getenv("INGEST_PER_SECOND", "500"))
    )
    scan_ports: int = field(default_factory=lambda: int(os.getenv("SCAN_PORTS", "15")))
    scan_window: int = field(
        default_factory=lambda: int(os.getenv("SCAN_WINDOW_SECONDS", "300"))
    )
    sampling: str = field(default_factory=lambda: os.getenv("SAMPLING_MODE", "unknown"))
    cookie_secure: bool = field(
        default_factory=lambda: os.getenv("COOKIE_SECURE", "false").lower() == "true"
    )
    demo: bool = field(
        default_factory=lambda: os.getenv("DEMO_MODE", "false").lower() == "true"
    )

    def validate(self):
        if len(self.password) < 12:
            raise ValueError("APP_PASSWORD must contain at least 12 characters")
        if self.sampling not in {"unknown", "limited", "full"}:
            raise ValueError("SAMPLING_MODE must be unknown, limited or full")
        for name in (
            "retention_days",
            "max_rows",
            "max_db_mb",
            "queue_size",
            "ingest_per_second",
            "scan_ports",
            "scan_window",
        ):
            if getattr(self, name) <= 0:
                raise ValueError(f"{name} must be positive")
        if self.min_free_mb < 0 or self.max_db_mb < 8:
            raise ValueError("Invalid disk protection settings")
        if self.demo and self.http_host not in {"127.0.0.1", "::1", "localhost"}:
            raise ValueError("Demo mode must listen on loopback")
        if not self.demo and not self.allowed_senders.strip():
            raise ValueError("ALLOWED_SENDERS must list the router's LAN IP or CIDR")
        if not isinstance(self.wan_map, dict) or not all(
            isinstance(k, str) and isinstance(v, str) for k, v in self.wan_map.items()
        ):
            raise ValueError("WAN_MAP must be a JSON object with string values")
        return [
            ipaddress.ip_network(s.strip(), strict=False)
            for s in self.allowed_senders.split(",")
            if s.strip()
        ]
