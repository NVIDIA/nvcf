"""Workspace-relative runfiles helper for cloud-tasks integration tests."""

def _nvct_workspace_runfiles_impl(ctx):
    symlinks = {}
    strip_prefix = ctx.attr.strip_prefix

    for src in ctx.files.srcs:
        runfiles_path = src.short_path
        if strip_prefix:
            if not runfiles_path.startswith(strip_prefix):
                fail("Expected %s to start with strip_prefix %s" % (runfiles_path, strip_prefix))
            runfiles_path = runfiles_path[len(strip_prefix):]

        if runfiles_path in symlinks:
            fail("Duplicate runfiles path: %s" % runfiles_path)
        symlinks[runfiles_path] = src

    return [DefaultInfo(runfiles = ctx.runfiles(symlinks = symlinks))]

nvct_workspace_runfiles = rule(
    implementation = _nvct_workspace_runfiles_impl,
    attrs = {
        "srcs": attr.label_list(allow_files = True),
        "strip_prefix": attr.string(),
    },
)
