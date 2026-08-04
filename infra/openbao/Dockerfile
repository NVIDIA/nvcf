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
ARG BAO_VERSION=2.5.5
ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS plugin-build
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

FROM openbao/openbao:${BAO_VERSION}

# hadolint ignore=DL3018
RUN apk add --no-cache curl jq bash && \
    mkdir -p /openbao/plugins

COPY --from=plugin-build --chmod=0555 /out/vault-plugin-secrets-jwt /openbao/plugins/vault-plugin-secrets-jwt
