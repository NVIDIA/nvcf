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

"""opt_binary: a binary built optimised (-c opt), whatever the command line says.

nvcf's .bazelrc builds with --compilation_mode=fastbuild, which compiles Rust
without optimisation, and the image push workflows do not pass
--config=release. An image built from an opt_binary ships the optimised
binary either way.
"""

def _opt_transition_impl(_settings, _attr):
    # Only the compilation mode: a build already at -c opt (--config=release)
    # then lands in the same configuration and is not built twice. rules_rust
    # takes the strip level from its toolchain per compilation mode (opt:
    # debuginfo), not from --strip.
    return {"//command_line_option:compilation_mode": "opt"}

_opt_transition = transition(
    implementation = _opt_transition_impl,
    inputs = [],
    outputs = ["//command_line_option:compilation_mode"],
)

def _opt_binary_impl(ctx):
    # A transition on an attribute makes it a list, with one entry here.
    src = ctx.attr.binary[0][DefaultInfo].files_to_run.executable
    out = ctx.actions.declare_file(ctx.label.name)
    ctx.actions.symlink(output = out, target_file = src, is_executable = True)
    return [DefaultInfo(files = depset([out]), executable = out)]

opt_binary = rule(
    implementation = _opt_binary_impl,
    attrs = {
        "binary": attr.label(
            doc = "The binary to build with -c opt.",
            mandatory = True,
            executable = True,
            cfg = _opt_transition,
        ),
        "_allowlist_function_transition": attr.label(
            default = "@bazel_tools//tools/allowlists/function_transition_allowlist",
        ),
    },
    executable = True,
    doc = "The binary, built with -c opt. Output named after this target.",
)
