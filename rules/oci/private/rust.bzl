# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"OCI image rules for Rust binaries."

load("@rules_pkg//pkg:tar.bzl", "pkg_tar")
load("//rules/oci/private:common.bzl", "DEFAULT_BASE", "create_oci_image")

def _rust_oci_image_impl(name, visibility, binary, binary_path, base, entrypoint, env, registry, extra_registries, tags, extra_layers):
    # Lay the binary down at a stable absolute path so the entrypoint
    # and pod-spec command can reference it directly. Defaults to
    # /usr/local/bin/{binary_target_name}, matching the legacy Dockerfile.
    if binary_path:
        bin_path = binary_path
    else:
        bin_path = "/usr/local/bin/" + native.package_relative_label(binary).name

    layer_name = name + "_layer"
    pkg_tar(
        extension = "tar.gz",  # gzip the layer (Docker parity; rules_oci ships pkg_tar as-is)
        name = layer_name,
        srcs = [binary],
        package_dir = "/",
        remap_paths = {"/" + native.package_relative_label(binary).name: bin_path},
        # Exported (the name is prefixed with the macro name) so the
        # calling package can wire an image_entrypoint_mode_test sh_test
        # against {name}_layer to guard the entrypoint exec bit.
        visibility = visibility,
    )

    entry = entrypoint
    if not entry:
        entry = [bin_path]

    create_oci_image(
        name = name,
        tars = [layer_name] + list(extra_layers),
        base = base,
        entrypoint = entry,
        env = env,
        visibility = visibility,
        registry = registry,
        extra_registries = extra_registries,
        tags = tags,
    )

rust_oci_image = macro(
    doc = "Packages a rust_binary into a multi-arch OCI image with Linux platform transition.",
    implementation = _rust_oci_image_impl,
    attrs = {
        "binary": attr.label(
            doc = "The rust_binary target to package.",
            mandatory = True,
            configurable = False,
        ),
        "binary_path": attr.string(
            doc = "Where to lay the binary in the image. Defaults to /usr/local/bin/{binary_name}.",
            configurable = False,
        ),
        "extra_layers": attr.label_list(
            doc = "Additional pkg_tar layers to stack on top of the binary layer.",
            configurable = False,
        ),
        "base": attr.label(
            doc = "Base OCI image.",
            default = DEFAULT_BASE,
            configurable = False,
        ),
        "entrypoint": attr.string_list(
            doc = "Container entrypoint. Defaults to [binary_path].",
            configurable = False,
        ),
        "env": attr.string_dict(
            doc = "Environment variables baked into the image config (key -> value).",
            configurable = False,
        ),
        "registry": attr.string(
            doc = "Primary registry to push to. If unset, the primary _push target is not created.",
            configurable = False,
        ),
        "extra_registries": attr.string_dict(
            doc = "Additional registries: suffix -> repository path. Generates {name}_push_{suffix} for each.",
            configurable = False,
        ),
        "tags": attr.string_list(
            doc = "Tags for generated targets. 'manual' is always added.",
            configurable = False,
        ),
    },
)
