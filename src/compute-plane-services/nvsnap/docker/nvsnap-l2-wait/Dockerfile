# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Dockerfile for the nvsnap-l2-wait init container (nvsnap#147).
#
# This container is injected by the mutating webhook onto restore
# pods. It polls nvsnap-server's pvc-state endpoint until the L2
# promote reaches a terminal state, then exits — gating the
# inference container on rox-<hash> PVC readiness.
#
# Image size goal: < 15 MB. Justification: this container runs on
# EVERY restore pod (potentially N replicas per function); shaving
# pull time matters for restore-from-cold latency. A scratch base
# + a static binary + no shell delivers that.
#
# Build (from repo root):
#   docker build -t nvsnap-l2-wait:VERSION -f docker/nvsnap-l2-wait/Dockerfile .

# Stage 1: build a static, stripped binary. CGO disabled so the
# resulting binary has no glibc dependency — scratch-runnable.
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
        -trimpath \
        -ldflags='-s -w -extldflags=-static' \
        -o /out/nvsnap-l2-wait \
        ./cmd/nvsnap-l2-wait/

# Stage 2: scratch + ca-certificates. We talk to nvsnap-server which
# in production is in-cluster (no TLS) but operators may proxy it
# behind a TLS-terminating ingress in some deployments — the certs
# bundle is 250 KB and worth carrying for that flexibility.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/nvsnap-l2-wait /nvsnap-l2-wait

# Sensible defaults; the webhook overrides via flags at injection
# time so per-pod config doesn't need a redeploy of this image.
ENV NVSNAP_POLL_INTERVAL=2s \
    NVSNAP_MAX_POLL_INTERVAL=30s \
    NVSNAP_WAIT_TIMEOUT=15m \
    NVSNAP_ALLOW_EMPTY_STATE=false

# Run as a non-root unprivileged user. The webhook sets the user/
# group on the pod's securityContext to match the inference
# container's, so this is just defense-in-depth — even if the
# webhook misconfigures, scratch UIDs default to 0 which kube admission
# may reject under PodSecurityStandards "restricted".
USER 65532:65532

ENTRYPOINT ["/nvsnap-l2-wait"]
