ARG GO_IMAGE=golang:1.27.0-alpine3.23@sha256:3747dcba41c8b0db3211fda4db61638b980e17ac5bb3c94460a975a9cfe19395

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

FROM cassandra:5.0.9

ARG GIT_SHA

USER root

RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get upgrade -y && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates gettext-base && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* && \
    rm -f \
      /opt/cassandra/lib/jackson-annotations-*.jar \
      /opt/cassandra/lib/jackson-core-*.jar \
      /opt/cassandra/lib/jackson-databind-*.jar \
      /opt/cassandra/lib/jackson-datatype-jsr310-*.jar \
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
