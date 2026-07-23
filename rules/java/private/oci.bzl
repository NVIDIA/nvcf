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

"""Self-contained OCI image helper for the Java rules module.

Vendored from //rules/oci so nvcf_java_rules is a standalone Bazel module with
no dependency on the root workspace's //rules/oci package. Kept behavior-for-
behavior identical to the shared helper: multi-arch (amd64 + arm64) images with
platform transitions and a registry push target. If the shared helper changes,
re-vendor this copy.
"""

load("@aspect_bazel_lib//lib:expand_template.bzl", "expand_template")
load("@aspect_bazel_lib//lib:transitions.bzl", "platform_transition_filegroup")
load("@rules_oci//oci:defs.bzl", "oci_image", "oci_image_index", "oci_load", "oci_push")

DEFAULT_BASE = "@ubuntu_noble"
DEFAULT_PLATFORMS = [
    "//platforms:linux_arm64",
    "//platforms:linux_x86_64",
]

COMMON_LAYERS = []

def _multiarch_transition(settings, attr):
    return [
        {"//command_line_option:platforms": str(platform)}
        for platform in attr.platforms
    ]

multiarch_transition = transition(
    implementation = _multiarch_transition,
    inputs = [],
    outputs = ["//command_line_option:platforms"],
)

def _multi_arch_impl(ctx):
    return DefaultInfo(files = depset(ctx.files.image))

multi_arch = rule(
    implementation = _multi_arch_impl,
    attrs = {
        "image": attr.label(cfg = multiarch_transition),
        "platforms": attr.label_list(),
        "_allowlist_function_transition": attr.label(
            default = "@bazel_tools//tools/allowlists/function_transition_allowlist",
        ),
    },
)

def create_oci_image(
        name,
        tars,
        base,
        entrypoint,
        visibility,
        registry = None,
        tags = None):
    """Creates OCI image targets with platform transitions and tarball output.

    Generates:
      - {name}: Platform-transitioned OCI image (for local builds)
      - {name}_index: Multi-arch image index (amd64 + arm64)
      - {name}_load: Local docker load target
      - {name}.tar: Tarball filegroup
      - {name}_push: Push to registry (if registry is set)
    """
    all_tags = ["manual"] + (tags or [])

    pre_transitioned = name + "_pre_transitioned"
    oci_image(
        name = pre_transitioned,
        base = base,
        tars = tars + COMMON_LAYERS,
        entrypoint = entrypoint,
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

    if registry:
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

        oci_push(
            name = name + "_push",
            image = name + "_index",
            remote_tags = stamped_tags,
            repository = registry,
            visibility = visibility,
            tags = all_tags,
        )
