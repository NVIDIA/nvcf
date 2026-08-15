"""Shared Java build macros for the NVCF monorepo.

Control-plane components use the neutral `nvcf_*` API. The `nv_boot_*`
wrappers are local profiles for sources owned by nv-boot-parent. Third-party
artifacts resolve from the root `@nv_third_party_deps` hub; executable helpers
and shared build targets live under `//tools/bazel/java`.
"""

load(
    "@rules_java//java:defs.bzl",
    _java_library = "java_library",
    _java_test = "java_test",
)
load("@rules_java//java/common:java_info.bzl", "JavaInfo")
load("@rules_shell//shell:sh_test.bzl", _sh_test = "sh_test")

# ============================================================================
# Shared helper targets (root-owned).
# ============================================================================
_LOMBOK_COMPILE_DEPS = ["//tools/bazel/java:lombok_annotations"]
_LOMBOK_PLUGINS = ["//tools/bazel/java:lombok_plugin"]
_JACOCO_TEST_RUNNER = "//tools/bazel/java:jacoco_test_runner.sh"
_JACOCO_CLI = "//tools/bazel/java:jacoco_cli"

_JUNIT5_ARGS = [
    "execute",
    "--details=flat",
    "--disable-ansi-colors",
    "--details-theme=ascii",
    "--fail-if-no-tests",
]

_JUNIT5_RUNTIME_DEPS = [
    "@nv_third_party_deps//:org_junit_platform_junit_platform_console_standalone",
]

_JUNIT5_COMPILE_DEPS = [
    "@nv_third_party_deps//:org_assertj_assertj_core",
    "@nv_third_party_deps//:org_junit_jupiter_junit_jupiter_api",
    "@nv_third_party_deps//:org_junit_jupiter_junit_jupiter_params",
    "@nv_third_party_deps//:org_mockito_mockito_core",
    "@nv_third_party_deps//:org_mockito_mockito_junit_jupiter",
    "@nv_third_party_deps//:org_springframework_boot_spring_boot_test",
    "@nv_third_party_deps//:org_springframework_boot_spring_boot_test_autoconfigure",
    "@nv_third_party_deps//:org_springframework_spring_test",
]

_MOCKITO_CORE = "@nv_third_party_deps//:org_mockito_mockito_core"
_JACOCO_AGENT = "@nv_third_party_deps//:org_jacoco_org_jacoco_agent_runtime"

_JACOCO_AGENT_JVM_FLAGS = [
    (
        "-javaagent:$(location %s)=destfile=jacoco.exec,append=false,"
        + "dumponexit=true,includes=com.nvidia.*"
    ) % _JACOCO_AGENT,
]

def _unique(values):
    seen = {}
    result = []
    for value in values:
        if value not in seen:
            seen[value] = True
            result.append(value)
    return result

def _conventional_resource_root(path):
    return native.glob(
        [path],
        allow_empty = True,
        exclude_directories = 0,
    )

# ============================================================================
# Shared rules.
# ============================================================================
def _nv_boot_runtime_classpath_test_impl(ctx):
    runtime_jars = ctx.attr.target[JavaInfo].transitive_runtime_jars.to_list()
    leaked = []

    for jar in runtime_jars:
        for artifact in ctx.attr.forbidden_artifacts:
            if artifact in jar.basename:
                leaked.append(jar.short_path)
                break

    if leaked:
        fail(
            "%s exports Maven-optional/provided runtime jars:\n%s" % (
                ctx.attr.target.label,
                "\n".join(sorted(leaked)),
            ),
        )

    executable = ctx.actions.declare_file(ctx.label.name + ".sh")
    ctx.actions.write(
        output = executable,
        content = "#!/bin/sh\nexit 0\n",
        is_executable = True,
    )
    return [DefaultInfo(executable = executable)]

nv_boot_runtime_classpath_test = rule(
    implementation = _nv_boot_runtime_classpath_test_impl,
    attrs = {
        "forbidden_artifacts": attr.string_list(mandatory = True),
        "target": attr.label(mandatory = True, providers = [JavaInfo]),
    },
    test = True,
)

def _workspace_runfiles_impl(ctx):
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

_workspace_runfiles = rule(
    implementation = _workspace_runfiles_impl,
    attrs = {
        "srcs": attr.label_list(allow_files = True),
        "strip_prefix": attr.string(),
    },
)

# Control-plane components use the neutral name. nv-boot-parent keeps its local
# wrapper name because it describes the owning library profile.
nvcf_workspace_runfiles = _workspace_runfiles
nv_boot_workspace_runfiles = _workspace_runfiles

# ============================================================================
# Shared NVCF Java profile.
# ============================================================================
NVCF_JAVA_JAVACOPTS = [
    "--release",
    "25",
    "-Xlint:deprecation",
]

NVCF_JAVA_ERROR_PRONE_COMPAT_JAVACOPTS = [
    "-Xep:CheckReturnValue:OFF",
    "-Xep:ImpossibleNullComparison:OFF",
    "-Xep:OptionalOfRedundantMethod:OFF",
]

