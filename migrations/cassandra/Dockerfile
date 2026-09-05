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

FROM ${GO_IMAGE} AS kubectl-downloader

ARG TARGETARCH
ARG KUBECTL_VERSION=v1.37.0
ARG KUBECTL_MIN_GO_VERSION=1.26.6
ARG KUBECTL_LINUX_AMD64_SHA256=6129359f4e1f3848a5572ccb0b26cf28b8ca08cef38c95a765b2f64a2c961a2f
ARG KUBECTL_LINUX_ARM64_SHA256=922df28df248cc00a9e025f947704f1d1482de64ece54cfe57e61f19eaf1eef3

# Kubernetes v1.37.0 is built with Go 1.26.6. Verify both the official checksum
# and the reviewed per-architecture digest.
RUN apk add --no-cache curl && \
    case "${TARGETARCH}" in \
      amd64) KUBECTL_SHA256="${KUBECTL_LINUX_AMD64_SHA256}" ;; \
      arm64) KUBECTL_SHA256="${KUBECTL_LINUX_ARM64_SHA256}" ;; \
      *) echo "Unsupported architecture for kubectl: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    KUBECTL_BASE_URL="https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}" && \
    mkdir -p /out && \
    curl -fsSLo /out/kubectl "${KUBECTL_BASE_URL}/kubectl" && \
    curl -fsSLo /tmp/kubectl.sha256 "${KUBECTL_BASE_URL}/kubectl.sha256" && \
    test "$(cat /tmp/kubectl.sha256)" = "${KUBECTL_SHA256}" && \
    printf '%s  %s\n' "${KUBECTL_SHA256}" /out/kubectl | sha256sum -c - && \
    chmod 0555 /out/kubectl && \
    KUBECTL_METADATA="$(/out/kubectl version --client -o json)" && \
    KUBECTL_GIT_VERSION="$(printf '%s\n' "${KUBECTL_METADATA}" | awk -F '"' '/"gitVersion":/ { print $4; exit }')" && \
    KUBECTL_GO_VERSION="$(printf '%s\n' "${KUBECTL_METADATA}" | awk -F '"' '/"goVersion":/ { sub(/^go/, "", $4); print $4; exit }')" && \
    test "${KUBECTL_GIT_VERSION}" = "${KUBECTL_VERSION}" && \
    awk -v actual="${KUBECTL_GO_VERSION}" -v minimum="${KUBECTL_MIN_GO_VERSION}" \
      'BEGIN { \
        split(actual, a, "."); split(minimum, m, "."); \
        for (i = 1; i <= 3; i++) { \
          if (a[i] + 0 > m[i] + 0) exit 0; \
          if (a[i] + 0 < m[i] + 0) exit 1; \
        } \
        exit 0 \
      }'

FROM cassandra:5.0.9

ARG GIT_SHA

USER root

RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get upgrade -y && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl gettext-base && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

COPY --from=migrate-builder --chmod=0555 /out/migrate /usr/bin/migrate
COPY --from=kubectl-downloader --chmod=0555 /out/kubectl /usr/local/bin/kubectl

COPY keyspaces /app/keyspaces
COPY --chmod=775 execute_sqls.sh /app

LABEL org.opencontainers.image.revision="${GIT_SHA}"

ENTRYPOINT ["/bin/sh"]
CMD ["/app/execute_sqls.sh"]
