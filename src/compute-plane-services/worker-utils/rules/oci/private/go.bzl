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

def _go_oci_image_impl(name, visibility, binary, base, entrypoint, registry, tags):
    layer_name = name + "_layer"

    # Place the binary at /<basename> in the layer tarball. Without
    # strip_prefix + package_dir, rules_pkg writes it at its full
    # workspace short-path (e.g. /src/clis/nvcf-cli/nvcf-cli), which would
    # not match the entrypoint computed below ([/<basename>]) and the
    # produced image would fail at `docker run` with "exec /<name>: no
    # such file or directory". `bazel build :image` succeeds either way
    # because no container is actually executed; only the layer is
    # assembled. strip_prefix.from_pkg("") strips the binary's own
    # package path, regardless of where this macro is called from.
    pkg_tar(
        name = layer_name,
        srcs = [binary],
        package_dir = "/",
        strip_prefix = strip_prefix.from_pkg(""),
        visibility = ["//visibility:private"],
    )

    entry = entrypoint
    if not entry:
        entry = ["/" + native.package_relative_label(binary).name]

    create_oci_image(
        name = name,
        tars = [layer_name],
        base = base,
        entrypoint = entry,
        visibility = visibility,
        registry = registry,
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
        "entrypoint": attr.string_list(
            doc = "Container entrypoint. Defaults to /{binary_name}.",
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
    },
)
