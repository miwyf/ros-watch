"""Send synthetic RouterOS messages to a development instance, never a router."""

import argparse
import socket
import time


def sample(i):
    return f"<134>Oct 31 12:00:00 demo RW-INPUT input: in:pppoe-out1 out:(none), proto TCP (SYN), 198.51.100.{i % 20 + 1}:{40000 + i % 20000}->192.0.2.1:{1000 + i % 64}, len 60".encode()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=5514)
    p.add_argument("--count", type=int, default=40)
    p.add_argument("--rate", type=float, default=10)
    a = p.parse_args()
    if a.rate <= 0 or not 1 <= a.count <= 100000:
        p.error("count must be 1–100000 and rate positive")
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        for i in range(a.count):
            sock.sendto(sample(i), (a.host, a.port))
            time.sleep(1 / a.rate)
    print(f"Sent {a.count} synthetic messages (delivery is not guaranteed by UDP)")


if __name__ == "__main__":
    main()
