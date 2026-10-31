"""Parse RouterOS plain/BSD/RFC5424 syslog envelopes with firewall payloads.

Time is the receiver's UTC time, avoiding RFC3164's missing year/timezone.
Events are logged packet observations, not proof of a successful connection.
"""

import ipaddress
import re
import time


FLOW = re.compile(
    r"(?P<src>\[[^\]]+\](?::\d+)?|[^\s,]+)->(?P<dst>\[[^\]]+\](?::\d+)?|[^\s,]+)"
)
CHAIN = re.compile(r"\b(input|forward):\s+in:([^\s]+)\s+out:([^,\s]+)", re.I)
PROTO = re.compile(r"\bproto\s+([\w-]+)(?:\s+\(([^)]*)\))?", re.I)


def endpoint(value):
    if value.startswith("["):
        end = value.index("]")
        address = str(ipaddress.ip_address(value[1:end]))
        port = int(value[end + 2 :]) if value[end + 1 :].startswith(":") else None
    else:
        try:
            return str(ipaddress.ip_address(value)), None
        except ValueError:
            address, sep, port = value.rpartition(":")
            if not sep:
                raise ValueError("Missing address")
            address, port = str(ipaddress.ip_address(address)), int(port)
    if port is not None and not 0 <= port <= 65535:
        raise ValueError("Invalid port")
    return address, port


def parse_message(raw, sender, wan_map, received_at=None):
    message = raw.decode("utf-8", "replace") if isinstance(raw, bytes) else raw
    message = "".join(c for c in message if c >= " " and c != "\x7f")[:4096]
    chain, proto, flow = (
        CHAIN.search(message),
        PROTO.search(message),
        FLOW.search(message),
    )
    if not (chain and proto and flow):
        return None
    try:
        src, sport = endpoint(flow["src"])
        dst, dport = endpoint(flow["dst"])
    except (ValueError, IndexError):
        return None
    interface = chain[2]
    # Ignore LAN traffic accidentally included in a broad firewall logging rule.
    # Custom prefixes are accepted with an unmapped WAN interface (e.g. ether1).
    prefix = next(
        (
            p
            for p in ("RW-DROP", "RW-INPUT", "RW-FWD")
            if re.search(r"\b" + p + r"\b", message[: chain.start()])
        ),
        "",
    )
    if interface not in wan_map and not prefix:
        return None
    return {
        "ts": received_at if received_at is not None else time.time(),
        "sender": sender,
        "src": src,
        "sport": sport,
        "dst": dst,
        "dport": dport,
        "proto": proto[1].upper(),
        "flags": proto[2] or "",
        "wan": wan_map.get(interface, interface),
        "interface": interface,
        "scope": "router" if chain[1].lower() == "input" else "forward",
        "outcome": "blocked" if prefix == "RW-DROP" else "attempt",
        "raw": message[:1200],
    }
