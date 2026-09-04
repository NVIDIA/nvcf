ARG ALPINE_VERSION=3.23
ARG GO_IMAGE=golang:1.27.0-alpine3.23@sha256:3747dcba41c8b0db3211fda4db61638b980e17ac5bb3c94460a975a9cfe19395

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
ARG KUBECTL_VERSION=v1.36.4
ARG KUBECTL_LINUX_AMD64_SHA256=8b8f088da2dab964f853b38464033b1be15ede2839eca751482357c45abdd05a
ARG KUBECTL_LINUX_ARM64_SHA256=0ecf44450ee6063bf19dd166a103ee6df4a9034455c2abce626e6eea657d73fb

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

FROM openbao/openbao:2.5.5

# Install runtime dependencies and copy verified tools from downloader stages.
RUN apk add --no-cache openssl uuidgen helm curl bash jq

COPY --from=jwker-builder /out/jwker /usr/local/bin/jwker
COPY --from=kubectl-downloader /out/kubectl /usr/local/bin/kubectl

WORKDIR /app

COPY --chmod=775 migrations/ /app/migrations/
COPY --chmod=775 addons/ /app/addons/
COPY --chmod=775 entrypoint.sh /app/

CMD ["/app/entrypoint.sh"]
