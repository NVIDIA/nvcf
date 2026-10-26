# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"Executable layers carrying explicit Linux file capabilities."

def _capability_layer_impl(ctx):
    output = ctx.actions.declare_file(ctx.label.name + ".tar.gz")
    ctx.actions.run(
        executable = ctx.executable._builder,
        inputs = [ctx.file.binary],
        outputs = [output],
        arguments = [
            "--binary", ctx.file.binary.path,
            "--path", ctx.attr.binary_path,
            "--capabilities", ",".join(ctx.attr.capabilities),
            "--output", output.path,
        ],
        mnemonic = "OCICapabilityLayer",
    )
    return [DefaultInfo(files = depset([output]))]

capability_layer = rule(
    implementation = _capability_layer_impl,
    attrs = {
        "binary": attr.label(allow_single_file = True, mandatory = True),
        "binary_path": attr.string(mandatory = True),
        "capabilities": attr.string_list(mandatory = True),
        "_builder": attr.label(
            default = "//tools/oci-capability-layer",
            executable = True,
            cfg = "exec",
        ),
    },
)
