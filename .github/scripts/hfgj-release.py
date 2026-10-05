"""Meta-only maintenance. Prepare locally; promote/publish only after CI gates."""
import argparse
import datetime as dt
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile

REPOSITORY = "hfgj/mihomo"
CHANNEL = "HFGJ-Stable"
UPSTREAM = "https://github.com/MetaCubeX/mihomo.git"
TARGETS = {
    "mihomo-linux-arm64": ("gz", "elf", 183),
    "mihomo-windows-amd64-v2": ("zip", "pe", 0x8664),
    "mihomo-windows-arm64": ("zip", "pe", 0xaa64),
    "mihomo-darwin-arm64": ("gz", "macho", 0x100000c),
    "mihomo-darwin-amd64-v1": ("gz", "macho", 0x1000007),
}
SHA = re.compile(r"[0-9a-f]{40}")
VERSION = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+-hfgj\.[0-9a-f]{12}")
LIMIT = 32 * 1024 * 1024

def digest(data):
    return hashlib.sha256(data).hexdigest()

def git(root, *args, env=None):
    return subprocess.check_output(["git", *args], cwd=root, env=env, text=True).strip()

def ancestor(root, older, newer):
    p = subprocess.run(["git", "merge-base", "--is-ancestor", older, newer], cwd=root)
    if p.returncode not in (0, 1):
        raise ValueError("Unable to check ancestry")
    return p.returncode == 0

def validate_metadata(meta):
    for key in ("source_sha", "base_sha", "mirror_sha", "upstream_sha", "upstream_tag_sha"):
        if not SHA.fullmatch(meta.get(key, "")):
            raise ValueError("Invalid locked source identity")
    if (meta.get("repository") != REPOSITORY or meta.get("channel") != CHANNEL
            or not VERSION.fullmatch(meta.get("version", ""))
            or meta["version"] != meta["upstream_tag"] + "-hfgj." + meta["source_sha"][:12]):
        raise ValueError("Wrong maintenance channel/version")
    if (not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", meta.get("upstream_tag", ""))
            or type(meta.get("upstream_is_tag")) is not bool
            or meta["upstream_is_tag"] != (meta["upstream_sha"] == meta["upstream_tag_sha"])):
        raise ValueError("Invalid upstream identity")
    dt.datetime.strptime(meta.get("build_time", ""), "%Y-%m-%dT%H:%M:%SZ")
    return meta

