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

load("@rules_pkg//pkg:mappings.bzl", "pkg_attributes", "pkg_files", "strip_prefix")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")
load("//rules/oci/private:common.bzl", "DEFAULT_BASE", "create_oci_image")

def _go_oci_image_impl(name, visibility, binary, base, entrypoint, binary_path, registry, extra_registries, tags):
    layer_name = name + "_layer"

    # Where the binary lands inside the layer tarball. Left to rules_pkg, it
    # goes to its full workspace short-path (e.g. /src/clis/nvcf-cli/nvcf-cli),
    # which does not match the entrypoint either branch computes, and the image
    # then fails at `docker run` with "exec /<name>: no such file or directory".
    # `bazel build :image` passes either way because no container is executed;
    # only the layer is assembled. So this is not something a build catches.
    if binary_path:
        # Explicit absolute path, e.g. /usr/bin/app: split into directory and
        # filename, rename the binary, and place it under that directory. This
        # reproduces the `COPY ... /usr/bin/app` contract that services carried
        # over from their Dockerfiles, so image consumers that hard-code the
        # path keep working across the migration.
        parts = binary_path.rsplit("/", 1)
        pkg_dir = parts[0] if parts[0] else "/"
        new_name = parts[1]
        files_name = name + "_files"
        pkg_files(
            name = files_name,
            srcs = [binary],
            prefix = pkg_dir,
            renames = {binary: new_name},
            # rules_pkg defaults non-source srcs to 0644, which leaves the
            # binary non-executable in the layer and the container fails to
            # start with "permission denied". This is the rules_pkg
            # default-0644 startup failure; 0755 is what makes it launchable as
            # PID 1. Callers pair this with an image_entrypoint_mode_test.
            attributes = pkg_attributes(mode = "0755"),
            visibility = ["//visibility:private"],
        )
        pkg_tar(
            extension = "tar.gz",  # gzip the layer (Docker parity; rules_oci ships pkg_tar as-is)
            name = layer_name,
            srcs = [":" + files_name],
            visibility = ["//visibility:private"],
        )
        default_entry = [binary_path]
    else:
        # strip_prefix.from_pkg("") strips the binary's own package path
        # regardless of where this macro is called from, putting it at
        # /<basename>.
        #
        # mode = "0755" is defensive here: a Go binary out of rules_go is
        # already 0755 and pkg_tar preserves that. It matters if a caller ever
        # passes a non-Go file (an embedded data file, a genrule wrapper script
        # without explicit chmod), which pkg_tar would otherwise write as 0644.
        pkg_tar(
            extension = "tar.gz",  # gzip the layer (Docker parity; rules_oci ships pkg_tar as-is)
            name = layer_name,
            srcs = [binary],
            mode = "0755",
            package_dir = "/",
            strip_prefix = strip_prefix.from_pkg(""),
            visibility = ["//visibility:private"],
        )
        default_entry = ["/" + native.package_relative_label(binary).name]

    entry = entrypoint if entrypoint else default_entry

    create_oci_image(
        name = name,
        tars = [layer_name],
        base = base,
        entrypoint = entry,
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
        "entrypoint": attr.string_list(
            doc = "Container entrypoint. Defaults to binary_path, or /{binary_name}.",
            configurable = False,
        ),
        "binary_path": attr.string(
            doc = "Absolute path for the binary inside the image, e.g. " +
                  "/usr/bin/app. Defaults to /{binary_name}.",
            configurable = False,
        ),
        "registry": attr.string(
            doc = "Registry to push to. If not set, push target is not created.",
            configurable = False,
        ),
        "extra_registries": attr.string_dict(
            doc = "Additional registries to push to, keyed by target-name " +
                  "suffix: each entry creates {name}_push_{suffix}.",
            configurable = False,
        ),
        "tags": attr.string_list(
            doc = "Tags for generated targets. 'manual' is always added.",
            configurable = False,
        ),
    },
)
