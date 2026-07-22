# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"Multi-arch transition rule for OCI images."

def _multiarch_transition(settings, attr):
    return [
        {
            "//command_line_option:platforms": str(platform),
            # Pin opt: fastbuild's zig arm64 UBSan traps SIGTRAP jemalloc at startup (nvbug 6451362).
            "//command_line_option:compilation_mode": "opt",
        }
        for platform in attr.platforms
    ]

multiarch_transition = transition(
    implementation = _multiarch_transition,
    inputs = [],
    outputs = ["//command_line_option:platforms", "//command_line_option:compilation_mode"],
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
