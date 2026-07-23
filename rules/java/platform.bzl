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

"""Single source of truth for the NVCF Java platform.

Every Java module resolves its own Maven graph and writes its own
maven_install.json, which is what makes each module independently buildable. The
risk of independent resolution is version drift: two modules pinning different
Spring versions put two copies on a transitive classpath.

To keep resolutions in agreement, the shared BOM set and version constants live
here, loadable from any BUILD or .bzl file. load() is forbidden in MODULE.bazel,
so a module cannot reference SPRING_BOOT_BOM directly in its maven.install. Each
module instead inlines the same literal BOM list in its MODULE.bazel and guards
it with bom_alignment_test (see //:bom_alignment.bzl), which fails the build if a
module's boms drift from this canonical list. Bumping a shared version is a
one-line change here plus a re-pin of every Java module.
"""

SPRING_BOOT_VERSION = "4.0.7"
SPRING_CLOUD_VERSION = "2025.1.2"
TESTCONTAINERS_VERSION = "2.0.5"
SHEDLOCK_VERSION = "7.7.0"

# Canonical shared BOM set. Keep this list, and the literal boms list in every
# Java module's MODULE.bazel, identical. bom_alignment_test enforces it.
SPRING_BOOT_BOM = [
    "org.springframework.boot:spring-boot-dependencies:%s" % SPRING_BOOT_VERSION,
    "org.springframework.cloud:spring-cloud-dependencies:%s" % SPRING_CLOUD_VERSION,
    "org.testcontainers:testcontainers-bom:%s" % TESTCONTAINERS_VERSION,
    "net.javacrumbs.shedlock:shedlock-bom:%s" % SHEDLOCK_VERSION,
]

# Public Central mirrors only, so the platform resolves anywhere including the
# public GitHub mirror. The Google-hosted mirror is primary (not IP-rate-limited
# like repo1.maven.org); Maven Central is the fallback. Internal builds that need
# NVIDIA artifacts prepend the Artifactory virtual repo in their own overlay.
MAVEN_REPOSITORIES = [
    "https://maven-central.storage-download.googleapis.com/maven2",
    "https://repo.maven.apache.org/maven2",
]
