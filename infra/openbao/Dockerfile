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
ARG BAO_VERSION=2.6.3
ARG GO_VERSION=1.27.3
ARG GO_IMAGE=golang:1.27.3-alpine3.23@sha256:8a5046497cda39860b832ffdbe8dd66018a0f05bb01352f533a36e7e2ced66a0
ARG BAO_SOURCE_COMMIT=63a65e6b907589dbb952c371a70260a065bf8bd7
ARG BAO_SOURCE_SHA256=471a2c2e3a156a833704f43335ed5e66476a90719ce692b80c3f541e346cda69
ARG BAO_RUNTIME_IMAGE=openbao/openbao:2.6.3@sha256:a60afafda36337abe833c4a63894bf1095098f29abea4091e7e555a33dd52889
# Preserve the published bytes, including their build metadata. Rebuilding the
# old source would not preserve the digest stored in existing catalogs.
ARG JWT_LEGACY_IMAGE=nvcr.io/nvidia/nvcf/nvcf-openbao:2.6.3-nv-1.3.7@sha256:0400a19179569c7c718106fccf6b296d169dcf5c6df0f17272f7d2453785337e
ARG JWT_INCLUDE_LEGACY=true

FROM ${JWT_LEGACY_IMAGE} AS legacy-plugin

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS bao-build
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

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS plugin-build
ARG TARGETARCH
WORKDIR /src
# Module files first so dependency download caches independently of source edits.
COPY plugins/vault-plugin-secrets-jwt/go.mod plugins/vault-plugin-secrets-jwt/go.sum ./
RUN GOTOOLCHAIN=local go mod download
COPY plugins/vault-plugin-secrets-jwt/ ./
# CGO_ENABLED=0 for a static binary: the plugin is exec'd by the OpenBao server
# inside a distro image it was not linked against, so it must not need a
# dynamic loader. -trimpath keeps build paths out of the binary.
RUN GOOS=linux GOARCH="${TARGETARCH}" CGO_ENABLED=0 GOTOOLCHAIN=local \
    go build -trimpath -o /out/vault-plugin-secrets-jwt ./cmd/vault-plugin-secrets-jwt

FROM ${BAO_RUNTIME_IMAGE} AS plugin-package-base
USER root
COPY --from=plugin-build /out/vault-plugin-secrets-jwt /target/vault-plugin-secrets-jwt
COPY jwt-plugin-versions.env /versions.env
COPY scripts/package-jwt-plugins.sh /package-jwt-plugins.sh

FROM plugin-package-base AS plugin-package-true
COPY --from=legacy-plugin /openbao/plugins/vault-plugin-secrets-jwt /legacy/vault-plugin-secrets-jwt
RUN JWT_INCLUDE_LEGACY=true /bin/sh /package-jwt-plugins.sh

FROM plugin-package-base AS plugin-package-false
RUN JWT_INCLUDE_LEGACY=false /bin/sh /package-jwt-plugins.sh

# BuildKit skips the legacy image dependency when selecting target-only output.
FROM plugin-package-${JWT_INCLUDE_LEGACY} AS plugin-package

FROM ${BAO_RUNTIME_IMAGE}
ARG TARGETARCH

# OpenBao 2.6.3 uses UID 100 and GID 1000. Escalate only while installing
# runtime packages, then restore that identity as numeric OCI metadata.
USER root
# Upgrade packages inherited from the upstream image before installing the
# runtime tools. This ensures security fixes from the pinned Alpine branch are
# applied even when the upstream image predates them.
# hadolint ignore=DL3018
RUN apk upgrade --no-cache && \
    apk add --no-cache 'brotli-libs>=1.2.1-r0' 'ca-certificates>=20261020-r0' \
      'ca-certificates-bundle>=20261020-r0' curl jq bash && \
    mkdir -p /openbao/plugins

COPY --from=bao-build --chmod=0555 /out/bao-linux-${TARGETARCH} /usr/bin/bao
COPY --from=plugin-package /out/plugins/ /openbao/plugins/
COPY --from=plugin-package /out/plugin-config/ /openbao/plugin-config/
COPY jwt-plugin-versions.env /openbao/jwt-plugin-versions.env
COPY --chmod=0555 scripts/jwt-plugin-lifecycle.sh /usr/local/bin/jwt-plugin-lifecycle
RUN mv /usr/local/bin/docker-entrypoint.sh /usr/local/bin/docker-entrypoint-upstream.sh
COPY --chmod=0555 scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

USER 100:1000
