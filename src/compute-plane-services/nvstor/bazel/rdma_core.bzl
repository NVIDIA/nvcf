# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""rdma-core for nvstor, from pinned Ubuntu noble packages.

One repository per architecture, `nvstor_rdma_core_amd64` and
`nvstor_rdma_core_arm64`, each with two targets:

  :dev_prefix     a directory laid out as an rdma-core `usr` tree (headers and
                  libibverbs.so), the RDMA_CORE_PREFIX nvstor-rdma's build
                  script links against
  :runtime_libdir the runtime shared libraries in one directory, for the
                  LD_LIBRARY_PATH of tests run on a host without rdma-core
  :runtime_layer  an image layer (tar.gz) with libibverbs, the verbs providers
                  (libmlx5 among them) and libnl, at their Ubuntu paths, plus
                  /var/lib/dpkg/status.d entries so image scanners list them

Every package is checked against its SHA-256 before it is unpacked. The
archive URL is tried first; the snapshot URL keeps a pin working after a
newer update supersedes it in the archive.
"""

_SNAPSHOT = "https://snapshot.ubuntu.com/ubuntu/20261101T000000Z/pool/main/"

_ARCHIVES = {
    "amd64": "https://archive.ubuntu.com/ubuntu/pool/main/",
    "arm64": "https://ports.ubuntu.com/ubuntu-ports/pool/main/",
}

# Debian multiarch directory, as under usr/lib/.
_MULTIARCH = {
    "amd64": "x86_64-linux-gnu",
    "arm64": "aarch64-linux-gnu",
}

_RDMA_CORE = "50.0-2ubuntu0.2"
_LIBNL = "3.7.0-0.3build1.1"

# name -> (pool directory, version, {arch: sha256}, trees it is unpacked into)
_PACKAGES = {
    "ibverbs-providers": ("r/rdma-core/", _RDMA_CORE, {
        "amd64": "69847c5522d32c14aa3de318da8edea1a1cac6a7d936d81082bf292975f83bb6",
        "arm64": "39e91a145faaa332aad6837783d6b600b7e1b136fa805fb7fc824a87d21897ab",
    }, ["runtime"]),
    "libibverbs-dev": ("r/rdma-core/", _RDMA_CORE, {
        "amd64": "fb409dc756c1697a83683e43a3ca78cb9eabbc24605391808252b8d8bedf54fc",
        "arm64": "1880ccb4116c112ccc4e6893f1d53892c0d5c1b307384ad4158c470dbed126e3",
    }, ["dev"]),
    "libibverbs1": ("r/rdma-core/", _RDMA_CORE, {
        "amd64": "8489558105ee1d1c65ae6e0f1bac0e9b15f314a68f106ee9a63c89821242bd8e",
        "arm64": "20b72b2c6b882ee4c155658be8a0a01d6edc2ee580eaeb330d9bb3ad643f386d",
    }, ["dev", "runtime"]),
    "libnl-3-200": ("libn/libnl3/", _LIBNL, {
        "amd64": "15b3f61db753dc3db6f62f4259f8b822299343e500886776486612a13a2ed247",
        "arm64": "be2e96607e4c6e745b9ef1ec11d8192f5603721a7a5a9989524ea3d7a7f6db16",
    }, ["runtime"]),
    "libnl-route-3-200": ("libn/libnl3/", _LIBNL, {
        "amd64": "c9dbd29cb7fdcf1a0587726cbc5432a7d3e81242108dfcfa2dd6716c44c74d4d",
        "arm64": "cce89a1737de041f03f47f71a7b177c3c22b4bc8085a39d84158ce56a8d07032",
    }, ["runtime"]),
}

def _member(rctx, directory, prefix):
    """The one member of an unpacked .deb whose name starts with prefix."""
    found = [e for e in rctx.path(directory).readdir() if e.basename.startswith(prefix)]
    if len(found) != 1:
        fail("%s: expected one %s* member, found %d" % (directory, prefix, len(found)))
    return found[0]

def _relative(link_dir, target):
    """target (a path from the tree root) relative to link_dir (also from the root)."""
    a = link_dir.split("/") if link_dir else []
    b = target.split("/")
    common = 0
    for i in range(min(len(a), len(b) - 1)):
        if a[i] != b[i]:
            break
        common = i + 1
    return "/".join([".."] * (len(a) - common) + b[common:])

def _walk(rctx, root):
    """Regular files and symlinks under root, as paths relative to root.

    Returns (files, links), links mapping a link's path to its target relative
    to the link's directory. A symlink to a directory is refused: none of these
    packages ships one, and pkg_files could not lay it down as a link.
    """
    root_path = rctx.path(root)
    root_real = str(root_path.realpath)
    files = []
    links = {}
    stack = [root_path]
    for _ in range(100000):
        if not stack:
            break
        d = stack.pop()
        for e in d.readdir():
            rel = str(e)[len(str(root_path)) + 1:]
            real = str(e.realpath)
            if real != root_real + "/" + rel:
                if e.is_dir:
                    fail("%s: symlink to a directory" % rel)
                if not real.startswith(root_real + "/"):
                    fail("%s: symlink leaves the tree (%s)" % (rel, real))
                links[rel] = _relative(rel.rpartition("/")[0], real[len(root_real) + 1:])
            elif e.is_dir:
                stack.append(e)
            else:
                files.append(rel)
    if stack:
        fail("%s: tree too deep" % root)
    return sorted(files), links

def _rdma_core_repo_impl(rctx):
    arch = rctx.attr.arch
    for name, (pool, version, sha256, trees) in _PACKAGES.items():
        deb = "%s_%s_%s.deb" % (name, version, arch)
        rctx.download(
            url = [_ARCHIVES[arch] + pool + deb, _SNAPSHOT + pool + deb],
            output = "debs/" + deb,
            sha256 = sha256[arch],
        )
        rctx.extract("debs/" + deb, output = "ar/" + name, type = "ar")
        control = _member(rctx, "ar/" + name, "control.tar")
        data = _member(rctx, "ar/" + name, "data.tar")
        for tree in trees:
            rctx.extract(data, output = tree)
        if "runtime" in trees:
            rctx.extract(control, output = "control/" + name)
            status = "runtime/var/lib/dpkg/status.d/"
            rctx.file(status + name, rctx.read("control/%s/control" % name).rstrip("\n") + "\n", executable = False)
            rctx.file(status + name + ".md5sums", rctx.read("control/%s/md5sums" % name), executable = False)
        rctx.delete("ar/" + name)
    rctx.delete("debs")
    rctx.delete("control")

    files, links = _walk(rctx, "runtime")
    multiarch = _MULTIARCH[arch]
    rctx.file("BUILD.bazel", """\
