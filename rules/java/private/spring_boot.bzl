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

"OCI image + library rules for Java (Spring Boot) services."

load("@rules_java//java:defs.bzl", "java_library")
load("@rules_java//java/common:java_info.bzl", "JavaInfo")
load("@rules_pkg//pkg:mappings.bzl", "pkg_files", "strip_prefix")
load("@rules_pkg//pkg:tar.bzl", "pkg_tar")
load("//private:oci.bzl", "create_oci_image")

DEFAULT_JAVA_BASE = "@temurin_jre"

def _runtime_classpath_impl(ctx):
    jars = depset(transitive = [
        dep[JavaInfo].transitive_runtime_jars
        for dep in ctx.attr.deps
    ])
    return [DefaultInfo(files = jars)]

# Collects the transitive runtime jars of one or more Java targets so they can
# be packaged as individual files (see _nvcf_spring_boot_image_impl for why the
# jars are kept separate instead of merged into a fat jar).
_runtime_classpath = rule(
    implementation = _runtime_classpath_impl,
    attrs = {
        "deps": attr.label_list(
            providers = [JavaInfo],
            mandatory = True,
            doc = "Java targets whose transitive runtime jars form the classpath.",
        ),
    },
)

def _nvcf_spring_boot_image_impl(name, visibility, deps, main_class, base, registry, tags, jvm_flags):
    cp_name = name + "_classpath"
    _runtime_classpath(
        name = cp_name,
        deps = deps,
        visibility = ["//visibility:private"],
    )

    files_name = name + "_classpath_files"
    pkg_files(
        name = files_name,
        srcs = [cp_name],
        # Flatten every jar to /app/lib/<basename>. Maven artifact jars are
        # already uniquely named (artifact-version.jar). Bazel-produced jars are
        # named after their target label (lib<name>.jar), so two same-named
        # java_library targets in different packages would collide here. See the
        # deps attr doc for the single-basename constraint on multi-module apps.
        strip_prefix = strip_prefix.files_only(),
        prefix = "app/lib",
        visibility = ["//visibility:private"],
    )

    layer_name = name + "_layer"
    pkg_tar(
        name = layer_name,
        srcs = [files_name],
        visibility = ["//visibility:private"],
    )

    # Run the app off the exploded classpath instead of a fat jar. Keeping each
    # dependency jar separate preserves its META-INF/spring.factories and
    # META-INF/spring/*.AutoConfiguration.imports; a fat/singlejar collapses
    # those same-path resources to a single file and silently breaks Spring
    # Boot auto-configuration.
    entrypoint = ["java"] + jvm_flags + ["-cp", "/app/lib/*", main_class]

    create_oci_image(
        name = name,
        tars = [layer_name],
        base = base,
        entrypoint = entrypoint,
        visibility = visibility,
        registry = registry,
        tags = tags,
    )

nvcf_spring_boot_image = macro(
    doc = "Packages a Java (Spring Boot) app's runtime classpath into a multi-arch OCI image.",
    implementation = _nvcf_spring_boot_image_impl,
    attrs = {
        "deps": attr.label_list(
            doc = (
                "Java targets (typically one java_library) providing the app and its runtime " +
                "deps. Jars are packaged flat under /app/lib by basename, so every jar on the " +
                "transitive runtime classpath must have a unique filename. Maven jars already " +
                "are (artifact-version.jar); Bazel jars take their target label (lib<name>.jar). " +
                "A multi-module app must therefore give its java_library targets distinct names " +
                "(avoid reusing 'lib' or 'main' across packages) or the compiled jars silently " +
                "overwrite each other in the image layer."
            ),
            mandatory = True,
            configurable = False,
        ),
        "main_class": attr.string(
            doc = "Fully-qualified main class (the @SpringBootApplication class).",
            mandatory = True,
            configurable = False,
        ),
        "base": attr.label(
            doc = "Base JRE OCI image.",
            default = DEFAULT_JAVA_BASE,
            configurable = False,
        ),
        "registry": attr.string(
            doc = "Registry to push to. If unset, no push target is created.",
            configurable = False,
        ),
        "tags": attr.string_list(
            doc = "Tags for generated targets. 'manual' is always added.",
            configurable = False,
        ),
        "jvm_flags": attr.string_list(
            doc = "Extra JVM flags inserted before -cp in the entrypoint.",
            configurable = False,
        ),
    },
)

def nvcf_java_library(name, **kwargs):
    """Thin wrapper over java_library for NVCF Java code.

    Exists so services load a single NVCF entry point and so shared defaults
    (for example Maven publishing) can be added here later without touching
    every service BUILD file.
    """
    java_library(name = name, **kwargs)
