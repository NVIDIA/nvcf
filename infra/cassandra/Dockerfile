# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

ARG GOSU_GO_IMAGE=golang:1.27.1-trixie@sha256:9baa6b4187bbb98d240372a8a235ac0bb6b5ddd52bba1431dc2f7c0705862728

FROM --platform=$BUILDPLATFORM ${GOSU_GO_IMAGE} AS gosu-builder

ARG TARGETARCH

# Rebuild the same gosu 1.19 source used by the Cassandra base with a current
# Go toolchain. The immutable source archive is checksum-pinned because the
# upstream release binaries were built with an older Go standard library.
ADD --checksum=sha256:33d7537d588ea49458b9509bcf4554bdf5ceacc66da71e5caa1058ea3b689c3b \
    https://github.com/tianon/gosu/archive/6456aaa0f3c854d199d0f037f068eb97515b7513.tar.gz \
    /tmp/gosu.tar.gz
RUN mkdir -p /src/gosu /out && \
    tar -xzf /tmp/gosu.tar.gz --strip-components=1 -C /src/gosu && \
    cd /src/gosu && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" GOTOOLCHAIN=local \
      go build -mod=readonly -trimpath -buildvcs=false \
      -ldflags="-d -w" -o /out/gosu . && \
    go version -m /out/gosu

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
# The lock pins public Maven Central URLs. A release build can route the same
# artifact paths through a caching repository manager with
# --build-arg MAVEN_REPOSITORY_BASE=<base>; checksums are verified either way.
ARG MAVEN_REPOSITORY_BASE=https://repo.maven.apache.org/maven2
COPY java-libraries.lock /tmp/java-libraries.lock
COPY scripts/fetch-verified.sh /usr/local/bin/fetch-verified
COPY scripts/fetch-java-libraries.sh /usr/local/bin/fetch-java-libraries
RUN MAVEN_REPOSITORY_BASE="${MAVEN_REPOSITORY_BASE}" \
    fetch-java-libraries /tmp/java-libraries.lock /tmp/java-libraries

FROM --platform=$BUILDPLATFORM alpine:3.21 AS exporter-repacker
RUN apk add --no-cache curl unzip zip

# The release build supplies a compatible exporter agent. Replace the complete
# shaded Netty module set with checksum-pinned security updates while retaining
# the agent implementation, manifest, and unrelated dependencies.
ARG EXPORTER_JAR=files/.gitkeep
ARG EXPORTER_JAVAAGENT=""
# Same override as the java-libraries stage; the Netty jars default to Maven
# Central's canonical host when it is not set.
ARG MAVEN_REPOSITORY_BASE=""
COPY ${EXPORTER_JAR} /tmp/exporter-input.jar
COPY scripts/fetch-verified.sh /usr/local/bin/fetch-verified
COPY scripts/repack-exporter-netty.sh /usr/local/bin/repack-exporter-netty
RUN if [ -s /tmp/exporter-input.jar ] && [ -z "${EXPORTER_JAVAAGENT}" ]; then \
      echo "EXPORTER_JAR and EXPORTER_JAVAAGENT must be set together" >&2; \
      exit 1; \
    fi && \
    if [ ! -s /tmp/exporter-input.jar ] && [ -n "${EXPORTER_JAVAAGENT}" ]; then \
      echo "EXPORTER_JAR and EXPORTER_JAVAAGENT must be set together" >&2; \
      exit 1; \
    fi && \
    MAVEN_REPOSITORY_BASE="${MAVEN_REPOSITORY_BASE}" \
    repack-exporter-netty /tmp/exporter-input.jar /tmp/cassandra-exporter-agent.jar

FROM cassandra:5.0.9@sha256:d35e159439b302146f964919904f84fd3c2cebf347272b8cb8c4368c1cf200e5

COPY --from=gosu-builder --chmod=0555 /out/gosu /usr/local/bin/gosu

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
    gosu --version && \
    gosu nobody true && \
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
# The javaagent flag itself is also gated behind a build arg. Without a real
# jar, wiring -javaagent unconditionally would point the JVM at the empty
# .gitkeep placeholder and abort startup with "Error opening zip file or JAR
# manifest missing". Internal builds pass both EXPORTER_JAR (the real jar)
# and EXPORTER_JAVAAGENT (the -javaagent flag) together; a bare OSS build
# passes neither, so the image boots with no agent and no metrics.
ARG EXPORTER_JAVAAGENT=""

COPY --from=exporter-repacker /tmp/cassandra-exporter-agent.jar /opt/cassandra/lib/cassandra-exporter-agent.jar
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

USER 999:999
