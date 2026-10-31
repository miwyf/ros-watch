"""Administrator recovery; requires local access to the persistent data volume."""

import argparse
import getpass
import json
import sys
from dataclasses import asdict

from .config import Config, Settings
from .control import Control


def import_v01(control, values):
    """One-time import from trusted, local v0.1 effective environment values."""
    if not isinstance(values, dict) or not isinstance(values.get("APP_PASSWORD"), str):
        raise ValueError("Missing legacy APP_PASSWORD for one-time migration")
    settings = asdict(Settings())
    for key in (
        "retention_days",
        "max_rows",
        "max_db_mb",
        "min_free_mb",
        "queue_size",
        "ingest_per_second",
        "scan_ports",
    ):
        settings[key] = int(values.get(key.upper(), settings[key]))
    settings.update(
        allowed_senders=values.get("ALLOWED_SENDERS", ""),
        wan_map=json.loads(values.get("WAN_MAP") or json.dumps(settings["wan_map"])),
        scan_window=int(values.get("SCAN_WINDOW_SECONDS", 300)),
        sampling=values.get("SAMPLING_MODE", "limited"),
    )
    return control.bootstrap(
        values.get("APP_USERNAME", "admin"),
        values["APP_PASSWORD"],
        Settings.from_dict(settings),
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["reset-password", "import-v01"])
    parser.add_argument("--username", help="Optionally recover the username as well")
    parser.add_argument(
        "--stdin",
        action="store_true",
        help="Read legacy settings JSON over stdin for migration only",
    )
    args = parser.parse_args()
    if args.command == "import-v01":
        if not args.stdin:
            parser.error(
                "Use --stdin with legacy configuration JSON; never pass passwords as command arguments"
            )
        imported = import_v01(Control(Config()), json.load(sys.stdin))
        print(
            "Legacy account/settings imported."
            if imported
            else "Existing account preserved; import skipped."
        )
        return
    if not sys.stdin.isatty():
        parser.error(
            "Use an interactive terminal (docker compose exec); do not put passwords in command arguments"
        )
    password = getpass.getpass("New password (15–128 characters): ")
    if password != getpass.getpass("Repeat new password: "):
        raise SystemExit("Passwords do not match")
    Control(Config()).reset_password(password, args.username)
    print("Account updated. Previous web sessions have been invalidated.")


if __name__ == "__main__":
    main()
