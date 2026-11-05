ARG GO_IMAGE=golang:1.27.3-alpine3.23@sha256:754d5200fc3edae76d3b6598a9a3fac7e74981ee6a5413ab955025f185753ef2

# Cassandra 5.0.9 still copies an older Temurin 17 JRE. Use the patched
# Temurin 17.0.21+9 trust store without changing Cassandra or the JVM binary.
FROM --platform=$BUILDPLATFORM alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63 AS patched-cacerts
ARG TARGETARCH
RUN apk add --no-cache curl && \
    case "${TARGETARCH}" in \
      amd64) jre_arch=x64; jre_sha256=9f41bf88c7900c7097444a1bdeb84146ae692bc08842490ab098063c74bf9a8a ;; \
      arm64) jre_arch=aarch64; jre_sha256=1114f09d6174f54270c3117410b5e4022b43ebc0b43b751af08b927c55ed7bb5 ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSL --retry 7 --retry-all-errors -o /tmp/temurin-jre.tar.gz \
      "https://github.com/adoptium/temurin17-binaries/releases/download/jdk-17.0.21%2B9/OpenJDK17U-jre_${jre_arch}_linux_hotspot_17.0.21_9.tar.gz" && \
    printf '%s  /tmp/temurin-jre.tar.gz\n' "${jre_sha256}" | sha256sum -c - && \
    mkdir -p /tmp/temurin-jre /out && \
    tar -xzf /tmp/temurin-jre.tar.gz -C /tmp/temurin-jre && \
    cacerts=$(find /tmp/temurin-jre -type f -path '*/lib/security/cacerts') && \
    test -f "${cacerts}" && \
    cp "${cacerts}" /out/cacerts

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS migrate-builder

ARG TARGETARCH
ARG MIGRATE_VERSION=v4.19.1
ARG MIGRATE_SOURCE_SHA256=a81780cf0fd577735fc58ce6c2f9829b3b8c9a2f41d326c9edbe3f00ff271c68

# Build only the Cassandra database driver. The upstream release binary also
# bundles unrelated database and cloud drivers and their runtime dependencies.
RUN apk add --no-cache curl unzip && \
    curl -fsSLo /tmp/migrate.zip \
      "https://github.com/golang-migrate/migrate/releases/download/${MIGRATE_VERSION}/migrate-${MIGRATE_VERSION#v}.zip" && \
    printf '%s  %s\n' "${MIGRATE_SOURCE_SHA256}" /tmp/migrate.zip | sha256sum -c - && \
    mkdir -p /src /out && \
    unzip -q /tmp/migrate.zip -d /src && \
    cd /src && \
    MIGRATE_BUILD_VERSION="${MIGRATE_VERSION#v}" && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" GOTOOLCHAIN=local \
      go build -mod=readonly -trimpath -buildvcs=false -tags cassandra \
      -ldflags="-s -w -buildid= -X main.Version=${MIGRATE_BUILD_VERSION}" \
      -o /out/migrate ./cmd/migrate

FROM --platform=$BUILDPLATFORM alpine:3.23 AS java-library-downloader

# Cassandra can lag security-only Jackson releases. Fetch one coherent,
# checksum-pinned replacement set without adding build tools to the final
# image. Release builds can route the same paths through a caching repository.
ARG MAVEN_REPOSITORY_BASE=https://repo.maven.apache.org/maven2
COPY java-libraries.lock /tmp/java-libraries.lock
RUN apk add --no-cache curl && \
    mkdir -p /tmp/java-libraries && \
    while read -r checksum url; do \
      case "${checksum}" in \#*|'') continue ;; esac; \
      path="${url#https://repo.maven.apache.org/maven2/}"; \
      test "${path}" != "${url}"; \
      destination="/tmp/java-libraries/${url##*/}"; \
      curl -fsSL --retry 7 --retry-all-errors \
        -o "${destination}" "${MAVEN_REPOSITORY_BASE%/}/${path}"; \
      printf '%s  %s\n' "${checksum}" "${destination}" | sha256sum -c -; \
    done < /tmp/java-libraries.lock

FROM cassandra:5.0.9@sha256:ae4cc2e4b62a5b2a2dd1645ba827cb50c589cc7531bcffe8b8b60ac3b1adc79d

ARG GIT_SHA

USER root

COPY --from=patched-cacerts --chmod=0644 /out/cacerts /opt/java/openjdk/lib/security/cacerts
COPY --chmod=0644 NOTICE /usr/share/doc/cacerts-NOTICE

RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get upgrade -y && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates gettext-base && \
    dpkg --compare-versions "$(dpkg-query -W -f='${Version}' perl-base)" ge 5.40.1-6+deb13u1 && \
    dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge 20250419+deb13u1 && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* && \
    rm -f \
      /opt/cassandra/lib/jackson-annotations-*.jar \
      /opt/cassandra/lib/jackson-core-*.jar \
      /opt/cassandra/lib/jackson-databind-*.jar \
      /opt/cassandra/lib/jackson-datatype-jsr310-*.jar \
      /opt/cassandra/lib/lz4-java-*.jar \
      /opt/cassandra/lib/netty-*-4.1.130.Final*.jar

COPY --from=migrate-builder --chmod=0555 /out/migrate /usr/bin/migrate
COPY --from=java-library-downloader /tmp/java-libraries/*.jar /opt/cassandra/lib/

# The Cassandra base uses gosu from its own entrypoint. This image replaces
# that entrypoint with /bin/sh and never invokes the inherited helper.
RUN rm -f /usr/local/bin/gosu

COPY keyspaces /app/keyspaces
COPY --chmod=775 execute_sqls.sh /app

LABEL org.opencontainers.image.revision="${GIT_SHA}"

USER 999:999

ENTRYPOINT ["/bin/sh"]
CMD ["/app/execute_sqls.sh"]
