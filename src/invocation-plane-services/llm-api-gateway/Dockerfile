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
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-mod=mod \
    go build -o /out/$TARGETARCH/llm-api-gateway ./cmd/llm-api-gateway

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-mod=mod \
    go build -o /out/$TARGETARCH/llm-api-gateway-rate-limit-sync-worker ./cmd/llm-api-gateway-rate-limit-sync-worker

FROM nvcr.io/nvidia/distroless/go:v4.0.8

ARG TARGETARCH

COPY --from=builder /out/${TARGETARCH}/llm-api-gateway /usr/bin/llm-api-gateway
COPY --from=builder /out/${TARGETARCH}/llm-api-gateway-rate-limit-sync-worker /usr/bin/llm-api-gateway-rate-limit-sync-worker

EXPOSE 8080 9464

CMD ["/usr/bin/llm-api-gateway"]
