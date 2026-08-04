# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

"OCI image rules for Go binaries."

load("@rules_pkg//pkg:mappings.bzl", "strip_prefix")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")
load("//rules/oci/private:common.bzl", "DEFAULT_BASE", "create_oci_image")

# Re-exports an artifact built in the exec config, pinning the web-asset layer
# to the build host so the image's linux platform transition does not force a
# cross-rebuild of the native Vite/Rolldown toolchain (which cannot cross-run).
def _exec_files_impl(ctx):
    return DefaultInfo(files = depset(ctx.files.src))

_exec_files = rule(
    implementation = _exec_files_impl,
    attrs = {"src": attr.label(cfg = "exec", allow_files = True)},
)

def _go_web_oci_image_impl(name, visibility, binaries, web_assets, web_dir, package_dir, base, entrypoint, registry, tags, repo_tag, source_layer):
    tars = []

    # One layer per binary at {package_dir}/{basename}, e.g. /app/server.
    for binary in binaries:
        blabel = native.package_relative_label(binary)
        layer_name = name + "_" + blabel.name + "_layer"
        pkg_tar(
            name = layer_name,
            srcs = [binary],
            mode = "0755",
            package_dir = package_dir,
            strip_prefix = strip_prefix.from_pkg(""),
            visibility = ["//visibility:private"],
        )
        tars.append(layer_name)

    exec_web = name + "_web_exec"
    _exec_files(
        name = exec_web,
        src = web_assets,
        visibility = ["//visibility:private"],
    )

    # strip_prefix drops the tree artifact's short-path (e.g. "ui/ui") so its
    # contents land directly under web_dir, not web_dir/<out_dir>/.
    web_layer = name + "_web_layer"
    pkg_tar(
        name = web_layer,
        srcs = [exec_web],
        package_dir = web_dir,
        strip_prefix = strip_prefix.from_root(
            web_assets.package + "/" + web_assets.name,
        ),
        visibility = ["//visibility:private"],
    )
    tars.append(web_layer)

    # Optional pre-built source bundle layer (e.g. //:source_layer).
    if source_layer:
        tars.append(source_layer)

    entry = entrypoint
    if not entry:
        entry = [package_dir.rstrip("/") + "/" + native.package_relative_label(binaries[0]).name]

    create_oci_image(
        name = name,
        tars = tars,
        base = base,
        entrypoint = entry,
        workdir = package_dir,
        visibility = visibility,
        registry = registry,
        tags = tags,
        repo_tag = repo_tag,
    )

go_web_oci_image = macro(
    doc = "Packages one or more go_binary targets plus a static web-asset " +
          "directory into a single multi-arch OCI image. Used for the nvcf-ui " +
          "server, which serves the built SPA from disk alongside the " +
          "control-plane monitor binary.",
    implementation = _go_web_oci_image_impl,
    attrs = {
        "binaries": attr.label_list(
            doc = "go_binary targets to package under package_dir.",
            mandatory = True,
            configurable = False,
        ),
        "web_assets": attr.label(
            doc = "Directory/filegroup of static web assets (Vite dist/).",
            mandatory = True,
            configurable = False,
        ),
        "web_dir": attr.string(
            doc = "In-image mount path for web_assets.",
            default = "/app/static",
            configurable = False,
        ),
        "package_dir": attr.string(
            doc = "In-image directory for the binaries.",
            default = "/app",
            configurable = False,
        ),
        "base": attr.label(
            doc = "Base OCI image.",
            default = DEFAULT_BASE,
            configurable = False,
        ),
        "entrypoint": attr.string_list(
            doc = "Container entrypoint. Defaults to {package_dir}/{first binary}.",
            configurable = False,
        ),
        "registry": attr.string(
            doc = "Registry to push to. If not set, push target is not created.",
            configurable = False,
        ),
        "tags": attr.string_list(
            doc = "Tags for generated targets. 'manual' is always added.",
            configurable = False,
        ),
        "repo_tag": attr.string(
            doc = "docker repo:tag for the _load target. Required at the repo " +
                  "root, where the package name is empty.",
            default = "nvcf-ui:latest",
            configurable = False,
        ),
        "source_layer": attr.label(
            doc = "Optional pre-built pkg_tar target whose contents are added as " +
                  "a layer in the image. Intended to bundle the repository source " +
                  "at a well-known path (e.g. /usr/share/nvcf-ui/src) for " +
                  "open-source compliance and auditability.",
            configurable = False,
        ),
    },
)
