ARG ALPINE_VERSION=3.23
ARG GO_IMAGE=golang:1.27.3-alpine3.23@sha256:754d5200fc3edae76d3b6598a9a3fac7e74981ee6a5413ab955025f185753ef2
ARG KUBECTL_GO_IMAGE=golang:1.26.10-alpine3.23@sha256:0525c952defebc693cdaf62cdf43afa32be5a7f377e99260642ea19c240c708e
ARG BAO_VERSION=2.6.3
ARG BAO_RUNTIME_SHA256=sha256:a60afafda36337abe833c4a63894bf1095098f29abea4091e7e555a33dd52889
ARG BAO_SOURCE_COMMIT=63a65e6b907589dbb952c371a70260a065bf8bd7
ARG BAO_SOURCE_SHA256=471a2c2e3a156a833704f43335ed5e66476a90719ce692b80c3f541e346cda69
ARG KUBECTL_VERSION=v1.36.5
ARG KUBECTL_GO_VERSION=go1.26.10
ARG KUBECTL_SOURCE_COMMIT=ad950d1cc78b0183c476bd4d3f1934c104229727
ARG KUBECTL_SOURCE_SHA256=168162db6254cf3c453798de09a3a65f3d11673e0d3e1705fe061d960b05d137
ARG KUBECTL_BUILD_DATE=2026-09-23T17:07:00Z
ARG KUBECTL_SOURCE_DATE_EPOCH=1790183220

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS bao-builder

ARG BAO_VERSION
ARG BAO_SOURCE_COMMIT
ARG BAO_SOURCE_SHA256
ARG TARGETARCH

RUN apk add --no-cache curl xz
COPY scripts/build-openbao.sh scripts/verify-openbao.sh scripts/verify-go-security.sh /usr/local/bin/
RUN BAO_VERSION="${BAO_VERSION}" \
    BAO_SOURCE_COMMIT="${BAO_SOURCE_COMMIT}" \
    BAO_SOURCE_SHA256="${BAO_SOURCE_SHA256}" \
    TARGETARCH="${TARGETARCH}" \
    OUTPUT_DIR=/out \
    /usr/local/bin/build-openbao.sh

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS jwker-builder

COPY scripts/verify-go-security.sh /usr/local/bin/

ARG TARGETARCH
ARG JWKER_VERSION=v0.2.2
ARG JWKER_COMMIT=d708da84676fb39f7c957d7e4d589206d56c3089
ARG JWKER_BUILD_DATE=2025-11-13T20:16:40Z
ARG JWKER_SOURCE_SHA256=258c3618e7dfa293b9db529ff013a09a2ce5bffd468715e26943b9185d773910

# Build jwker from reviewed source with a fixed toolchain and source digest.
RUN apk add --no-cache curl tar && \
    curl -fsSLo /tmp/jwker.tar.gz "https://github.com/jphastings/jwker/archive/refs/tags/${JWKER_VERSION}.tar.gz" && \
    printf '%s  %s\n' "${JWKER_SOURCE_SHA256}" /tmp/jwker.tar.gz | sha256sum -c - && \
    mkdir -p /out /src && \
    tar xzf /tmp/jwker.tar.gz -C /src --strip-components=1 && \
    cd /src && \
    GOTOOLCHAIN=local go mod download && \
    JWKER_BUILD_VERSION="${JWKER_VERSION#v}" && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" GOTOOLCHAIN=local \
        go build -mod=readonly -trimpath -buildvcs=false \
        -ldflags="-s -w -buildid= -X main.version=${JWKER_BUILD_VERSION} -X main.commit=${JWKER_COMMIT} -X main.date=${JWKER_BUILD_DATE}" \
        -o /out/jwker ./cmd/jwker && \
    /usr/local/bin/verify-go-security.sh /out/jwker

FROM --platform=$BUILDPLATFORM ${KUBECTL_GO_IMAGE} AS kubectl-builder

