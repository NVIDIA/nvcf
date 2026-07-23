# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"Shared helpers for OCI image rules."

load("@aspect_bazel_lib//lib:expand_template.bzl", "expand_template")
load("@aspect_bazel_lib//lib:transitions.bzl", "platform_transition_filegroup")
load("@rules_oci//oci:defs.bzl", "oci_image", "oci_image_index", "oci_load", "oci_push")
load("//rules/oci:transition.bzl", "multi_arch")

DEFAULT_BASE = "@distroless_cc"

# Multi-arch: amd64 + arm64. stargate has no openssl-sys / system-libssl
# pin (it uses rustls + aws-lc / ring), so the aarch64 zig cross-compile
# does not hit the fixed-path libssl problem that forced function-autoscaler
# to single-arch.
DEFAULT_PLATFORMS = [
    "//platforms:linux_x86_64",
    "//platforms:linux_arm64",
]

COMMON_LAYERS = []

def create_oci_image(
        name,
        tars,
        base,
        entrypoint,
        visibility,
        env = None,
        registry = None,
        extra_registries = None,
        tags = None):
    """Creates OCI image targets with platform transitions and tarball output.

    Generates:
      - {name}: Platform-transitioned OCI image (for local builds)
      - {name}_index: Multi-arch image index (amd64 + arm64)
      - {name}_load: Local docker load target
      - {name}.tar: Tarball filegroup
      - {name}_push: Push to `registry` (if set)
      - {name}_push_{suffix}: Push to each entry in `extra_registries` (dict suffix -> repo)

    `extra_registries` lets us publish the same multi-arch image to several
    nvcr.io repos (kaze + nvcf-dev + ncp-dev) under different docker auth
    contexts, with the same set of stamped tags.
    """
    all_tags = ["manual"] + (tags or [])

    pre_transitioned = name + "_pre_transitioned"
    oci_image(
        name = pre_transitioned,
        base = base,
        tars = tars + COMMON_LAYERS,
        entrypoint = entrypoint,
        env = env,
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

    load_name = name + "_load"
    oci_load(
        name = load_name,
        image = name,
        repo_tags = [native.package_name() + ":latest"],
        visibility = visibility,
        tags = all_tags,
    )

    native.filegroup(
        name = name + ".tar",
        srcs = [load_name],
        output_group = "tarball",
        visibility = visibility,
        tags = all_tags,
    )

    # Single stamped-tags template, reused by every push target.
    have_push = registry or extra_registries
    if have_push:
        stamped_tags = name + "_stamped_tags"
        expand_template(
            name = stamped_tags,
            out = name + "_tags.txt",
            stamp_substitutions = {
                "{VERSION}": "{{STABLE_VERSION}}",
                "{OCI_TAG}": "{{STABLE_OCI_TAG}}",
                "{COMMIT}": "{{STABLE_GIT_COMMIT}}",
            },
            template = [
                "latest",
                "{VERSION}",
                "{OCI_TAG}",
                "{COMMIT}",
            ],
            visibility = ["//visibility:private"],
        )

    if registry:
        oci_push(
            name = name + "_push",
            image = name + "_index",
            remote_tags = stamped_tags,
            repository = registry,
            visibility = visibility,
            tags = all_tags,
        )

    for suffix, repo in (extra_registries or {}).items():
        oci_push(
            name = name + "_push_" + suffix,
            image = name + "_index",
            remote_tags = stamped_tags,
            repository = repo,
            visibility = visibility,
            tags = all_tags,
        )
