# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"Shared helper for building multi-arch OCI images from a base plus tar layers."

load("@aspect_bazel_lib//lib:transitions.bzl", "platform_transition_filegroup")
load("@rules_oci//oci:defs.bzl", "oci_image", "oci_image_index", "oci_load")
load("//rules/oci:transition.bzl", "multi_arch")

# Official Apache Cassandra base, pulled by digest in MODULE.bazel.
DEFAULT_BASE = "@cassandra_base"

# Multi-arch: amd64 + arm64. The base manifest-list carries both.
DEFAULT_PLATFORMS = [
    "//platforms:linux_x86_64",
    "//platforms:linux_arm64",
]

def create_oci_image(
        name,
        base,
        tars,
        visibility,
        entrypoint = None,
        cmd = None,
        env = None,
        exposed_ports = None,
        tags = None):
    """Create multi-arch OCI image targets from a base image and tar layers.

    Generates:
      - {name}: platform-transitioned single-arch image (host cpu)
      - {name}_index: multi-arch image index (amd64 + arm64)
      - {name}_load: local `docker load` target
      - {name}.tar: tarball filegroup

    `entrypoint` / `cmd` default to None so the base image's own
    ENTRYPOINT/CMD (the official docker-entrypoint.sh + `cassandra -f`) are
    inherited unchanged. This helper does not create push targets; the
    nvidia-internal/ call-site pushes via the shared oci-destinations macro.
    """
    all_tags = ["manual"] + (tags or [])

    pre_transitioned = name + "_pre_transitioned"
    oci_image(
        name = pre_transitioned,
        base = base,
        tars = tars,
        entrypoint = entrypoint,
        cmd = cmd,
        env = env,
        exposed_ports = exposed_ports,
        visibility = ["//visibility:private"],
        tags = all_tags,
    )

    platform_transition_filegroup(
        name = name,
        srcs = [pre_transitioned],
        target_platform = select({
            "@platforms//cpu:arm64": "//platforms:linux_arm64",
            "@platforms//cpu:x86_64": "//platforms:linux_x86_64",
        }),
        visibility = visibility,
        tags = all_tags,
    )

    multi_arch_name = name + "_multi_arch"
    multi_arch(
        name = multi_arch_name,
        image = pre_transitioned,
        platforms = DEFAULT_PLATFORMS,
        visibility = ["//visibility:private"],
        tags = all_tags,
    )

    oci_image_index(
        name = name + "_index",
        images = [multi_arch_name],
        visibility = visibility,
        tags = all_tags,
    )

    # native.package_name() is "" for the root package; fall back to the
    # target name so the local repo tag stays valid (e.g. image:latest).
    repo = native.package_name() if native.package_name() else name
    oci_load(
        name = name + "_load",
        image = name,
        repo_tags = [repo + ":latest"],
        visibility = visibility,
        tags = all_tags,
    )

    native.filegroup(
        name = name + ".tar",
        srcs = [name + "_load"],
        output_group = "tarball",
        visibility = visibility,
        tags = all_tags,
    )