ARG TARGETARCH
ARG KUBECTL_VERSION
ARG KUBECTL_GO_VERSION
ARG KUBECTL_SOURCE_COMMIT
ARG KUBECTL_SOURCE_SHA256
ARG KUBECTL_BUILD_DATE
ARG KUBECTL_SOURCE_DATE_EPOCH

# Rebuild the 1.36 client with the patched toolchain while preserving coverage
# across the repository's latest-and-N-2 Kubernetes support window.
RUN apk add --no-cache curl
COPY scripts/build-kubectl.sh /usr/local/bin/
RUN KUBECTL_VERSION="${KUBECTL_VERSION}" \
    KUBECTL_GO_VERSION="${KUBECTL_GO_VERSION}" \
    KUBECTL_SOURCE_COMMIT="${KUBECTL_SOURCE_COMMIT}" \
    KUBECTL_SOURCE_SHA256="${KUBECTL_SOURCE_SHA256}" \
    KUBECTL_BUILD_DATE="${KUBECTL_BUILD_DATE}" \
    KUBECTL_SOURCE_DATE_EPOCH="${KUBECTL_SOURCE_DATE_EPOCH}" \
    TARGETARCH="${TARGETARCH}" \
    OUTPUT_DIR=/out \
    /usr/local/bin/build-kubectl.sh
COPY scripts/verify-kubectl.sh scripts/verify-go-security.sh /usr/local/bin/
RUN KUBECTL_GO_VERSION="${KUBECTL_GO_VERSION}" \
    TARGETARCH="${TARGETARCH}" \
    /usr/local/bin/verify-kubectl.sh

FROM alpine:${ALPINE_VERSION} AS kubectl-runtime-verifier

ARG TARGETARCH
ARG KUBECTL_VERSION
ARG KUBECTL_GO_VERSION
ARG KUBECTL_SOURCE_COMMIT
ARG KUBECTL_BUILD_DATE

COPY --from=kubectl-builder /out/kubectl /out/kubectl

# Execute each target binary and verify the public client identity before it is
# copied into the final image.
RUN KUBECTL_METADATA="$(/out/kubectl version --client -o json)" && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq "\"gitVersion\": \"${KUBECTL_VERSION}\"" && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq "\"gitCommit\": \"${KUBECTL_SOURCE_COMMIT}\"" && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq '"gitTreeState": "dirty"' && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq "\"buildDate\": \"${KUBECTL_BUILD_DATE}\"" && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq "\"goVersion\": \"${KUBECTL_GO_VERSION}\"" && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq "\"platform\": \"linux/${TARGETARCH}\""

FROM openbao/openbao:${BAO_VERSION}@${BAO_RUNTIME_SHA256} AS runtime
ARG GIT_SHA
RUN test -n "${GIT_SHA}"
LABEL org.opencontainers.image.source="https://github.com/NVIDIA/nvcf" \
      org.opencontainers.image.revision="${GIT_SHA}"

# Upgrade inherited packages before installing runtime dependencies so the
# image receives fixes published after the upstream OpenBao release. Require
# the zlib fix for CVE-2026-85091 even when using a cached upstream base.
USER root
RUN apk upgrade --no-cache && \
    apk add --no-cache 'zlib>=1.3.2-r1' openssl uuidgen curl bash jq

FROM runtime
ARG TARGETARCH
COPY --from=bao-builder --chmod=0555 /out/bao-linux-${TARGETARCH} /usr/bin/bao
COPY --from=jwker-builder /out/jwker /usr/local/bin/jwker
COPY --from=kubectl-runtime-verifier /out/kubectl /usr/local/bin/kubectl

WORKDIR /app

COPY --chmod=775 migrations/ /app/migrations/
COPY --chmod=775 addons/ /app/addons/
COPY --chmod=775 entrypoint.sh /app/

# The hook scripts do not use Helm. Check the final runtime on both platforms.
RUN ! command -v helm && ! apk info --exists helm

USER 100:1000

CMD ["/app/entrypoint.sh"]