def prepare(root, output, upstream=UPSTREAM, published_version=None):
    if git(root, "status", "--porcelain"):
        raise ValueError("Uncommitted source; nothing prepared")
    git(root, "fetch", "--no-tags", "origin",
        "refs/heads/hfgj:refs/remotes/origin/hfgj",
        "refs/heads/Meta:refs/remotes/origin/Meta")
    base = git(root, "rev-parse", "origin/hfgj")
    if git(root, "rev-parse", "HEAD") != base:
        raise ValueError("Source changed since checkout; retry latest hfgj")
    mirror = git(root, "rev-parse", "origin/Meta")
    git(root, "fetch", "--no-tags", upstream,
        "refs/heads/Meta:refs/remotes/hfgj-upstream/Meta",
        "refs/tags/v*:refs/tags/hfgj-upstream/v*")
    upstream_sha = git(root, "rev-parse", "refs/remotes/hfgj-upstream/Meta")
    if not ancestor(root, mirror, upstream_sha):
        raise ValueError("Meta mirror diverged or upstream rewound; no overwrite")
    tags = []
    for ref in git(root, "for-each-ref", "--format=%(refname)", "refs/tags/hfgj-upstream/").splitlines():
        name = ref.rsplit("/", 1)[-1]
        if re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", name):
            commit = git(root, "rev-parse", ref + "^{commit}")
            if ancestor(root, commit, upstream_sha):
                tags.append((tuple(map(int, name[1:].split("."))), name, commit))
    if not tags:
        raise ValueError("No official stable version ancestor")
    _, tag, tag_sha = max(tags)
    git(root, "checkout", "-b", "hfgj-maintenance-candidate", base)
    stamp = max(int(git(root, "show", "-s", "--format=%ct", sha))
                for sha in (base, upstream_sha)) + 1
    env = dict(os.environ, GIT_AUTHOR_NAME="HFGJ maintenance",
               GIT_COMMITTER_NAME="HFGJ maintenance",
               GIT_AUTHOR_EMAIL="41898282+github-actions[bot]@users.noreply.github.com",
               GIT_COMMITTER_EMAIL="41898282+github-actions[bot]@users.noreply.github.com",
               GIT_AUTHOR_DATE=f"@{stamp} +0000", GIT_COMMITTER_DATE=f"@{stamp} +0000")
    p = subprocess.run(["git", "merge", "--no-edit", upstream_sha], cwd=root,
                       env=env, capture_output=True, text=True)
    if p.returncode:
        subprocess.run(["git", "merge", "--abort"], cwd=root, check=True)
        raise ValueError("Upstream merge conflict; official refs and release retained")
    source = git(root, "rev-parse", "HEAD")
    if not ancestor(root, upstream_sha, source) or not ancestor(root, base, source):
        raise ValueError("Candidate lost a parent")
    meta = {
        "repository": REPOSITORY, "channel": CHANNEL, "base_sha": base,
        "mirror_sha": mirror, "upstream_sha": upstream_sha, "source_sha": source,
        "upstream_tag": tag, "upstream_tag_sha": tag_sha,
        "upstream_is_tag": upstream_sha == tag_sha,
        "version": tag + "-hfgj." + source[:12],
        "build_time": dt.datetime.fromtimestamp(
            int(git(root, "show", "-s", "--format=%ct", source)), dt.timezone.utc
        ).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    validate_metadata(meta)
    meta["release_needed"] = published_version != meta["version"]
    meta["changed"] = meta["release_needed"] or mirror != upstream_sha
    output.mkdir(parents=True, exist_ok=False)
    (output / "maintenance.json").write_text(json.dumps(meta, indent=2) + "\n")
    git(root, "bundle", "create", str(output / "source.bundle"), "HEAD")
    return meta

def import_candidate(root, directory, meta):
    validate_metadata(meta)
    git(root, "fetch", str(directory / "source.bundle"),
        "HEAD:refs/remotes/hfgj-candidate/locked")
    if git(root, "rev-parse", "refs/remotes/hfgj-candidate/locked") != meta["source_sha"]:
        raise ValueError("Candidate bundle identity mismatch")
    for parent in ("base_sha", "upstream_sha"):
        if not ancestor(root, meta[parent], meta["source_sha"]):
            raise ValueError("Candidate bundle lost a parent")

def promote(root, directory, meta):
    import_candidate(root, directory, meta)
    git(root, "fetch", "--no-tags", "origin",
        "refs/heads/hfgj:refs/remotes/origin/hfgj",
        "refs/heads/Meta:refs/remotes/origin/Meta")
    if (git(root, "rev-parse", "origin/hfgj") != meta["base_sha"]
            or git(root, "rev-parse", "origin/Meta") != meta["mirror_sha"]):
        raise ValueError("Concurrent branch update; refusing stale candidate")
    if not ancestor(root, meta["mirror_sha"], meta["upstream_sha"]):
        raise ValueError("Mirror update is not a fast-forward")
    git(root, "push", "--atomic", "origin",
        meta["upstream_sha"] + ":refs/heads/Meta",
        meta["source_sha"] + ":refs/heads/hfgj")

def unpack(path, base, extension):
    data = path.read_bytes()
    if not 0 < len(data) < LIMIT:
        raise ValueError("Core package exceeds updater limit")
    if extension == "zip":
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            entries = archive.infolist()
            if len(entries) != 1 or entries[0].filename != base + ".exe":
                raise ValueError("Unexpected ZIP entry")
            if not 64 <= entries[0].file_size <= 128 * 1024 * 1024:
                raise ValueError("Unexpected executable size")
            binary = archive.read(entries[0])
    else:
        if data[:4] != b"\x1f\x8b\x08\x08":
            raise ValueError("Missing/unsupported gzip executable filename")
        end = data.find(b"\0", 10)
        if end < 0 or data[10:end] != base.encode():
            raise ValueError("Wrong gzip executable filename")
        with gzip.GzipFile(fileobj=io.BytesIO(data)) as archive:
            binary = archive.read(128 * 1024 * 1024 + 1)
    if not 64 <= len(binary) <= 128 * 1024 * 1024:
        raise ValueError("Unexpected executable size")
    return binary

def check_binary(binary, kind, machine):
    if kind == "elf":
        if binary[:6] != b"\x7fELF\x02\x01" or int.from_bytes(binary[18:20], "little") != machine:
            raise ValueError("Wrong Linux executable")
        offset = int.from_bytes(binary[32:40], "little")
        size = int.from_bytes(binary[54:56], "little")
        count = int.from_bytes(binary[56:58], "little")
        if not count or size < 56 or offset + size * count > len(binary):
            raise ValueError("Invalid ELF program headers")
        if any(int.from_bytes(binary[offset + i * size:offset + i * size + 4], "little") in (2, 3)
               for i in range(count)):
            raise ValueError("Linux executable is not static")
    elif kind == "pe":
        offset = int.from_bytes(binary[60:64], "little")
        if (binary[:2] != b"MZ" or offset < 64 or offset + 24 > len(binary)
                or binary[offset:offset + 4] != b"PE\0\0"
                or int.from_bytes(binary[offset + 4:offset + 6], "little") != machine):
            raise ValueError("Wrong Windows executable")
    elif binary[:4] != b"\xcf\xfa\xed\xfe" or int.from_bytes(binary[4:8], "little") != machine:
        raise ValueError("Wrong macOS executable")

def assemble(directory, meta):
    validate_metadata(meta)
    expected = {f"{base}-{meta['version']}.{ext}" for base, (ext, _, _) in TARGETS.items()}
    actual = {p.name for p in directory.iterdir() if p.is_file()}
    if actual != expected:
        raise ValueError("Expected exactly all five locked core packages")
    packages = {}
    for base, (ext, kind, machine) in TARGETS.items():
        name = f"{base}-{meta['version']}.{ext}"
        binary = unpack(directory / name, base, ext)
        check_binary(binary, kind, machine)
        source_url = f"https://github.com/{REPOSITORY}/releases/download/{CHANNEL}/"
        if meta["version"].encode() not in binary or source_url.encode() not in binary:
            raise ValueError("Missing locked version or HFGJ update source")
        packages[name] = {"sha256": digest((directory / name).read_bytes()),
                          "binary_sha256": digest(binary)}
    manifest = dict(meta, packages=packages)
    (directory / "maintenance-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (directory / "version.txt").write_text(meta["version"] + "\n")
    (directory / "SHA256SUMS").write_text("".join(
        digest((directory / name).read_bytes()) + "  " + name + "\n"
        for name in sorted(expected | {"version.txt"})))
    return manifest

class GitHub:
    def __init__(self, token):
        self.token = token

    def api(self, path, method="GET", data=None):
        body = None if data is None else json.dumps(data).encode()
        return self.request("https://api.github.com/repos/" + REPOSITORY + path,
                            method, body, "application/json")

    def request(self, url, method, data, content_type):
        if not url.startswith(("https://api.github.com/", "https://uploads.github.com/")):
            raise ValueError("Refusing credentials on another host")
        headers = {"Authorization": "Bearer " + self.token, "User-Agent": "hfgj-maintenance",
                   "Accept": "application/vnd.github+json", "Content-Type": content_type}
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        with urllib.request.urlopen(req, timeout=60) as response:
            payload = response.read()
        return json.loads(payload) if payload else None

    def upload(self, release, name, data):
        url = release["upload_url"].split("{", 1)[0] + "?name=" + urllib.parse.quote(name, safe="")
        return self.request(url, "POST", data, "application/octet-stream")

    def download(self, asset):
        url = asset["browser_download_url"]
        prefix = f"https://github.com/{REPOSITORY}/releases/download/{CHANNEL}/"
        if not url.startswith(prefix):
            raise ValueError("Unexpected public release URL")
        req = urllib.request.Request(url, headers={"User-Agent": "hfgj-maintenance",
                                                   "Cache-Control": "no-cache"})
        with urllib.request.urlopen(req, timeout=60) as response:
            data = response.read(LIMIT + 1)
        if len(data) > LIMIT:
            raise ValueError("Release download exceeds limit")
        return data

def checksum_records(data):
    records = {}
    for line in data.decode().splitlines():
        parts = line.split()
        if (len(parts) != 2 or not re.fullmatch(r"[0-9a-f]{64}", parts[0])
                or not re.fullmatch(r"[A-Za-z0-9._-]+", parts[1]) or parts[1] in records):
            raise ValueError("Invalid existing channel checksums")
        records[parts[1]] = parts[0]
    return records

def publish(client, directory, meta, backup):
    """Retain old archives; change version.txt last; restore metadata on failure."""
    validate_metadata(meta)
    manifest = json.loads((directory / "maintenance-manifest.json").read_text())
    expected = {f"{base}-{meta['version']}.{ext}" for base, (ext, _, _) in TARGETS.items()}
    if set(manifest.get("packages", {})) != expected:
        raise ValueError("Manifest must contain exactly all five safe package names")
    if {k: manifest[k] for k in meta} != meta:
        raise ValueError("Build manifest differs from locked source")
    import tempfile
    with tempfile.TemporaryDirectory() as temp:
        fresh = Path(temp)
        for name in manifest["packages"]:
            (fresh / name).write_bytes((directory / name).read_bytes())
        if assemble(fresh, meta) != manifest:
            raise ValueError("Build artifacts were changed")
    if client.api("/git/ref/heads/hfgj")["object"]["sha"] != meta["source_sha"]:
        raise ValueError("hfgj advanced after CI; refusing stale release")
    release = client.api("/releases/tags/" + CHANNEL)
    if release.get("draft") or release.get("prerelease") or release.get("immutable"):
        raise ValueError("Existing stable release cannot be updated safely")
    assets = {a["name"]: a for a in release["assets"]}
    old_tag = client.api("/git/ref/tags/" + CHANNEL)["object"]["sha"]
    old = {name: client.download(assets[name]) for name in ("version.txt", "SHA256SUMS")}
    records = checksum_records(old["SHA256SUMS"])
    old_version = old["version.txt"].decode().strip()
    if not VERSION.fullmatch(old_version) or records.get("version.txt") != digest(old["version.txt"]):
        raise ValueError("Invalid existing channel version/checksum")
    numbers = lambda version: tuple(map(int, version.split("-hfgj.")[0][1:].split(".")))
    if numbers(old_version) > numbers(meta["version"]):
        raise ValueError("Refusing stable version downgrade")
    backup.mkdir(parents=True, exist_ok=False)
    for name, data in old.items():
        (backup / name).write_bytes(data)
    (backup / "release.json").write_text(json.dumps(
        {"release_id": release["id"], "tag_sha": old_tag, "body": release.get("body")}, indent=2) + "\n")

    def verify(asset, data):
        api_digest = asset.get("digest")
        if api_digest and api_digest != "sha256:" + digest(data):
            raise ValueError("Uploaded asset API digest differs")
        if client.download(asset) != data:
            raise ValueError("Uploaded asset bytes differ")

    def ensure(name, data):
        if name in assets:
            verify(assets[name], data)
            return assets[name]
        asset = client.upload(release, name, data)
        verify(asset, data)
        assets[name] = asset
        return asset

    for name in manifest["packages"]:
        if name in assets:
            verify(assets[name], (directory / name).read_bytes())
    if old_version == meta["version"]:
        if old_tag != meta["source_sha"]:
            raise ValueError("Same version has inconsistent channel tag")
        if not expected.issubset(assets):
            raise ValueError("Same version has missing channel packages")
        if any(records.get(name) != item["sha256"] for name, item in manifest["packages"].items()):
            raise ValueError("Same version has different channel checksums")
        return {"status": "unchanged", "version": meta["version"]}
    for name, item in manifest["packages"].items():
        ensure(name, (directory / name).read_bytes())
        records[name] = item["sha256"]
    ensure("hfgj-manifest-" + meta["version"] + ".json",
           (directory / "maintenance-manifest.json").read_bytes())
    version_data = (meta["version"] + "\n").encode()
    records["version.txt"] = digest(version_data)
    sums = "".join(value + "  " + name + "\n" for name, value in sorted(records.items())).encode()

    def replace(name, data, suffix):
        stage = ensure(name + ".hfgj-" + suffix, data)
        if name in assets:
            client.api("/releases/assets/" + str(assets[name]["id"]), "DELETE")
            del assets[name]
        renamed = client.api("/releases/assets/" + str(stage["id"]), "PATCH", {"name": name})
        assets.pop(stage["name"], None)
        assets[name] = renamed
        verify(renamed, data)

    # Check outside the rollback region: never overwrite a concurrent publisher.
    if client.api("/git/ref/heads/hfgj")["object"]["sha"] != meta["source_sha"]:
        raise ValueError("hfgj changed during upload")
    if client.api("/git/ref/tags/" + CHANNEL)["object"]["sha"] != old_tag:
        raise ValueError("Channel tag changed during upload")
    try:
        replace("SHA256SUMS", sums, meta["source_sha"][:12])
        client.api("/git/refs/tags/" + CHANNEL, "PATCH", {"sha": meta["source_sha"], "force": True})
        client.api("/releases/" + str(release["id"]), "PATCH", {
            "body": f"HFGJ Stable\nCore: {meta['version']}\nSource: {meta['source_sha']}\n"
                    f"Upstream Meta: {meta['upstream_sha']}\nStable ancestor: {meta['upstream_tag']}\n"})
        replace("version.txt", version_data, meta["source_sha"][:12])
    except Exception as original:
        errors = []
        for name in ("SHA256SUMS", "version.txt"):
            try:
                replace(name, old[name], "restore-" + meta["source_sha"][:12])
            except Exception:
                errors.append(name)
        for path, data in (
            ("/git/refs/tags/" + CHANNEL, {"sha": old_tag, "force": True}),
            ("/releases/" + str(release["id"]), {"body": release.get("body")})):
            try:
                client.api(path, "PATCH", data)
            except Exception:
                errors.append(path)
        if errors:
            raise ValueError("Publish failed; metadata recovery incomplete; keep recovery artifact") from original
        raise ValueError("Publish failed; previous channel metadata restored") from original
    return {"status": "published", "version": meta["version"], "source_sha": meta["source_sha"]}

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("command", choices=("prepare", "promote", "assemble", "publish"))
    p.add_argument("--root", type=Path, default=Path.cwd())
    p.add_argument("--locked", type=Path, required=True)
    p.add_argument("--packages", type=Path)
    p.add_argument("--backup", type=Path)
    args = p.parse_args()
    if os.environ.get("GITHUB_REPOSITORY") != REPOSITORY:
        p.error("Cloud entry point is restricted to hfgj/mihomo")
    if args.command == "prepare":
        client = GitHub(os.environ.get("GH_TOKEN", ""))
        try:
            r = client.api("/releases/tags/" + CHANNEL)
            asset = next(a for a in r["assets"] if a["name"] == "version.txt")
            published = client.download(asset).decode().strip()
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
            published = None
        result = prepare(args.root.resolve(), args.locked.resolve(), published_version=published)
        if os.environ.get("GITHUB_OUTPUT"):
            with open(os.environ["GITHUB_OUTPUT"], "a") as stream:
                for name in ("source_sha", "version", "build_time", "changed", "release_needed"):
                    value = result[name]
                    stream.write(f"{name}={str(value).lower() if isinstance(value, bool) else value}\n")
    else:
        meta = validate_metadata(json.loads((args.locked / "maintenance.json").read_text()))
        if args.command == "promote":
            promote(args.root, args.locked, meta)
            result = {"status": "promoted", "source_sha": meta["source_sha"]}
        elif args.command == "assemble":
            result = assemble(args.packages, meta)
        else:
            if not args.backup:
                p.error("Publication requires a recovery directory")
            result = publish(GitHub(os.environ["GH_TOKEN"]), args.packages, meta, args.backup)
    print(json.dumps(result))

if __name__ == "__main__":
    try:
        main()
    except (ValueError, subprocess.CalledProcessError, urllib.error.URLError) as error:
        print("HFGJ maintenance failed:", str(error), file=sys.stderr)
        sys.exit(1)
