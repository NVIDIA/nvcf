# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG SOURCE_REVISION=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOWORK=off GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-X github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/version.GitHash=${SOURCE_REVISION}" \
    -o /out/pylon-operator ./cmd/pylon-operator
FROM nvcr.io/nvidia/distroless/go:v4.1.2
COPY --from=builder --chmod=755 /out/pylon-operator /usr/bin/pylon-operator
ENTRYPOINT ["/usr/bin/pylon-operator"]
