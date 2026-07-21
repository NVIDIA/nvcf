load("@rules_java//java:defs.bzl", _java_binary = "java_binary", _java_library = "java_library")
load("@rules_java//java/common:java_info.bzl", "JavaInfo")
load("@rules_shell//shell:sh_test.bzl", _sh_test = "sh_test")

NV_JAVA_JAVACOPTS = [
    "-Xlint:deprecation",
]

NV_LOMBOK_COMPILE_DEPS = [
    "//src/libraries/java/nv-boot-parent/tools/bazel:lombok_annotations",
]

NV_LOMBOK_PLUGINS = [
    "//src/libraries/java/nv-boot-parent/tools/bazel:lombok_plugin",
]

NV_JUNIT5_ARGS = [
    "execute",
    "--details=flat",
    "--disable-ansi-colors",
    "--details-theme=ascii",
    "--include-classname=.*(Test|IntegrationTest)",
    "--fail-if-no-tests",
]

NV_JUNIT5_RUNTIME_DEPS = [
    "@nv_third_party_deps//:org_junit_platform_junit_platform_console_standalone",
]

NV_JUNIT5_COMPILE_DEPS = [
    "@nv_third_party_deps//:org_assertj_assertj_core",
    "@nv_third_party_deps//:org_junit_jupiter_junit_jupiter_api",
    "@nv_third_party_deps//:org_junit_jupiter_junit_jupiter_params",
    "@nv_third_party_deps//:org_mockito_mockito_core",
    "@nv_third_party_deps//:org_mockito_mockito_junit_jupiter",
    "@nv_third_party_deps//:org_springframework_boot_spring_boot_test",
    "@nv_third_party_deps//:org_springframework_boot_spring_boot_test_autoconfigure",
    "@nv_third_party_deps//:org_springframework_spring_test",
]

NV_MOCKITO_CORE = "@nv_third_party_deps//:org_mockito_mockito_core"

NV_MOCKITO_AGENT_DATA = [
    NV_MOCKITO_CORE,
]

NV_MOCKITO_AGENT_JVM_FLAGS = [
    "-javaagent:$(location %s)" % NV_MOCKITO_CORE,
]

NV_JACOCO_AGENT = "@nv_third_party_deps//:org_jacoco_org_jacoco_agent_runtime"

NV_JACOCO_AGENT_DATA = [
    NV_JACOCO_AGENT,
]

NV_JACOCO_AGENT_JVM_FLAGS = [
    (
        "-javaagent:$(location %s)=destfile=jacoco.exec,append=false,"
        + "dumponexit=true,includes=com.nvidia.*"
    ) % NV_JACOCO_AGENT,
]

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

def _nv_boot_workspace_runfiles_impl(ctx):
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

nv_boot_workspace_runfiles = rule(
    implementation = _nv_boot_workspace_runfiles_impl,
    attrs = {
        "srcs": attr.label_list(allow_files = True),
        "strip_prefix": attr.string(),
    },
)

def nv_boot_library(
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
        deps = deps + NV_LOMBOK_COMPILE_DEPS,
        javacopts = NV_JAVA_JAVACOPTS,
        plugins = NV_LOMBOK_PLUGINS,
        resources = resources,
        resource_strip_prefix = resource_strip_prefix,
        runtime_deps = runtime_deps,
        visibility = visibility,
    )

def nv_boot_library_test(
        name,
        srcs,
        deps,
        coverage_library,
        data = [],
        junit_classpath = [],
        jvm_flags = [],
        resources = [],
        runtime_deps = [],
        size = "small",
        tags = [],
        timeout = "short",
        resource_strip_prefix = ""):
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
        data = data + NV_MOCKITO_AGENT_DATA + NV_JACOCO_AGENT_DATA,
        deps = deps + NV_LOMBOK_COMPILE_DEPS + NV_JUNIT5_COMPILE_DEPS,
        javacopts = NV_JAVA_JAVACOPTS,
        jvm_flags = NV_JACOCO_AGENT_JVM_FLAGS + NV_MOCKITO_AGENT_JVM_FLAGS + jvm_flags,
        main_class = "org.junit.platform.console.ConsoleLauncher",
        plugins = NV_LOMBOK_PLUGINS,
        resources = resources,
        resource_strip_prefix = resource_strip_prefix,
        runtime_deps = runtime_deps + NV_JUNIT5_RUNTIME_DEPS,
        tags = ["manual"],
        testonly = True,
        visibility = ["//visibility:private"],
    )

    _sh_test(
        name = name,
        srcs = ["//src/libraries/java/nv-boot-parent/tools/bazel:jacoco_test_runner.sh"],
        args = [
            "$(location :%s)" % junit_runner,
            "$(location %s)" % coverage_library,
            coverage_source_root if coverage_sourcefiles else "",
            native.package_name(),
            "$(location //src/libraries/java/nv-boot-parent/tools/bazel:jacoco_cli)",
        ] + NV_JUNIT5_ARGS + [
            "--class-path=$(location :%s.jar)" % junit_runner,
            "--scan-classpath=$(location :%s.jar)" % junit_runner,
        ] + [
            "--class-path=%s" % path
            for path in junit_classpath
        ],
        data = [
            ":" + junit_runner,
            ":%s.jar" % junit_runner,
            coverage_library,
            "//src/libraries/java/nv-boot-parent/tools/bazel:jacoco_cli",
        ] + coverage_sourcefiles,
        size = size,
        tags = tags,
        timeout = timeout,
    )
