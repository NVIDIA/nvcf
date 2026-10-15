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

"OCI image rules for Java applications."

load("@rules_pkg//pkg:mappings.bzl", "pkg_attributes", "pkg_files")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")
load("@rules_shell//shell:sh_test.bzl", "sh_test")
load("//rules/oci/private:common.bzl", "create_oci_image")

# NVIDIA distroless java, matching the distroless_go / distroless_cc bases the
# Go and Rust services already use. Publicly pullable, multi-arch, and the same
# base the pre-Bazel service Dockerfiles use. Keep the JDK major version in
# lockstep with --java_language_version in //.bazelrc: a runtime older than the
# emitted bytecode fails at container startup with UnsupportedClassVersionError,
# which no build-time check catches.
DEFAULT_JAVA_BASE = "@distroless_java"

# The distroless bases set ENTRYPOINT ["/usr/bin/shelless_ulimit"], a shim that
# raises the soft file-descriptor limit before exec'ing the real command.
# oci_image REPLACES the base entrypoint rather than appending, so omitting the
# shim here would silently drop it and leave the container on the default 1024
# soft limit. Same fix grpc-proxy applies for its Go image.
ULIMIT_SHIM = "/usr/bin/shelless_ulimit"

# Arch-stable java path. Do NOT build this from JAVA_HOME: the base sets it
# per-architecture (/usr/lib/jvm/temurin-25-jdk-amd64 vs -arm64), so a
# hardcoded JAVA_HOME path would break the arm64 half of the image index.
# /usr/bin/java is present on both arches.
JAVA_BIN = "/usr/bin/java"

def _java_oci_image_impl(name, visibility, jar, base, jar_path, java_bin, entrypoint, jvm_flags, env, workdir, registry, tags):
    layer_name = name + "_layer"
    files_name = name + "_files"

    if not jar_path.startswith("/"):
        fail("jar_path must be absolute, got: " + jar_path)
    jar_dir, _, jar_name = jar_path.rpartition("/")

    # Install the jar AT jar_path. pkg_tar alone preserves the source
    # basename and only the entrypoint would name jar_path, so a jar not
    # already called app.jar, or a jar_path pointing elsewhere, would produce
    # an image whose entrypoint references a file that does not exist. pkg_files
    # renames and relocates it so the layer and the entrypoint cannot disagree.
    #
    # mode 0644 is correct and is NOT the go_oci_image case: a jar is read by
    # the JVM, never exec'd, so it needs no exec bit. The executable in the
    # entrypoint is java, which comes from the base image.
    pkg_files(
        name = files_name,
        srcs = [jar],
        prefix = jar_dir,
        renames = {jar: jar_name},
        attributes = pkg_attributes(mode = "0644"),
        visibility = ["//visibility:private"],
    )

    pkg_tar(
        name = layer_name,
        extension = "tar.gz",
        srcs = [files_name],
        visibility = ["//visibility:private"],
    )

    entry = entrypoint
    if not entry:
        entry = [ULIMIT_SHIM, java_bin] + list(jvm_flags) + ["-jar", jar_path]

    create_oci_image(
        name = name,
        tars = [layer_name],
        base = base,
        entrypoint = entry,
        env = env,
        workdir = workdir,
        visibility = visibility,
        registry = registry,
        tags = tags,
    )