def nvcf_java_library(
        name,
        srcs,
        deps = [],
        ide_visible = True,
        javacopts = [],
        resources = [],
        runtime_deps = [],
        testonly = False,
        visibility = None,
        resource_strip_prefix = ""):
    _java_library(
        name = name,
        srcs = srcs,
        deps = _unique(deps + _LOMBOK_COMPILE_DEPS),
        javacopts = NVCF_JAVA_JAVACOPTS + javacopts,
        plugins = _LOMBOK_PLUGINS,
        resources = resources,
        resource_strip_prefix = resource_strip_prefix,
        runtime_deps = runtime_deps,
        tags = [] if ide_visible else ["no-ide"],
        testonly = testonly,
        visibility = visibility,
    )

    main_resource_roots = _conventional_resource_root("src/main/resources")
    if main_resource_roots and not native.existing_rule("_nvcf_ide_main_resources"):
        # JetBrains treats each Bazel `resources` entry as a resource root.
        # Expose the conventional directory without changing runtime packaging.
        _java_library(
            name = "_nvcf_ide_main_resources",
            resources = main_resource_roots,
            tags = ["manual"],
            visibility = ["//visibility:private"],
        )

    test_resource_roots = _conventional_resource_root("src/test/resources")
    if test_resource_roots and not native.existing_rule("_nvcf_ide_test_resources"):
        # Keep this metadata-only target outside the test macro. JetBrains
        # requires a runnable test macro to expand to one same-named target.
        _java_test(
            name = "_nvcf_ide_test_resources",
            main_class = "org.junit.platform.console.ConsoleLauncher",
            resources = test_resource_roots,
            runtime_deps = _JUNIT5_RUNTIME_DEPS,
            tags = ["manual"],
            testonly = True,
            use_testrunner = False,
            visibility = ["//visibility:private"],
        )

def nvcf_java_test(
        name,
        deps,
        data = [],
        include_classname = ".*(Test|IntegrationTest)",
        ide_visible = True,
        javacopts = [],
        junit_classpath = [],
        jvm_flags = [],
        resources = [],
        runtime_deps = [],
        size = "small",
        srcs = [],
        tags = [],
        timeout = "short",
        resource_strip_prefix = "",
        visibility = None):
    compile_deps = _unique(deps + _LOMBOK_COMPILE_DEPS + _JUNIT5_COMPILE_DEPS)
    test_runtime_deps = _unique(runtime_deps + _JUNIT5_RUNTIME_DEPS)
    test_jar = native.package_name() + "/" + name + ".jar"
    test_args = {
        "name": name,
        "srcs": srcs,
        "data": _unique(data + [_JACOCO_AGENT, _MOCKITO_CORE]),
        "deps": compile_deps,
        "javacopts": NVCF_JAVA_JAVACOPTS + javacopts,
        "jvm_flags": _JACOCO_AGENT_JVM_FLAGS + [
            "-javaagent:$(location %s)" % _MOCKITO_CORE,
        ] + jvm_flags,
        "main_class": "org.junit.platform.console.ConsoleLauncher",
        "plugins": _LOMBOK_PLUGINS,
        "resources": resources,
        "runtime_deps": test_runtime_deps,
        "size": size,
        "tags": _unique(tags + ["manual"] + ([] if ide_visible else ["no-ide"])),
        "testonly": True,
        "timeout": timeout,
        "use_testrunner": False,
        "visibility": visibility,
        "args": _JUNIT5_ARGS + [
            "--include-classname=%s" % include_classname,
            "--class-path=%s" % test_jar,
            "--scan-classpath=%s" % test_jar,
        ] + [
            "--class-path=%s" % path
            for path in junit_classpath
        ],
    }
    if resource_strip_prefix:
        test_args["resource_strip_prefix"] = resource_strip_prefix
    _java_test(**test_args)

def nvcf_java_coverage_test(
        name,
        test,
        coverage_target,
        include_classname = ".*(Test|IntegrationTest)",
        junit_classpath = [],
        size = "small",
        tags = [],
        timeout = "short",
        visibility = None):
    if type(test) != "string" or not test.startswith(":"):
        fail("test must be a local label starting with ':'")
    if type(coverage_target) != "string" or not coverage_target.startswith(":"):
        fail(
            "coverage_target must be the module library target as a local "
            + "label starting with ':'",
        )

    coverage_sourcefiles = native.glob(["src/main/java/**/*.java"])
    coverage_source_root = native.package_name() + "/src/main/java"
    test_jar = native.package_name() + "/" + test[1:] + ".jar"
    _sh_test(
        name = name,
        srcs = [_JACOCO_TEST_RUNNER],
        args = [
            "$(location %s)" % test,
            "$(location %s)" % coverage_target,
            coverage_source_root if coverage_sourcefiles else "",
            native.package_name(),
            "$(location %s)" % _JACOCO_CLI,
        ] + _JUNIT5_ARGS + [
            "--include-classname=%s" % include_classname,
            "--class-path=%s" % test_jar,
            "--scan-classpath=%s" % test_jar,
        ] + [
            "--class-path=%s" % path
            for path in junit_classpath
        ],
        data = _unique([
            test,
            coverage_target,
            _JACOCO_CLI,
        ] + coverage_sourcefiles),
        size = size,
        tags = tags,
        timeout = timeout,
        visibility = visibility,
    )

# ============================================================================
# nv-boot-parent local profile.
# ============================================================================
def nv_boot_library(
        name,
        srcs,
        deps = [],
        ide_visible = True,
        javacopts = [],
        resources = [],
        runtime_deps = [],
        testonly = False,
        visibility = None,
        resource_strip_prefix = ""):
    nvcf_java_library(
        name = name,
        srcs = srcs,
        deps = deps,
        ide_visible = ide_visible,
        javacopts = javacopts,
        resources = resources,
        resource_strip_prefix = resource_strip_prefix,
        runtime_deps = runtime_deps,
        testonly = testonly,
        visibility = visibility,
    )
