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

load("@rules_pkg//pkg:mappings.bzl", "pkg_files", "pkg_attributes", "strip_prefix")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")
load("//rules/oci/private:common.bzl", "DEFAULT_BASE", "create_oci_image")

def _go_oci_image_impl(name, visibility, binary, base, entrypoint, cmd, binary_path, registry, extra_registries, tags):
    layer_name = name + "_layer"

    # Place the binary at `binary_path` in the layer tarball. By default
    # rules_pkg writes it at its full workspace short-path (e.g.
    # /src/clis/nvcf-cli/nvcf-cli), which would not match a default
    # entrypoint of [/<basename>] and the produced image would fail at
    # `docker run` with "exec /<name>: no such file or directory".
    # `bazel build :image` succeeds either way because no container is
    # actually executed; only the layer is assembled. strip_prefix.from_pkg("")
    # strips the binary's own package path, regardless of where this
    # macro is called from.
    if binary_path:
        # Custom absolute path inside the container, e.g. /usr/bin/app.
        # Split into directory + filename, rename the binary to the
        # filename, and place under the directory. Matches the Dockerfile
        # contract for services migrating from `COPY ... /usr/bin/app`.
        parts = binary_path.rsplit("/", 1)
        pkg_dir = parts[0] if parts[0] else "/"
        new_name = parts[1]
        files_name = name + "_files"
        pkg_files(
            name = files_name,
            srcs = [binary],
            prefix = pkg_dir,
            renames = {binary: new_name},
            attributes = pkg_attributes(mode = "0755"),
            visibility = ["//visibility:private"],
        )
        pkg_tar(
            name = layer_name,
            srcs = [":" + files_name],
            visibility = ["//visibility:private"],
        )
    else:
        pkg_tar(
            name = layer_name,
            srcs = [binary],
            package_dir = "/",
            strip_prefix = strip_prefix.from_pkg(""),
            visibility = ["//visibility:private"],
        )

    create_oci_image(
        name = name,
        tars = [layer_name],
        base = base,
        entrypoint = entrypoint,
        cmd = cmd,
        visibility = visibility,
        registry = registry,
        extra_registries = extra_registries,
        tags = tags,
    )

go_oci_image = macro(
    doc = "Packages a go_binary into a multi-arch OCI image with Linux platform transition.",
    implementation = _go_oci_image_impl,
    attrs = {
        "binary": attr.label(
            doc = "The go_binary target to package.",
            mandatory = True,
            configurable = False,
        ),
        "base": attr.label(
            doc = "Base OCI image.",
            default = DEFAULT_BASE,
            configurable = False,
        ),
        "binary_path": attr.string(
            doc = "Absolute path inside the container where the binary is placed " +
                  "(e.g. /usr/bin/app). Defaults to /{binary_name}.",
            configurable = False,
        ),
        "entrypoint": attr.string_list(
            doc = "Container entrypoint, if any.",
            configurable = False,
        ),
        "cmd": attr.string_list(
            doc = "Container cmd, if any.",
            configurable = False,
        ),
        "registry": attr.string(
            doc = "Primary registry to push to. If not set and `extra_registries` " +
                  "is empty, no push target is created.",
            configurable = False,
        ),
        "extra_registries": attr.string_dict(
            doc = "Additional registries keyed by suffix. Each entry generates " +
                  "`<name>_push_<suffix>` targeting that repository.",
            configurable = False,
        ),
        "tags": attr.string_list(
            doc = "Tags for generated targets. 'manual' is always added.",
            configurable = False,
        ),
    },
)

def _go_oci_multi_binary_image_impl(name, visibility, binaries, base, entrypoint, cmd, registry, extra_registries, tags):
    """Pack multiple go_binary targets into a single OCI image layer.

    Used for images that bundle several binaries (eg nvca-operator's image
    ships nvca-operator + nvca-mirror + nvca-operator-cleanup at distinct
    paths under /usr/bin/). Each binary is placed at the path declared as
    its dict value.
    """

    # Build one pkg_files target per binary, then merge them into a single
    # tarball. Each pkg_files renames the binary so it lands at the
    # declared path (rather than at its workspace short-path).
    files_targets = []
    first_path = None
    for i, (bin_label, bin_path) in enumerate(binaries.items()):
        parts = bin_path.rsplit("/", 1)
        pkg_dir = parts[0] if parts[0] else "/"
        new_name = parts[1]
        files_name = name + "_files_" + str(i)
        pkg_files(
            name = files_name,
            srcs = [bin_label],
            prefix = pkg_dir,
            renames = {bin_label: new_name},
            attributes = pkg_attributes(mode = "0755"),
            visibility = ["//visibility:private"],
        )
        files_targets.append(":" + files_name)
        if first_path == None:
            first_path = bin_path

    layer_name = name + "_layer"
    pkg_tar(
        name = layer_name,
        srcs = files_targets,
        visibility = ["//visibility:private"],
    )

    create_oci_image(
        name = name,
        tars = [layer_name],
        base = base,
        entrypoint = entrypoint,
        cmd = cmd,
        visibility = visibility,
        registry = registry,
        extra_registries = extra_registries,
        tags = tags,
    )

go_oci_multi_binary_image = macro(
    doc = "Packages multiple go_binary targets into one multi-arch OCI image. " +
          "Use when a single deployable image needs more than one binary " +
          "(eg an operator image that ships operator + mirror + cleanup helpers).",
    implementation = _go_oci_multi_binary_image_impl,
    attrs = {
        "binaries": attr.label_keyed_string_dict(
            doc = "Map of go_binary label -> absolute path inside the container " +
                  "where it should be placed (eg {':nvca-operator': '/usr/bin/nvca-operator'}). " +
                  "Iteration order determines the default entrypoint; pass " +
                  "`entrypoint` explicitly when relying on a specific order.",
            mandatory = True,
            configurable = False,
        ),
        "base": attr.label(
            doc = "Base OCI image.",
            default = DEFAULT_BASE,
            configurable = False,
        ),
        "entrypoint": attr.string_list(
            doc = "Container entrypoint, if any.",
            configurable = False,
        ),
        "cmd": attr.string_list(
            doc = "Container cmd, if any.",
            configurable = False,
        ),
        "registry": attr.string(
            doc = "Primary registry to push to.",
            configurable = False,
        ),
        "extra_registries": attr.string_dict(
            doc = "Additional registries keyed by suffix.",
            configurable = False,
        ),
        "tags": attr.string_list(
            doc = "Tags for generated targets. 'manual' is always added.",
            configurable = False,
        ),
    },
)
