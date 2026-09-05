ARG ALPINE_VERSION=3.23
ARG GO_IMAGE=golang:1.27.0-alpine3.23@sha256:3747dcba41c8b0db3211fda4db61638b980e17ac5bb3c94460a975a9cfe19395
ARG BAO_VERSION=2.6.2
ARG BAO_SOURCE_COMMIT=dd9c19c37a878cf4a81b18efb8d6f0599c7da923
ARG BAO_SOURCE_SHA256=a7784550a9db16f24e99d65a18c9b12a433707c79ef4c1f34262d3f48171c7a9
ARG KUBECTL_VERSION=v1.37.0
ARG KUBECTL_GO_VERSION=go1.26.6

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

FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS kubectl-downloader

ARG TARGETARCH
ARG KUBECTL_VERSION
ARG KUBECTL_LINUX_AMD64_SHA256=6129359f4e1f3848a5572ccb0b26cf28b8ca08cef38c95a765b2f64a2c961a2f
ARG KUBECTL_LINUX_ARM64_SHA256=922df28df248cc00a9e025f947704f1d1482de64ece54cfe57e61f19eaf1eef3

# Verify the official checksum and the reviewed per-architecture checksum.
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
    chmod +x /out/kubectl

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS kubectl-verifier

ARG KUBECTL_VERSION
ARG KUBECTL_GO_VERSION

COPY --from=kubectl-downloader /out/kubectl /out/kubectl

# Keep the scanner-relevant toolchain floor explicit and verify the downloaded
# binary is the requested Kubernetes module before it reaches the final image.
RUN go version -m /out/kubectl >/tmp/kubectl.buildinfo && \
    grep -Fq ": ${KUBECTL_GO_VERSION}" /tmp/kubectl.buildinfo && \
    grep -Eq "^[[:space:]]*mod[[:space:]]+k8s.io/kubernetes[[:space:]]+${KUBECTL_VERSION}([[:space:]]|$)" /tmp/kubectl.buildinfo

FROM openbao/openbao:${BAO_VERSION}
ARG TARGETARCH

# Upgrade inherited packages before installing runtime dependencies so the
# image receives fixes published after the upstream OpenBao release.
USER root
RUN apk upgrade --no-cache && \
    apk add --no-cache openssl uuidgen helm curl bash jq

COPY --from=bao-builder --chmod=0555 /out/bao-linux-${TARGETARCH} /usr/bin/bao
COPY --from=jwker-builder /out/jwker /usr/local/bin/jwker
COPY --from=kubectl-verifier /out/kubectl /usr/local/bin/kubectl

WORKDIR /app

COPY --chmod=775 migrations/ /app/migrations/
COPY --chmod=775 addons/ /app/addons/
COPY --chmod=775 entrypoint.sh /app/

USER openbao

CMD ["/app/entrypoint.sh"]
