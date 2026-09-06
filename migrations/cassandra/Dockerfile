ARG GO_IMAGE=golang:1.27.0-alpine3.23@sha256:3747dcba41c8b0db3211fda4db61638b980e17ac5bb3c94460a975a9cfe19395
ARG KUBECTL_GO_IMAGE=golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406

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

FROM --platform=$BUILDPLATFORM ${KUBECTL_GO_IMAGE} AS kubectl-builder

ARG TARGETARCH
ARG KUBECTL_VERSION=v1.36.4
ARG KUBECTL_SOURCE_SHA256=3c28f11492472df48e658551bf268fd92938b127b0f9dcef7090ac800318c821
ARG KUBECTL_SOURCE_COMMIT=bb826b1d48562f110659e64e8ec444327433db95
ARG KUBECTL_BUILD_DATE=2026-08-20T03:09:25Z

# kubectl v1.36 is compatible with every Kubernetes minor supported by NVCF
# (v1.35-v1.37). The official v1.36.4 binary was built with Go 1.26.5, so
# rebuild the checksum-pinned release source with the patched Go 1.26.6 toolchain.
RUN apk add --no-cache curl tar && \
    case "${TARGETARCH}" in amd64|arm64) ;; \
      *) echo "Unsupported architecture for kubectl: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    test "$(go env GOVERSION)" = "go1.26.6" && \
    curl -fsSLo /tmp/kubernetes-src.tar.gz \
      "https://dl.k8s.io/${KUBECTL_VERSION}/kubernetes-src.tar.gz" && \
    printf '%s  %s\n' "${KUBECTL_SOURCE_SHA256}" /tmp/kubernetes-src.tar.gz | sha256sum -c - && \
    mkdir -p /src /out && \
    tar -xzf /tmp/kubernetes-src.tar.gz -C /src && \
    cd /src && \
    KUBECTL_LDFLAGS="-s -w -buildid= \
      -X k8s.io/client-go/pkg/version.gitVersion=${KUBECTL_VERSION} \
      -X k8s.io/client-go/pkg/version.gitCommit=${KUBECTL_SOURCE_COMMIT} \
      -X k8s.io/client-go/pkg/version.gitTreeState=clean \
      -X k8s.io/client-go/pkg/version.buildDate=${KUBECTL_BUILD_DATE} \
      -X k8s.io/client-go/pkg/version.gitMajor=1 \
      -X k8s.io/client-go/pkg/version.gitMinor=36 \
      -X k8s.io/component-base/version.gitVersion=${KUBECTL_VERSION} \
      -X k8s.io/component-base/version.gitCommit=${KUBECTL_SOURCE_COMMIT} \
      -X k8s.io/component-base/version.gitTreeState=clean \
      -X k8s.io/component-base/version.buildDate=${KUBECTL_BUILD_DATE} \
      -X k8s.io/component-base/version.gitMajor=1 \
      -X k8s.io/component-base/version.gitMinor=36" && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" GOTOOLCHAIN=local GOPROXY=off \
      go build -mod=vendor -trimpath -tags=selinux,notest,grpcnotrace \
      -ldflags="${KUBECTL_LDFLAGS}" -o /out/kubectl ./cmd/kubectl && \
    CGO_ENABLED=0 GOOS=linux GOARCH="$(go env GOHOSTARCH)" GOTOOLCHAIN=local GOPROXY=off \
      go build -mod=vendor -trimpath -tags=selinux,notest,grpcnotrace \
      -ldflags="${KUBECTL_LDFLAGS}" -o /tmp/kubectl-host ./cmd/kubectl && \
    KUBECTL_METADATA="$(/tmp/kubectl-host version --client -o json)" && \
    KUBECTL_GIT_VERSION="$(printf '%s\n' "${KUBECTL_METADATA}" | awk -F '"' '/"gitVersion":/ { print $4; exit }')" && \
    KUBECTL_GIT_COMMIT="$(printf '%s\n' "${KUBECTL_METADATA}" | awk -F '"' '/"gitCommit":/ { print $4; exit }')" && \
    KUBECTL_BUILD_METADATA_DATE="$(printf '%s\n' "${KUBECTL_METADATA}" | awk -F '"' '/"buildDate":/ { print $4; exit }')" && \
    KUBECTL_GO_VERSION="$(printf '%s\n' "${KUBECTL_METADATA}" | awk -F '"' '/"goVersion":/ { sub(/^go/, "", $4); print $4; exit }')" && \
    test "${KUBECTL_GIT_VERSION}" = "${KUBECTL_VERSION}" && \
    test "${KUBECTL_GIT_COMMIT}" = "${KUBECTL_SOURCE_COMMIT}" && \
    test "${KUBECTL_BUILD_METADATA_DATE}" = "${KUBECTL_BUILD_DATE}" && \
    test "${KUBECTL_GO_VERSION}" = "1.26.6" && \
    go version -m /out/kubectl | grep -Eq '^[[:space:]]*build[[:space:]]+GOOS=linux$' && \
    go version -m /out/kubectl | grep -Eq "^[[:space:]]*build[[:space:]]+GOARCH=${TARGETARCH}$" && \
    chmod 0555 /out/kubectl

FROM cassandra:5.0.9

ARG GIT_SHA

USER root

RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get upgrade -y && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl gettext-base && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

COPY --from=migrate-builder --chmod=0555 /out/migrate /usr/bin/migrate
COPY --from=kubectl-builder --chmod=0555 /out/kubectl /usr/local/bin/kubectl

COPY keyspaces /app/keyspaces
COPY --chmod=775 execute_sqls.sh /app

LABEL org.opencontainers.image.revision="${GIT_SHA}"

ENTRYPOINT ["/bin/sh"]
CMD ["/app/execute_sqls.sh"]
