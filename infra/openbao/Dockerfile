# The JWT secrets plugin is compiled here rather than copied in prebuilt.
#
# It used to come from scripts/build-jwt-plugin.sh, which cloned a fork hosted
# outside this repository at a pinned revision and dropped the binaries into
# files/plugins/. That made the image unbuildable from public source, so the
# binaries had to reach the build some other way: committed to git, or injected
# as a private overlay the way the cassandra image takes its exporter jar.
#
# Neither is needed now. The plugin source lives at plugins/ inside this build
# context, so the stage below compiles it from the same commit that produces
# the image. Nothing is vendored and nothing is injected, so `docker build .`
# here produces the same image as the release pipeline.
ARG BAO_VERSION=2.6.2
ARG GO_VERSION=1.27.0
ARG GO_IMAGE=golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc
ARG BAO_SOURCE_COMMIT=dd9c19c37a878cf4a81b18efb8d6f0599c7da923
ARG BAO_SOURCE_SHA256=a7784550a9db16f24e99d65a18c9b12a433707c79ef4c1f34262d3f48171c7a9
ARG BAO_RUNTIME_IMAGE=openbao/openbao:2.6.2@sha256:11fd73a2102cda9c55d5d881a8c3210303146a7ec1e8ac76f526e175c6d24641

FROM ${GO_IMAGE} AS bao-build
ARG BAO_VERSION
ARG BAO_SOURCE_COMMIT
ARG BAO_SOURCE_SHA256
ARG GO_VERSION
ARG TARGETARCH
RUN apk add --no-cache curl xz
COPY scripts/build-openbao.sh scripts/verify-openbao.sh scripts/semver.sh /usr/local/bin/
RUN BAO_VERSION="${BAO_VERSION}" \
    BAO_SOURCE_COMMIT="${BAO_SOURCE_COMMIT}" \
    BAO_SOURCE_SHA256="${BAO_SOURCE_SHA256}" \
    GO_TOOLCHAIN="go${GO_VERSION}" \
    TARGETARCH="${TARGETARCH}" \
    OUTPUT_DIR=/out \
    /usr/local/bin/build-openbao.sh

FROM ${GO_IMAGE} AS plugin-build
ARG TARGETARCH
WORKDIR /src
# Module files first so dependency download caches independently of source edits.
COPY plugins/vault-plugin-secrets-jwt/go.mod plugins/vault-plugin-secrets-jwt/go.sum ./
RUN go mod download
COPY plugins/vault-plugin-secrets-jwt/ ./
# CGO_ENABLED=0 for a static binary: the plugin is exec'd by the OpenBao server
# inside a distro image it was not linked against, so it must not need a
# dynamic loader. -trimpath keeps build paths out of the binary.
RUN GOOS=linux GOARCH="${TARGETARCH}" CGO_ENABLED=0 \
    go build -trimpath -o /out/vault-plugin-secrets-jwt ./cmd/vault-plugin-secrets-jwt

FROM ${BAO_RUNTIME_IMAGE}
ARG TARGETARCH

# OpenBao 2.6.2 runs as the non-root openbao user by default. Escalate only
# while installing the image's runtime packages, then restore that identity.
USER root
# Upgrade packages inherited from the upstream image before installing the
# runtime tools. This ensures security fixes from the pinned Alpine branch are
# applied even when the upstream image predates them.
# hadolint ignore=DL3018
RUN apk upgrade --no-cache && \
    apk add --no-cache curl jq bash && \
    mkdir -p /openbao/plugins

COPY --from=bao-build --chmod=0555 /out/bao-linux-${TARGETARCH} /usr/bin/bao
COPY --from=plugin-build --chmod=0555 /out/vault-plugin-secrets-jwt /openbao/plugins/vault-plugin-secrets-jwt

USER openbao
