# syntax=docker/dockerfile:1.6
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# ReVal server image.
#
# Some dependencies (github.com/NVIDIA/*) are private and require a GitHub token.
# Pass it as a BuildKit secret so it never appears in image layers:
#
#   DOCKER_BUILDKIT=1 docker build \
#     --secret id=github_token,src=<(echo $GITHUB_TOKEN) \
#     -f docker/Dockerfile -t reval-server:local .

FROM golang:1.25.7-bookworm AS builder

ARG REVAL_VERSION=dev
ARG REVAL_GIT_COMMIT=dev
ARG TARGETARCH=amd64

ENV CGO_ENABLED=0
ENV GOPRIVATE="github.com/NVIDIA"
ENV GOPROXY=https://proxy.golang.org,direct
WORKDIR /app

COPY go.mod go.sum ./
# TODO: remove secret mount and git credential rewrite once this repo is open-sourced —
# at that point this becomes a plain `RUN go mod download`.
RUN --mount=type=secret,id=github_token \
    GITHUB_TOKEN=$(cat /run/secrets/github_token) && \
    git config --global url."https://oauth2:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/" && \
    go mod download

COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
COPY reval/ ./reval/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
	go build \
	-ldflags "-X main.Version=${REVAL_VERSION} -X main.GitCommit=${REVAL_GIT_COMMIT} -extldflags '-static' -w -s" \
	-o /reval ./cmd/reval-service

FROM gcr.io/distroless/static-debian12:nonroot

ARG REVAL_VERSION=dev

COPY --from=builder /reval /usr/bin/reval

ENV REVAL_TELEMETRY_SERVICE_VERSION=${REVAL_VERSION}
ENV REVAL_CONFIG="/config.yaml"

USER nonroot:nonroot
ENTRYPOINT ["/usr/bin/reval"]
