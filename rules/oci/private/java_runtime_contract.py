# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Check the merged Java runtime filesystem without executing a container."""

import json
import posixpath
import re
import sys
import tarfile
from pathlib import Path


# Debian 13's security backport, rather than the upstream Brotli version.
BROTLI_FLOOR = "1.1.0-2+deb13u1"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def version_compare(left, right):
    """Compare Debian versions, including epochs, revisions and tilde suffixes."""
    # https://www.debian.org/doc/debian-policy/ch-controlfields.html#version
    def parts(version):
        epoch, sep, rest = version.partition(":")
        epoch, rest = (int(epoch), rest) if sep else (0, version)
        upstream, sep, revision = rest.rpartition("-")
        return epoch, upstream if sep else rest, revision if sep else "0"

    def order(char):
        if char == "~":
            return -1
        if not char or char.isdigit():
            return 0
        return ord(char) if char.isalpha() else ord(char) + 256

    a, b = parts(left), parts(right)
    if a[0] != b[0]:
        return (a[0] > b[0]) - (a[0] < b[0])
    for x, y in zip(a[1:], b[1:]):
        while x or y:
            while (x and not x[0].isdigit()) or (y and not y[0].isdigit()):
                ox, oy = order(x[:1]), order(y[:1])
                if ox != oy:
                    return (ox > oy) - (ox < oy)
                x, y = x[1:], y[1:]
            nx, ny = re.match(r"\d*", x)[0], re.match(r"\d*", y)[0]
            ix, iy = int(nx or "0"), int(ny or "0")
            if ix != iy:
                return (ix > iy) - (ix < iy)
            x, y = x[len(nx):], y[len(ny):]
    return 0


def merged_files(layout, layers):
    """Retain metadata and small text records; never extract untrusted paths."""
    files = {}
    for layer in layers:
        additions, deletions = {}, []
        with tarfile.open(blob_path(layout, layer["digest"]), "r:*") as archive:
            for member in archive:
                path = posixpath.normpath(member.name).lstrip("/")
                parent, name = posixpath.split(path)
                if name == ".wh..wh..opq":
                    deletions.append(parent + "/" if parent else "")
                elif name.startswith(".wh."):
                    deletions.append(posixpath.join(parent, name[4:]))
                else:
                    content = b""
                    if member.isfile() and member.size < 65536:
                        content = archive.extractfile(member).read()
                    additions[path] = (member, content)
        for deleted in deletions:
            files = {
                p: v for p, v in files.items()
                if deleted and p != deleted
                and not p.startswith(deleted.rstrip("/") + "/")
            }
        replaced = {path for path, (member, _) in additions.items() if not member.isdir()}
        for path in list(files):
            ancestor = posixpath.dirname(path)
            while ancestor:
                if ancestor in replaced:
                    del files[path]
                    break
                ancestor = posixpath.dirname(ancestor)
        files.update(additions)
    return files


def resolve(files, path):
    path = posixpath.normpath(path).lstrip("/")
    for _ in range(40):
        parts = path.split("/")
        for length in range(1, len(parts) + 1):
            prefix = "/".join(parts[:length])
            if prefix not in files:
                continue  # Tar archives may omit directory entries.
            member, _ = files[prefix]
            if member.issym() or member.islnk():
                parent = posixpath.dirname(prefix) if member.issym() else ""
                target = posixpath.join(parent, member.linkname, *parts[length:])
                path = posixpath.normpath(target).lstrip("/")
                break
            require(length == len(parts) or member.isdir(),
                    f"non-directory runtime path component: {prefix}")
        else:
            return files[path]
    raise ValueError(f"symlink loop at {path}")


def blob_path(layout, digest):
    algorithm, value = digest.split(":", 1)
    if algorithm != "sha256" or not re.fullmatch(r"[a-f0-9]{64}", value):
        raise ValueError(f"unsupported OCI digest: {digest}")
    return Path(layout) / "blobs" / algorithm / value


def image_manifests(layout, descriptor):
    manifest = json.loads(blob_path(layout, descriptor["digest"]).read_text())
    if "manifests" in manifest:
        for child in manifest["manifests"]:
            yield from image_manifests(layout, child)
    else:
        yield manifest


def check_runtime(layout, manifest):
    config = json.loads(blob_path(layout, manifest["config"]["digest"]).read_text())
    arch = config["architecture"]
    require(config["os"] == "linux", "Java runtime must use Linux")
    files = merged_files(layout, manifest["layers"])
    user = config["config"].get("User", "")
    if user != "1000:1000":
        passwd = resolve(files, "etc/passwd")[1].decode()
        accounts = [line.split(":") for line in passwd.splitlines()]
        require(any(account[0] == user and account[2:4] == ["1000", "1000"]
                    for account in accounts), "runtime must use UID:GID 1000:1000")
    release = resolve(files, "etc/os-release")[1].decode()
    require(re.search(r'^ID="?debian"?$', release, re.M),
        "review the package floor for this distribution"
    )
    require(re.search(r'^VERSION_ID="?13"?$', release, re.M),
        "review the package floor for this Debian release"
    )
    status = resolve(files, "var/lib/dpkg/status.d/libbrotli1")[1].decode()
    fields = dict(
        line.split(": ", 1) for line in status.splitlines()
        if ": " in line and not line.startswith((" ", "\t"))
    )
    require(fields["Package"] == "libbrotli1", "missing Brotli package record")
    require(fields["Architecture"] == arch, "Brotli package architecture mismatch")
    version = fields["Version"]
    require(version_compare(version, BROTLI_FLOOR) >= 0,
        f"libbrotli1 {version} is below {BROTLI_FLOOR}"
    )
    for path in ("usr/bin/java", "usr/bin/shelless_ulimit"):
        member, _ = resolve(files, path)
        require(member.isfile() and member.mode & 0o111, f"{path} must resolve to an executable")
    env = dict(entry.split("=", 1) for entry in config["config"]["Env"])
    java_home = env["JAVA_HOME"]
    java_release = resolve(files, java_home + "/release")[1].decode()
    require(re.search(r'^JAVA_VERSION="25(?:\.|\")', java_release, re.M),
        "runtime must support Java 25 bytecode"
    )
    truststore, _ = resolve(files, java_home + "/lib/security/cacerts")
    require(truststore.isfile() and truststore.size > 0, "Java trust store is missing or empty")
    print(f"ok: {arch} Java 25 runtime, trust store and libbrotli1 {version}")
    return arch


def main(layouts):
    architectures = []
    for layout in layouts:
        index = json.loads((Path(layout) / "index.json").read_text())
        for descriptor in index["manifests"]:
            for manifest in image_manifests(layout, descriptor):
                architectures.append(check_runtime(layout, manifest))
    require(sorted(architectures) == ["amd64", "arm64"],
        f"expected both runtime platforms, got {architectures}"
    )


if __name__ == "__main__":
    main(sys.argv[1:])
