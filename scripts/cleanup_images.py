"""Manually clean GHCR versions, retaining the current stable and dev image graphs."""

import argparse
import json
import os
import re
import time
import urllib.error
import urllib.request


def manifest_graph(fetch, root):
    keep, pending = set(), [root]
    while pending:
        digest = pending.pop()
        if digest in keep:
            continue
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            raise ValueError("Unexpected manifest digest")
        keep.add(digest)
        manifest, _ = fetch(digest)
        pending.extend(item["digest"] for item in manifest.get("manifests", []))
        if manifest.get("subject"):
            pending.append(manifest["subject"]["digest"])
    return keep


def deletion_plan(versions, keep):
    return [version for version in versions if version["name"] not in keep]


def tagged_roots(versions, tags):
    return {version["name"] for version in versions
            if tags.intersection(version.get("metadata", {}).get("container", {}).get("tags", []))}


def dev_retirement_target(versions, expected, stable_graph):
    targets = [version for version in versions if "dev" in version.get("metadata", {}).get("container", {}).get("tags", [])]
    if not targets:
        return None
    if len(targets) != 1:
        raise RuntimeError("Expected one package version tagged dev")
    target = targets[0]
    if target["name"] != expected or target["name"] in stable_graph or target["metadata"]["container"]["tags"] != ["dev"]:
        raise RuntimeError("dev digest or tags changed, or its version belongs to the stable image")
    return target


def request_json(url, headers, method="GET"):
    request = urllib.request.Request(url, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            data = response.read()
            return (json.loads(data) if data else None), response.headers
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"Registry/package API returned HTTP {error.code}") from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expected-digest", required=True)
    parser.add_argument("--retire-dev-digest")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", args.expected_digest):
        parser.error("Expected a sha256 image index digest")
    if args.retire_dev_digest is not None and not re.fullmatch(r"sha256:[0-9a-f]{64}", args.retire_dev_digest):
        parser.error("Expected a sha256 digest for the legacy dev image")
    if os.environ.get("GITHUB_REPOSITORY", "").lower() != "miwyf/ros-watch":
        parser.error("This maintenance operation is restricted to miwyf/ros-watch")
    token = os.environ["GITHUB_TOKEN"]
    registry_token, _ = request_json(
        "https://ghcr.io/token?service=ghcr.io&scope=repository:miwyf/ros-watch:pull", {}
    )
    registry_headers = {
        "Authorization": "Bearer " + registry_token["token"],
        "Accept": ",".join([
            "application/vnd.oci.image.index.v1+json",
            "application/vnd.oci.image.manifest.v1+json",
            "application/vnd.docker.distribution.manifest.list.v2+json",
            "application/vnd.docker.distribution.manifest.v2+json",
        ]),
    }

    def fetch(reference):
        return request_json(
            "https://ghcr.io/v2/miwyf/ros-watch/manifests/" + reference,
            registry_headers,
        )

    protected_dev = None

    def require_current():
        _, headers = fetch("latest")
        if headers.get("Docker-Content-Digest") != args.expected_digest:
            raise RuntimeError("latest changed; refusing to delete any further versions")
        if protected_dev is not None and tagged_roots(list_versions(), {"dev"}) != protected_dev:
            raise RuntimeError("dev changed; refusing to delete any further versions")

    require_current()
    keep = manifest_graph(fetch, args.expected_digest)
    package = "https://api.github.com/users/miwyf/packages/container/ros-watch/versions"
    headers = {
        "Authorization": "Bearer " + token,
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }

    def list_versions():
        versions, page = [], 1
        while True:
            batch, _ = request_json(f"{package}?state=active&per_page=100&page={page}", headers)
            versions.extend(batch)
            if len(batch) < 100:
                return versions
            page += 1

    versions = list_versions()
    if args.retire_dev_digest is not None:
        target = dev_retirement_target(versions, args.retire_dev_digest, keep)
        if target is None:
            print("No active package version tagged dev")
            return
        version_id = int(target["id"])
        print(json.dumps({"apply": args.apply, "dev_digest": target["name"], "delete_id": version_id}), flush=True)
        if args.apply:
            require_current()
            if dev_retirement_target(list_versions(), args.retire_dev_digest, keep) != target:
                raise RuntimeError("dev changed before deletion")
            request_json(f"{package}/{version_id}", headers, method="DELETE")
            require_current()
            if tagged_roots(list_versions(), {"dev"}):
                raise RuntimeError("dev is still present after deletion")
            for digest in keep:
                fetch(digest)
            print(f"Deleted only dev package version {version_id}", flush=True)
        return

    protected_dev = tagged_roots(versions, {"dev"})
    for digest in protected_dev:
        keep.update(manifest_graph(fetch, digest))
    require_current()
    if not keep.issubset({version["name"] for version in versions}):
        raise RuntimeError("Current referenced manifests are missing from the package version list")
    old = deletion_plan(versions, keep)
    print(json.dumps({"apply": args.apply, "keep": sorted(keep), "delete_count": len(old), "delete_ids": [version["id"] for version in old]}), flush=True)
    if not args.apply:
        return
    for version in old:
        require_current()
        request_json(f"{package}/{int(version['id'])}", headers, method="DELETE")
        print(f"Deleted version {version['id']}", flush=True)
        time.sleep(1)
    require_current()
    for digest in keep:
        fetch(digest)
    remaining = list_versions()
    if {version["name"] for version in remaining} != keep:
        raise RuntimeError("Package contents changed or old versions remain; review required")
    print(json.dumps({"deleted": len(old), "remaining": len(remaining), "current_image_intact": True}), flush=True)


if __name__ == "__main__":
    main()
