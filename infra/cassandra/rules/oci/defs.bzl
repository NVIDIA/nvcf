# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"Public OCI image rules for assembling multi-arch Cassandra images."

load(
    "//rules/oci/private:common.bzl",
    _DEFAULT_BASE = "DEFAULT_BASE",
    _create_oci_image = "create_oci_image",
)

create_oci_image = _create_oci_image
DEFAULT_BASE = _DEFAULT_BASE

# The exporter agent reflects into JDK-internal com.sun.jmx classes; on the
# JDK 17 runtime in the official base those packages are not open to unnamed
# modules by default. These --add-opens are harmless when no agent is wired
# in, so they stay unconditional (Dockerfile parity). Shared by the public
# image (add-opens only) and the nvidia-internal with-exporter image (which
# prepends -javaagent).
JVM_ADD_OPENS = " ".join([
    "--add-opens=java.management/com.sun.jmx.mbeanserver=ALL-UNNAMED",
    "--add-opens=java.management/com.sun.jmx.interceptor=ALL-UNNAMED",
    "--add-opens=java.management/com.sun.jmx.remote.util=ALL-UNNAMED",
    "--add-opens=java.management/com.sun.jmx.remote.internal=ALL-UNNAMED",
])
