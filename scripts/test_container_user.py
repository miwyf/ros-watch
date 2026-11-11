"""Exercise real container identities and Linux bind mounts; all data is synthetic."""

import argparse
import json
import subprocess
import time
import uuid


CAPS = ["CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID"]


def docker(*args, check=True):
    result = subprocess.run(
        ["docker", *args], capture_output=True, text=True, encoding="utf-8"
    )
    if check and result.returncode:
        raise RuntimeError(f"docker {' '.join(args)}\n{result.stdout}{result.stderr}")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="ros-watch:uidgid-local")
    parser.add_argument("--build-image", default="ros-watch:uidgid-build")
    parser.add_argument("--baseline-image", help="Optional old image for migration test")
    parser.add_argument("--report")
    args = parser.parse_args()
    prefix = "ros-watch-user-test-" + uuid.uuid4().hex[:12]
    root = "/tmp/" + prefix
    containers = []
    results = []

    def helper(*command):
        return docker(
            "run", "--rm", "--mount", "type=bind,src=/tmp,dst=/host-tmp",
            "alpine:3.21", *command
        ).stdout.strip()

    def directory(name, owner="0:0"):
        path = root + "/" + name
        host = "/host-tmp/" + prefix + "/" + name
        helper("mkdir", "-p", host)
        helper("chown", owner, host)
        helper("chmod", "0700", host)
        return path

    def start(name, path, env=(), extra=(), image=None):
        container = prefix + "-" + name
        containers.append(container)
        command = [
            "run", "-d", "--name", container, "--read-only", "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges:true", "--memory", "192m",
            "--mount", f"type=bind,src={path},dst=/data",
        ]
        for cap in CAPS:
            command += ["--cap-add", cap]
        for item in env:
            command += ["-e", item]
        docker(*command, *extra, image or args.image)
        return container

    def healthy(container):
        for _ in range(60):
            state = json.loads(docker("inspect", container).stdout)[0]["State"]
            if not state["Running"]:
                break
            if docker("exec", container, "/ros-watch", "healthcheck", check=False).returncode == 0:
                return
            time.sleep(0.25)
        raise AssertionError(docker("logs", container).stdout + docker("logs", container).stderr)

    def identity(container, uid, gid):
        status = docker(
            "run", "--rm", "--pid", "container:" + container,
            "alpine:3.21", "cat", "/proc/1/status"
        ).stdout
        fields = dict(line.split(":", 1) for line in status.splitlines() if ":" in line)
        assert fields["Name"].strip() == "ros-watch", fields
        assert [int(v) for v in fields["Uid"].split()] == [uid] * 4, fields
        assert [int(v) for v in fields["Gid"].split()] == [gid] * 4, fields
        assert int(fields["CapEff"], 16) == 0, fields
        assert int(fields["CapPrm"], 16) == 0, fields
        assert fields["NoNewPrivs"].strip() == "1", fields

    def passed(name, **details):
        results.append({"case": name, "passed": True, **details})
        print("PASS", name, flush=True)

    try:
        cases = [
            ("default", (), 1000, 1000),
            ("empty", ("PUID=", "PGID="), 1000, 1000),
            ("unraid", ("PUID=99", "PGID=100"), 99, 100),
            ("uid-only", ("PUID=1234",), 1234, 1000),
            ("gid-only", ("PGID=2345",), 1000, 2345),
        ]
        for name, env, uid, gid in cases:
            path = directory(name, "99:100")
            container = start(name, path, env)
            healthy(container)
            identity(container, uid, gid)
            assert helper("stat", "-c", "%u:%g", "/host-tmp/" + prefix + "/" + name) == f"{uid}:{gid}"
            docker("stop", container)
            docker("start", container)
            healthy(container)
            identity(container, uid, gid)
            passed(name, uid=uid, gid=gid, restart=True)
            docker("stop", container)

        # A real old-format account, settings and collected records survive ID migration.
        path = directory("migration", "10001:10001")
        old = start("old", path, ("PUID=10001", "PGID=10001"), image=args.baseline_image)
        healthy(old)
        probe = docker(
            "run", "--rm", "--network", "container:" + old,
            "--entrypoint", "/out/probe", args.build_image,
            "--seconds", "2", "--rate", "10"
        )
        probe_report = json.loads(probe.stdout)
        docker("stop", old)
        host = "/host-tmp/" + prefix + "/migration"
        key_hash = helper("sha256sum", host + "/session.key").split()[0]
        # Keep a copy for a read-only SQL comparison after the new process starts.
        helper("cp", host + "/control.sqlite3", host + "/before-control.sqlite3")
        migrated = start("migrated", path)
        healthy(migrated)
        identity(migrated, 1000, 1000)
        assert helper("sha256sum", host + "/session.key").split()[0] == key_hash
        # Compare through SQLite rather than file hashes: valid SQLite files can change.
        compare_source = '''package main
import("database/sql";"fmt";_ "modernc.org/sqlite")
func main(){
d,e:=sql.Open("sqlite","file:/data/control.sqlite3?mode=ro");if e!=nil{panic(e)}
_,e=d.Exec("ATTACH DATABASE 'file:/data/before-control.sqlite3?mode=ro' AS old");if e!=nil{panic(e)}
for _,q:=range []string{
"SELECT count(*) FROM (SELECT * FROM account EXCEPT SELECT * FROM old.account)",
"SELECT count(*) FROM (SELECT * FROM old.account EXCEPT SELECT * FROM account)",
"SELECT count(*) FROM (SELECT * FROM settings EXCEPT SELECT * FROM old.settings)",
"SELECT count(*) FROM (SELECT * FROM old.settings EXCEPT SELECT * FROM settings)"}{var n int;if e=d.QueryRow(q).Scan(&n);e!=nil||n!=0{panic(fmt.Sprint(n,e))}}
e=d.Close();if e!=nil{panic(e)}
d,e=sql.Open("sqlite","file:/data/events.sqlite3?mode=ro");if e!=nil{panic(e)}
var n int;if e=d.QueryRow("SELECT count(*) FROM events").Scan(&n);e!=nil||n!=20{panic(fmt.Sprint(n,e))}
fmt.Println("account/settings unchanged; 20 records retained")}
'''
        check = subprocess.run(
            ["docker", "run", "--rm", "-i", "--mount", f"type=bind,src={path},dst=/data,readonly",
             "--entrypoint", "/bin/sh", args.build_image,
             "-c", "cat > /tmp/check.go && go run /tmp/check.go"],
            input=compare_source, text=True, capture_output=True, encoding="utf-8"
        )
        assert check.returncode == 0, check.stdout + check.stderr
        passed("old-data-migration", session_sha256=key_hash, probe=probe_report)
        docker("stop", migrated)

        custom = start("migrated-unraid", path, ("PUID=99", "PGID=100"))
        healthy(custom)
        identity(custom, 99, 100)
        assert helper("sha256sum", host + "/session.key").split()[0] == key_hash
        assert helper("stat", "-c", "%u:%g", host + "/before-control.sqlite3") == "0:0"
        check = subprocess.run(
            ["docker", "run", "--rm", "-i", "--mount", f"type=bind,src={path},dst=/data,readonly",
             "--entrypoint", "/bin/sh", args.build_image,
             "-c", "cat > /tmp/check.go && go run /tmp/check.go"],
            input=compare_source, text=True, capture_output=True, encoding="utf-8"
        )
        assert check.returncode == 0, check.stdout + check.stderr
        passed("existing-data-custom-ids")
        docker("stop", custom)

        path = directory("docker-user", "99:100")
        container = start("docker-user", path, extra=("--user", "99:100"))
        healthy(container)
        identity(container, 99, 100)
        passed("docker-user-override")
        docker("stop", container)

        for key, value in [("PUID", "abc"), ("PGID", "-1"), ("PUID", "2147483648")]:
            result = docker("run", "--rm", "-e", key + "=" + value, args.image, check=False)
            assert result.returncode == 1 and key in result.stderr, result
        passed("invalid-ids-rejected")

        path = directory("readonly")
        result = docker(
            "run", "--rm", "--mount", f"type=bind,src={path},dst=/data,readonly",
            args.image, check=False
        )
        assert result.returncode == 1 and "prepare DATA_DIR /data for 1000:1000" in result.stderr, result
        passed("readonly-mount-diagnostic")

        result = docker(
            "run", "--rm", "--read-only", "--cap-drop", "ALL", args.image, check=False
        )
        assert result.returncode == 1 and "SETGID" in result.stderr, result
        passed("missing-capabilities-diagnostic")

        result = docker(
            "run", "--rm", "--user", "99:100", "-e", "PUID=1000", args.image, check=False
        )
        assert result.returncode == 1 and "conflict with Docker --user" in result.stderr, result
        passed("conflicting-user-diagnostic")

        print(json.dumps(results, ensure_ascii=False, indent=2))
        if args.report:
            with open(args.report, "w", encoding="utf-8") as report:
                json.dump(results, report, ensure_ascii=False, indent=2)
    finally:
        for container in containers:
            docker("rm", "-fv", container, check=False)
        # Only this run's generated Linux temporary directory is removed.
        assert root == "/tmp/" + prefix and prefix.startswith("ros-watch-user-test-")
        helper("rm", "-rf", "--", "/host-tmp/" + prefix)


if __name__ == "__main__":
    main()
