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
# Common builder stage (executes ONLY on the build platform)
FROM --platform=$BUILDPLATFORM golang:1.24 AS builder

# ---- Build configuration -----------------------------------------------------
ENV GOPROXY=https://proxy.golang.org,direct
ENV GOCACHE=/root/.cache/go-build
ENV CGO_ENABLED=0

WORKDIR /go/src/app

# Copy only go.mod and go.sum and run go mod download for better caching.
# We don't need new modules unless the go.mod or go.sum file is changed.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .

# The following build args are automatically populated by BuildKit.
# They are purposely set after the go mod download command so that the go mod download command is run once.
ARG TARGETOS
ARG TARGETARCH
# Multi-architecture build stage
# -buildvcs=false: skip git ls-remote VCS stamp — avoids a second auth round-trip.
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -a -buildvcs=false \
    -o /out/$TARGETARCH/app ./

# Final runtime image – one per requested TARGETPLATFORM
# BuildKit will create a final image per platform and copy the pre-built
# binary that matches the platform's architecture.
FROM urm.nvidia.com/sw-gpu-ucs-hardened-docker/distroless/go:v3.1.10

# The following build arg is automatically populated by BuildKit.
ARG TARGETARCH

# Copy the correct binary for the current TARGETARCH
COPY --from=builder /out/${TARGETARCH}/app /usr/bin/app

# enable setting the ulimit to max on boot
ENV ULIMIT_FLAG=1

# the image uses ENTRYPOINT, so do not override it
# What the container will run when it is started
CMD ["/usr/bin/app"]
