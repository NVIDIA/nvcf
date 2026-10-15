# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# nvsnap-blobstore Dockerfile — minimal Go binary, distroless runtime.
#
# Build context is the repo root: `docker build -f docker/nvsnap-blobstore/Dockerfile .`

# Stage 1: Go binary
FROM golang:1.25-bookworm AS go-builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o nvsnap-blobstore ./cmd/nvsnap-blobstore

# Stage 2: Runtime — distroless. The blobstore needs a writable
# /data PVC and that's it; no shell, no certs, no userland tools.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go-builder /workspace/nvsnap-blobstore /nvsnap-blobstore
EXPOSE 9000
USER 65532:65532
ENTRYPOINT ["/nvsnap-blobstore"]
