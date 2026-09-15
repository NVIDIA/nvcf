"""Root-only Zig repository adapter for the NVCF monorepo."""

load("@hermetic_cc_toolchain//toolchain:defs.bzl", zig_toolchains = "toolchains")

def _root_zig_impl(module_ctx):
    repos = zig_toolchains()
    return module_ctx.extension_metadata(
        reproducible = True,
        root_module_direct_dev_deps = list(repos.public),
        root_module_direct_deps = [],
    )

root_zig = module_extension(implementation = _root_zig_impl)
