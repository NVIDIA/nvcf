ARG ALPINE_VERSION=3.23
ARG GO_IMAGE=golang:1.27.0-alpine3.23@sha256:3747dcba41c8b0db3211fda4db61638b980e17ac5bb3c94460a975a9cfe19395
ARG KUBECTL_GO_IMAGE=golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406
ARG BAO_VERSION=2.6.2
ARG BAO_RUNTIME_SHA256=sha256:11fd73a2102cda9c55d5d881a8c3210303146a7ec1e8ac76f526e175c6d24641
ARG BAO_SOURCE_COMMIT=dd9c19c37a878cf4a81b18efb8d6f0599c7da923
ARG BAO_SOURCE_SHA256=a7784550a9db16f24e99d65a18c9b12a433707c79ef4c1f34262d3f48171c7a9
ARG KUBECTL_VERSION=v1.36.4
ARG KUBECTL_GO_VERSION=go1.26.6
ARG KUBECTL_SOURCE_COMMIT=bb826b1d48562f110659e64e8ec444327433db95
ARG KUBECTL_SOURCE_SHA256=3c28f11492472df48e658551bf268fd92938b127b0f9dcef7090ac800318c821
ARG KUBECTL_BUILD_DATE=2026-08-20T03:09:25Z
ARG KUBECTL_SOURCE_DATE_EPOCH=1787195365

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS bao-builder

ARG BAO_VERSION
ARG BAO_SOURCE_COMMIT
ARG BAO_SOURCE_SHA256
ARG TARGETARCH

RUN apk add --no-cache curl xz
COPY scripts/build-openbao.sh scripts/verify-openbao.sh /usr/local/bin/
RUN BAO_VERSION="${BAO_VERSION}" \
    BAO_SOURCE_COMMIT="${BAO_SOURCE_COMMIT}" \
    BAO_SOURCE_SHA256="${BAO_SOURCE_SHA256}" \
    TARGETARCH="${TARGETARCH}" \
    OUTPUT_DIR=/out \
    /usr/local/bin/build-openbao.sh

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS jwker-builder

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
        -o /out/jwker ./cmd/jwker

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
COPY scripts/build-kubectl.sh scripts/verify-kubectl.sh /usr/local/bin/
RUN KUBECTL_VERSION="${KUBECTL_VERSION}" \
    KUBECTL_GO_VERSION="${KUBECTL_GO_VERSION}" \
    KUBECTL_SOURCE_COMMIT="${KUBECTL_SOURCE_COMMIT}" \
    KUBECTL_SOURCE_SHA256="${KUBECTL_SOURCE_SHA256}" \
    KUBECTL_BUILD_DATE="${KUBECTL_BUILD_DATE}" \
    KUBECTL_SOURCE_DATE_EPOCH="${KUBECTL_SOURCE_DATE_EPOCH}" \
    TARGETARCH="${TARGETARCH}" \
    OUTPUT_DIR=/out \
    /usr/local/bin/build-kubectl.sh && \
    KUBECTL_GO_VERSION="${KUBECTL_GO_VERSION}" \
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
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq '"gitTreeState": "clean"' && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq "\"buildDate\": \"${KUBECTL_BUILD_DATE}\"" && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq "\"goVersion\": \"${KUBECTL_GO_VERSION}\"" && \
    printf '%s\n' "${KUBECTL_METADATA}" | grep -Fq "\"platform\": \"linux/${TARGETARCH}\""

FROM openbao/openbao:${BAO_VERSION}@${BAO_RUNTIME_SHA256}
ARG TARGETARCH

# Upgrade inherited packages before installing runtime dependencies so the
# image receives fixes published after the upstream OpenBao release.
USER root
RUN apk upgrade --no-cache && \
    apk add --no-cache openssl uuidgen helm curl bash jq

COPY --from=bao-builder --chmod=0555 /out/bao-linux-${TARGETARCH} /usr/bin/bao
COPY --from=jwker-builder /out/jwker /usr/local/bin/jwker
COPY --from=kubectl-runtime-verifier /out/kubectl /usr/local/bin/kubectl

WORKDIR /app

COPY --chmod=775 migrations/ /app/migrations/
COPY --chmod=775 addons/ /app/addons/
COPY --chmod=775 entrypoint.sh /app/

USER openbao

CMD ["/app/entrypoint.sh"]
