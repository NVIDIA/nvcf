# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# yq is used by the chart's conf initContainer to deep-merge cassandra.yaml
# overrides onto the image default. Fetch it in a native build stage (no
# emulation) and verify it against a pinned checksum.
FROM --platform=$BUILDPLATFORM alpine:3.21 AS yq-downloader
ARG TARGETARCH
ARG YQ_VERSION=v4.53.6
ARG YQ_LINUX_AMD64_SHA256=c5f056448f973ae7d39b5401949648a78f2dc1947d6a8eb65be60d5c504b9385
ARG YQ_LINUX_ARM64_SHA256=88a1016bc1d657375a35864e4f44b6f333df8ff97b559f51bba0adcb2169df09
RUN apk add --no-cache curl && \
    case "${TARGETARCH}" in \
      amd64) YQ_SHA256="${YQ_LINUX_AMD64_SHA256}" ;; \
      arm64) YQ_SHA256="${YQ_LINUX_ARM64_SHA256}" ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSLo /tmp/yq "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${TARGETARCH}" && \
    printf '%s  /tmp/yq\n' "${YQ_SHA256}" | sha256sum -c - && \
    chmod +x /tmp/yq

# Cassandra 5.0.9 is still the latest supported 5.0 release, but its bundled
# Jackson and Netty versions can lag security-only patch releases. Download a
# checksum-pinned replacement set without adding build tools to the final image.
COPY java-libraries.lock /tmp/java-libraries.lock
RUN mkdir /tmp/java-libraries && \
    while read -r checksum url; do \
      case "${checksum}" in \#*|'') continue ;; esac; \
      destination="/tmp/java-libraries/${url##*/}"; \
      curl -fsSLo "${destination}" "${url}"; \
      printf '%s  %s\n' "${checksum}" "${destination}" | sha256sum -c -; \
    done < /tmp/java-libraries.lock

FROM cassandra:5.0.9@sha256:d35e159439b302146f964919904f84fd3c2cebf347272b8cb8c4368c1cf200e5

# Refresh the OpenSSL runtime packages independently of the upstream image
# rebuild cadence. Fail the build if the Debian repository cannot provide the
# first release containing the required fixes.
RUN apt-get update && \
    apt-get install -y --no-install-recommends --only-upgrade \
      libssl3t64 \
      openssl-provider-legacy && \
    dpkg --compare-versions "$(dpkg-query -W -f='${Version}' libssl3t64)" ge 3.5.7-1~deb13u2 && \
    dpkg --compare-versions "$(dpkg-query -W -f='${Version}' openssl-provider-legacy)" ge 3.5.7-1~deb13u2 && \
    rm -rf /var/lib/apt/lists/* && \
    rm -f \
      /opt/cassandra/lib/jackson-annotations-*.jar \
      /opt/cassandra/lib/jackson-core-*.jar \
      /opt/cassandra/lib/jackson-databind-*.jar \
      /opt/cassandra/lib/jackson-datatype-jsr310-*.jar && \
    find /opt/cassandra/lib -maxdepth 1 -type f -name 'netty-*-4.1.130.Final*.jar' -delete

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
COPY --from=yq-downloader /tmp/java-libraries/*.jar /opt/cassandra/lib/

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