load("@aspect_bazel_lib//lib:copy_to_directory.bzl", "copy_to_directory")
load("@rules_pkg//pkg:mappings.bzl", "pkg_files", "pkg_mklink", "strip_prefix")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")

package(default_visibility = ["//visibility:public"])

copy_to_directory(
    name = "dev_prefix",
    srcs = glob([
        "dev/usr/include/**",
        "dev/usr/lib/{multiarch}/libibverbs.so*",
    ]),
    root_paths = ["dev/usr"],
)

# The runtime shared libraries alone, for LD_LIBRARY_PATH of tests that run
# a binary linked against libibverbs on a host without rdma-core.
copy_to_directory(
    name = "runtime_libdir",
    srcs = glob(["runtime/usr/lib/{multiarch}/*.so*"]),
    root_paths = ["runtime/usr/lib/{multiarch}"],
)

pkg_files(
    name = "runtime_files",
    srcs = {files},
    strip_prefix = strip_prefix.from_pkg("runtime"),
)

[
    pkg_mklink(
        name = "link_%d" % i,
        link_name = link,
        target = target,
    )
    for i, (link, target) in enumerate({links})
]

pkg_tar(
    name = "runtime_layer",
    srcs = [":runtime_files"] + [":link_%d" % i for i in range({nlinks})],
    extension = "tar.gz",
    package_dir = "/",
)
""".format(
        multiarch = multiarch,
        files = repr(["runtime/" + f for f in files]),
        links = repr(sorted(links.items())),
        nlinks = len(links),
    ))

_rdma_core_repo = repository_rule(
    implementation = _rdma_core_repo_impl,
    attrs = {"arch": attr.string(mandatory = True, values = _ARCHIVES.keys())},
    doc = "rdma-core's dev tree and runtime image layer for one architecture.",
)

def _rdma_core_impl(mctx):
    for arch in _ARCHIVES:
        _rdma_core_repo(name = "nvstor_rdma_core_" + arch, arch = arch)
    return mctx.extension_metadata(reproducible = True)

rdma_core = module_extension(
    implementation = _rdma_core_impl,
    doc = "The nvstor_rdma_core_{amd64,arm64} repositories.",
)
