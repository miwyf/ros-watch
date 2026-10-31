import ipaddress
import os
import re
from dataclasses import asdict, dataclass, field
from pathlib import Path


@dataclass(frozen=True)
class Settings:
    allowed_senders: str = ""
    wan_map: dict = field(
        default_factory=lambda: {"pppoe-out1": "WAN1", "pppoe-out2": "WAN2"}
    )
    retention_days: int = 7
    max_rows: int = 500000
    max_db_mb: int = 512
    min_free_mb: int = 256
    queue_size: int = 4096
    ingest_per_second: int = 500
    scan_ports: int = 15
    scan_window: int = 300
    sampling: str = "unknown"

    def networks(self):
        return tuple(
            ipaddress.ip_network(s, strict=False)
            for s in self.allowed_senders.split(",")
            if s
        )

    @classmethod
    def from_dict(cls, values):
        if not isinstance(values, dict) or set(values) - set(cls.__dataclass_fields__):
            raise ValueError("设置中包含未知字段")
        candidate = cls(**values)
        if (
            not isinstance(candidate.allowed_senders, str)
            or len(candidate.allowed_senders) > 4096
        ):
            raise ValueError("白名单格式无效或过长")
        networks = []
        for value in re.split(r"[,\s]+", candidate.allowed_senders.strip()):
            if not value:
                continue
            try:
                network = ipaddress.ip_network(value, strict=False)
            except ValueError:
                raise ValueError("白名单须填写有效的 IP 或网段") from None
            if network.prefixlen == 0:
                raise ValueError("请指定可信来源，不能允许所有地址")
            networks.append(str(network))
        mapping = candidate.wan_map
        if (
            not isinstance(mapping, dict)
            or len(mapping) > 32
            or not all(
                isinstance(k, str)
                and isinstance(v, str)
                and 1 <= len(k) <= 64
                and 1 <= len(v) <= 64
                and all(c >= " " for c in k + v)
                for k, v in mapping.items()
            )
        ):
            raise ValueError("WAN 映射须为接口名称与显示名称，最多 32 项")
        limits = {
            "retention_days": (1, 31),
            "max_rows": (1, 2000000),
            "max_db_mb": (8, 8192),
            "min_free_mb": (0, 1048576),
            "queue_size": (1, 8192),
            "ingest_per_second": (1, 5000),
            "scan_ports": (2, 1000),
            "scan_window": (60, 3600),
        }
        for name, (low, high) in limits.items():
            number = getattr(candidate, name)
            if type(number) is not int or not low <= number <= high:
                raise ValueError(f"{name} 的范围应为 {low}–{high}")
        if candidate.sampling not in {"unknown", "limited", "full"}:
            raise ValueError("采样模式无效")
        return cls(
            **{
                **asdict(candidate),
                "allowed_senders": ",".join(dict.fromkeys(networks)),
            }
        )


@dataclass
class Config:
    # Only deployment parameters live in the environment. Runtime settings and
    # account credentials are persisted in control.sqlite3.
    data_dir: Path = field(default_factory=lambda: Path(os.getenv("DATA_DIR", "data")))
    http_host: str = field(default_factory=lambda: os.getenv("HTTP_HOST", "0.0.0.0"))
    http_port: int = field(default_factory=lambda: int(os.getenv("HTTP_PORT", "8080")))
    syslog_host: str = field(
        default_factory=lambda: os.getenv("SYSLOG_HOST", "0.0.0.0")
    )
    syslog_port: int = field(
        default_factory=lambda: int(os.getenv("SYSLOG_PORT", "5514"))
    )
    cookie_secure: bool = field(
        default_factory=lambda: os.getenv("COOKIE_SECURE", "false").lower() == "true"
    )
    demo: bool = field(
        default_factory=lambda: os.getenv("DEMO_MODE", "false").lower() == "true"
    )
    settings: Settings = field(default_factory=Settings)

    def validate(self):
        if self.demo and self.http_host not in {"127.0.0.1", "::1", "localhost"}:
            raise ValueError("Demo mode must listen on loopback")
        Settings.from_dict(asdict(self.settings))
