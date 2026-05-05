# ------------------------------------------------------------------------------
# Common builder stage (executes ONLY on the build platform)
# ------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26.2-bookworm AS builder-base

# ---- Build configuration -----------------------------------------------------
ARG EGX_VERSION
ARG EGX_GITHASH
ARG EGX_WORKING_DIR_DIRTY
ARG NVCF_IMAGE_CREDENTIAL_HELPER_RELEASE_TAG
ARG NVCF_GOMAXPROCS=4
# Comma-separated list of GOARCH values to build for. Extend when adding
# additional target platforms.
ARG TARGETARCHS="amd64 arm64"

ENV CGO_ENABLED=0
ENV NVCF_GO_LD_FLAGS="-X main.Version=${EGX_VERSION} -X main.GitHash=${EGX_GITHASH} -extldflags \"-static\" -w -s"

WORKDIR /app

# Copy sources
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY cmd ./cmd
COPY credhelper ./credhelper

# ------------------------------------------------------------------------------
# Builder stage – compile image-credential-helper binary for all TARGETARCHS
# ------------------------------------------------------------------------------
FROM builder-base AS builder
RUN set -eux; \
    mkdir -p /out; \
    for arch in $(echo "${TARGETARCHS}" | tr ',' ' '); do \
        echo ":: building image-credential-helper for $arch"; \
        CGO_ENABLED=0 GOOS=linux GOARCH=${arch} \
            go build -a -mod=vendor -p "${NVCF_GOMAXPROCS}" \
            -ldflags "${NVCF_GO_LD_FLAGS}" \
            -o /out/${arch}/image-credential-helper ./cmd/image-credential-helper; \
    done

# ------------------------------------------------------------------------------
# Final runtime image – one per requested TARGETPLATFORM
# BuildKit will create a final image per platform and copy the pre-built
# binary that matches the platform's architecture.
# ------------------------------------------------------------------------------

# This distroless image already provides a non-root runtime user, so do not add a USER override here unless
# the runtime requirements change.
FROM nvcr.io/nvidia/distroless/go:v4.0.4
LABEL io.k8s.display-name="NVCF Image Credential Helper"

# The following build args are automatically populated by BuildKit.
ARG TARGETARCH

# Copy the correct binaries for the current TARGETARCH
COPY --from=builder /out/${TARGETARCH}/image-credential-helper /usr/bin/image-credential-helper

# Optionally ship the vendored sources for license inspection
COPY --from=builder /app/vendor/ /usr/pkgsrc/
