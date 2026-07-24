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

load("@rules_pkg//pkg:mappings.bzl", "strip_prefix")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")
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

def _java_oci_image_impl(name, visibility, jar, base, jar_path, entrypoint, jvm_flags, registry, tags):
    layer_name = name + "_layer"

    # Place the jar at a fixed absolute path so the entrypoint below can name
    # it. Without strip_prefix + package_dir, rules_pkg writes it at its full
    # workspace short-path and `java -jar /app.jar` fails at runtime with
    # "Unable to access jarfile" while `bazel build :image` still succeeds,
    # because building only assembles the layer and never runs the container.
    #
    # mode 0644 is correct here and is NOT the go_oci_image case: a jar is read
    # by the JVM, never exec'd, so it needs no exec bit. The entrypoint
    # executable is `java`, which comes from the base image.
    pkg_tar(
        name = layer_name,
        extension = "tar.gz",
        srcs = [jar],
        mode = "0644",
        package_dir = "/",
        strip_prefix = strip_prefix.from_pkg(""),
        visibility = ["//visibility:private"],
    )

    entry = entrypoint
    if not entry:
        entry = [ULIMIT_SHIM, JAVA_BIN] + list(jvm_flags) + ["-jar", jar_path]

    create_oci_image(
        name = name,
        tars = [layer_name],
        base = base,
        entrypoint = entry,
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
            doc = "Base OCI image. Must provide a JRE on PATH.",
            default = DEFAULT_JAVA_BASE,
            configurable = False,
        ),
        "jar_path": attr.string(
            doc = "Absolute in-image path of the jar.",
            default = "/app.jar",
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
