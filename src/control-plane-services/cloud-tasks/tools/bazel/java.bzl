load("@rules_java//java:defs.bzl", _java_binary = "java_binary", _java_library = "java_library")
load("@rules_shell//shell:sh_test.bzl", _sh_test = "sh_test")

NVCT_JAVACOPTS = [
    "--release",
    "25",
    "-Xep:CheckReturnValue:OFF",
    "-Xep:ImpossibleNullComparison:OFF",
    "-Xep:OptionalOfRedundantMethod:OFF",
    "-Xlint:deprecation",
]

NVCT_LOMBOK_COMPILE_DEPS = [
    "//tools/bazel:lombok_annotations",
]

NVCT_LOMBOK_PLUGINS = [
    "//tools/bazel:lombok_plugin",
]

NVCT_JUNIT_ARGS = [
    "execute",
    "--details=flat",
    "--disable-ansi-colors",
    "--details-theme=ascii",
    "--include-classname=.*(Test|IntegrationTest)",
    "--fail-if-no-tests",
]

NVCT_JUNIT_COMPILE_DEPS = [
    "@nv_third_party_deps//:org_assertj_assertj_core",
    "@nv_third_party_deps//:org_junit_jupiter_junit_jupiter_api",
    "@nv_third_party_deps//:org_junit_jupiter_junit_jupiter_params",
    "@nv_third_party_deps//:org_mockito_mockito_core",
    "@nv_third_party_deps//:org_mockito_mockito_junit_jupiter",
    "@nv_third_party_deps//:org_springframework_boot_spring_boot_test",
    "@nv_third_party_deps//:org_springframework_boot_spring_boot_test_autoconfigure",
    "@nv_third_party_deps//:org_springframework_spring_test",
]

NVCT_JUNIT_RUNTIME_DEPS = [
    "@nv_third_party_deps//:org_junit_platform_junit_platform_console_standalone",
]

NVCT_MOCKITO_CORE = "@nv_third_party_deps//:org_mockito_mockito_core"

NVCT_JACOCO_AGENT = "@nv_third_party_deps//:org_jacoco_org_jacoco_agent_runtime"

NVCT_JACOCO_JVM_FLAGS = [
    (
        "-javaagent:$(location %s)=destfile=jacoco.exec,append=false,"
        + "dumponexit=true,includes=com.nvidia.*"
    ) % NVCT_JACOCO_AGENT,
]

def _unique(values):
    seen = {}
    result = []
    for value in values:
        if value not in seen:
            seen[value] = True
            result.append(value)
    return result

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

def nvct_library(
        name,
        srcs,
        deps = [],
        resources = [],
        runtime_deps = [],
        visibility = None,
        resource_strip_prefix = ""):
    _java_library(
        name = name,
        srcs = srcs,
        deps = deps + NVCT_LOMBOK_COMPILE_DEPS,
        javacopts = NVCT_JAVACOPTS,
        plugins = NVCT_LOMBOK_PLUGINS,
        resources = resources,
        resource_strip_prefix = resource_strip_prefix,
        runtime_deps = runtime_deps,
        visibility = visibility,
    )

def nvct_library_test(
        name,
        deps,
        coverage_library,
        data = [],
        jvm_flags = [],
        resources = [],
        runtime_deps = [],
        size = "large",
        srcs = [],
        tags = [],
        timeout = "long",
        visibility = None):
    if type(coverage_library) != "string" or not coverage_library.startswith(":"):
        fail(
            "coverage_library must be the module library target as a local "
            + "label starting with ':'",
        )

    coverage_sourcefiles = native.glob(["src/main/java/**/*.java"])
    coverage_source_root = native.package_name() + "/src/main/java"
    junit_runner = name + "_junit_runner"

    _java_binary(
        name = junit_runner,
        srcs = srcs,
        data = _unique(data + [
            NVCT_JACOCO_AGENT,
            NVCT_MOCKITO_CORE,
        ]),
        deps = _unique(deps + NVCT_LOMBOK_COMPILE_DEPS + NVCT_JUNIT_COMPILE_DEPS),
        javacopts = NVCT_JAVACOPTS,
        jvm_flags = NVCT_JACOCO_JVM_FLAGS + [
            "-javaagent:$(location %s)" % NVCT_MOCKITO_CORE,
        ] + jvm_flags,
        main_class = "org.junit.platform.console.ConsoleLauncher",
        plugins = NVCT_LOMBOK_PLUGINS,
        resources = resources,
        runtime_deps = runtime_deps + NVCT_JUNIT_RUNTIME_DEPS,
        tags = ["manual"],
        testonly = True,
        visibility = ["//visibility:private"],
    )

    _sh_test(
        name = name,
        srcs = ["//tools/bazel:jacoco_test_runner.sh"],
        args = [
            "$(location :%s)" % junit_runner,
            "$(location %s)" % coverage_library,
            coverage_source_root if coverage_sourcefiles else "",
            native.package_name(),
            "$(location //tools/bazel:jacoco_cli)",
        ] + NVCT_JUNIT_ARGS + [
            "--class-path=$(location :%s.jar)" % junit_runner,
            "--scan-classpath=$(location :%s.jar)" % junit_runner,
        ],
        data = _unique([
            ":" + junit_runner,
            ":%s.jar" % junit_runner,
            coverage_library,
            "//tools/bazel:jacoco_cli",
        ] + coverage_sourcefiles),
        size = size,
        tags = tags,
        timeout = timeout,
        visibility = visibility,
    )
