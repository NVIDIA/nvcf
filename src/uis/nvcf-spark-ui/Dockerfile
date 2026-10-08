# syntax=docker/dockerfile:1
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# The BFF plus the built UI on distroless, laid out like nvcf-ui's Bazel
# go_web_oci_image (/app/server, /app/static), which replaces this file at
# port time. Both builds run on the build machine's own platform and only
# their output is copied onto the target platform's base, so an arm64 image
# for DGX Spark builds on an amd64 laptop without emulation:
#
#   docker buildx build --platform linux/arm64 -t nvcf-spark-ui:dev .
#
# Base images are pinned by digest; the distroless one is the monorepo's.

FROM --platform=$BUILDPLATFORM node:22-bookworm-slim@sha256:c3de60bf2f9dd0ac6370e6117950ff62d6e339527e7472301c9c78a017978392 AS ui
WORKDIR /src/ui
RUN corepack enable
COPY ui/package.json ui/pnpm-lock.yaml ui/pnpm-workspace.yaml ./
# The prepare script syncs the KUI agent skill into ../.claude, which the build
# doesn't need; everything else, including the allowed build scripts, runs.
RUN COREPACK_ENABLE_DOWNLOAD_PROMPT=0 pnpm install --frozen-lockfile \
    && rm -rf ../.claude
COPY ui/ ./
RUN pnpm build --outDir /out/static

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm@sha256:dc9ad6c05acc7a88e5b71bde60a5fe3bd4b9f0db209011711b464107438a8107 AS server
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static@sha256:9197324ba51d9cd071af8505989365c006adf9d6d2067eada25aef00abbb5278
WORKDIR /app
COPY --from=server /out/server /app/server
COPY --from=ui /out/static /app/static
# distroless's "nonroot" user; the chart also requires it (runAsNonRoot).
USER 65532:65532
EXPOSE 8300
ENTRYPOINT ["/app/server"]
