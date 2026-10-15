# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# yq is used by the chart's conf initContainer to deep-merge cassandra.yaml
# overrides onto the image default. Fetch it in a native build stage (no
# emulation) and verify it against a pinned checksum.
FROM --platform=$BUILDPLATFORM alpine:3.21 AS yq-downloader
ARG TARGETARCH
ARG YQ_VERSION=v4.44.3
ARG YQ_LINUX_AMD64_SHA256=a2c097180dd884a8d50c956ee16a9cec070f30a7947cf4ebf87d5f36213e9ed7
ARG YQ_LINUX_ARM64_SHA256=0e7e1524f68d91b3ff9b089872d185940ab0fa020a5a9052046ef10547023156
RUN apk add --no-cache curl && \
    case "${TARGETARCH}" in \
      amd64) YQ_SHA256="${YQ_LINUX_AMD64_SHA256}" ;; \
      arm64) YQ_SHA256="${YQ_LINUX_ARM64_SHA256}" ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSLo /tmp/yq "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${TARGETARCH}" && \
    printf '%s  /tmp/yq\n' "${YQ_SHA256}" | sha256sum -c - && \
    chmod +x /tmp/yq

FROM cassandra:5.0.8

# Metrics exporter agent. The OSS snapshot does not redistribute the jar;
# only files/.gitkeep ships, so the default here points at that placeholder
# and a bare `docker build` succeeds with the agent simply absent. Internal
# builds override this with --build-arg EXPORTER_JAR=files/<real-jar> (see
# Makefile / .gitlab-ci.yml). To build with the exporter locally, download
# the jar into files/ and pass --build-arg EXPORTER_JAR=files/<jar-name>.
ARG EXPORTER_JAR=files/.gitkeep

# The javaagent flag itself is also gated behind a build arg. Without a real
# jar, wiring -javaagent unconditionally would point the JVM at the empty
# .gitkeep placeholder and abort startup with "Error opening zip file or JAR
# manifest missing". Internal builds pass both EXPORTER_JAR (the real jar)
# and EXPORTER_JAVAAGENT (the -javaagent flag) together; a bare OSS build
# passes neither, so the image boots with no agent and no metrics.
ARG EXPORTER_JAVAAGENT=""

COPY ${EXPORTER_JAR} /opt/cassandra/lib/cassandra-exporter-agent.jar
COPY scripts/cassandra-env.sh /etc/cassandra/cassandra-env.sh
COPY --from=yq-downloader /tmp/yq /usr/local/bin/yq

# load cassandra exporter prom metrics library
# The exporter agent reflects into JDK-internal com.sun.jmx classes to
# intercept the platform MBeanServer; on the JDK 17 runtime shipped in the
# official image, those packages are not open to unnamed modules by
# default, so the agent needs these --add-opens or it aborts JVM startup
# with a FATAL ERROR. The --add-opens flags are harmless when the agent is
# not wired in, so they stay unconditional.
ENV JVM_EXTRA_OPTS="${EXPORTER_JAVAAGENT} --add-opens=java.management/com.sun.jmx.mbeanserver=ALL-UNNAMED --add-opens=java.management/com.sun.jmx.interceptor=ALL-UNNAMED --add-opens=java.management/com.sun.jmx.remote.util=ALL-UNNAMED --add-opens=java.management/com.sun.jmx.remote.internal=ALL-UNNAMED"

# Add 9500 to defaults for metrics exposure
EXPOSE 7000 9042 9500