java_oci_image = macro(
    doc = """Packages a Java application jar into a multi-arch OCI image.

    Intended for a Spring Boot executable jar (see //rules/java:spring.bzl),
    but works with any self-contained runnable jar. Produces the same target
    set as go_oci_image: {name}, {name}_index (multi-arch, this is what you
    push), {name}_load, {name}.tar, and {name}_push when registry is set.
    """,
    implementation = _java_oci_image_impl,
    attrs = {
        "jar": attr.label(
            doc = "The runnable jar to package (e.g. a spring_boot_app target).",
            mandatory = True,
            allow_single_file = True,
            configurable = False,
        ),
        "base": attr.label(
            doc = """Base OCI image. It must provide a Java launcher at the
            absolute path given by java_bin (default /usr/bin/java) and the
            shelless_ulimit shim, which the NVIDIA distroless bases do. A base
            with Java only on PATH, or at a different absolute path, must set
            java_bin accordingly.""",
            default = DEFAULT_JAVA_BASE,
            configurable = False,
        ),
        "java_bin": attr.string(
            doc = """Absolute path to the Java launcher in the base image.
            Absolute rather than PATH-resolved, and deliberately not derived
            from JAVA_HOME, which the distroless bases set per architecture.""",
            default = JAVA_BIN,
            configurable = False,
        ),
        "jar_path": attr.string(
            doc = """Absolute in-image path the jar is installed at. The layer
            and the entrypoint are both derived from this, so they cannot
            disagree.""",
            default = "/app.jar",
            configurable = False,
        ),
        "env": attr.string_dict(
            doc = """Environment variables set in the image. Required for
            runtime behavior the base image expects, for example ULIMIT_FLAG=1,
            without which the shelless_ulimit shim runs but raises no limit.""",
            configurable = False,
        ),
        "workdir": attr.string(
            doc = "Container working directory. Unset leaves the base's value.",
            configurable = False,
        ),
        "jvm_flags": attr.string_list(
            doc = "JVM flags inserted before -jar. Ignored when entrypoint is set.",
            configurable = False,
        ),
        "entrypoint": attr.string_list(
            doc = """Container entrypoint. Defaults to
            [/usr/bin/shelless_ulimit, /usr/bin/java, {jvm_flags}..., -jar, {jar_path}].
            If you override this, keep the shelless_ulimit shim first or the
            container loses the raised file-descriptor limit.""",
            configurable = False,
        ),
        "registry": attr.string(
            doc = "Registry to push to. If not set, push target is not created.",
            configurable = False,
        ),
        "tags": attr.string_list(
            doc = "Tags for generated targets. 'manual' is always added.",
            configurable = False,
        ),
    },
)

def _java_image_contract_test_impl(name, visibility, image, jar_path, env, workdir, **kwargs):
    sh_test(
        name = name,
        srcs = ["//rules/oci/private:java_image_contract_test.sh"],
        args = [
            "$(location {}.tar)".format(image),
            "$(location {}_index)".format(image),
            jar_path,
            workdir,
        ],
        # Env expectations travel as environment variables, not args. sh_test
        # args are shell-word-split, so a value containing spaces such as
        # JDK_JAVA_OPTIONS would arrive truncated at its first space and the
        # test would silently assert only the first fragment.
        env = dict(
            [("EXPECT_ENV_COUNT", str(len(env)))] +
            [
                ("EXPECT_ENV_{}".format(i), "{}={}".format(k, env[k]))
                for i, k in enumerate(sorted(env.keys()))
            ],
        ),
        data = [
            image + ".tar",
            image + "_index",
        ],
        visibility = visibility,
        **kwargs
    )

java_image_contract_test = macro(
    doc = """Assert a java_oci_image's runtime contract on every architecture.

    Checks the jar is installed where the entrypoint names it and is not
    whited out, that the entrypoint keeps the base's shelless_ulimit shim,
    that every declared env entry is present as a complete entry, and that
    the working directory matches. Runs against the host tar and against
    each platform in the image index, so an index that is missing or
    mislabels an architecture fails.

    Pass the same `env`, `jar_path` and `workdir` given to java_oci_image.
    """,
    implementation = _java_image_contract_test_impl,
    attrs = {
        "image": attr.string(
            configurable = False,
            mandatory = True,
            doc = "Label of the java_oci_image target, without a suffix.",
        ),
        "jar_path": attr.string(
            configurable = False,
            default = "/usr/share/app.jar",
            doc = "Absolute path the jar is installed at.",
        ),
        "env": attr.string_dict(
            configurable = False,
            default = {},
            doc = "Env entries that must be present, compared exactly.",
        ),
        "workdir": attr.string(
            configurable = False,
            default = "/home/app",
            doc = "Expected working directory.",
        ),
    },
)
