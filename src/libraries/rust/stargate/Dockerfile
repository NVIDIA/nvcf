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

ARG GRPC_HEALTH_PROBE_VERSION=v0.4.53

##########################################
##### COMMON BUILDER STAGE (BUILDPLATFORM)
##########################################
# Pinned alongside rust-toolchain.toml (1.94.0 stable, 2026-03-05).
FROM --platform=$BUILDPLATFORM rust:1.97-bookworm AS builder-base

ARG TARGETARCH

WORKDIR /usr/src/stargate

# Needed for protobuf/capnp codegen and Rust cross-compilation.
RUN set -eux; \
    packages="build-essential ca-certificates capnproto protobuf-compiler"; \
    case "${TARGETARCH}" in \
        amd64) packages="${packages} crossbuild-essential-amd64" ;; \
        arm64) packages="${packages} crossbuild-essential-arm64" ;; \
        *) echo "unsupported TARGETARCH '${TARGETARCH}'" >&2; exit 1 ;; \
    esac; \
    apt-get update; \
    apt-get install -y --no-install-recommends ${packages}; \
    rm -rf /var/lib/apt/lists/*

ENV CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_LINKER=x86_64-linux-gnu-gcc
ENV CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc

# Toolchain (stable + components) must be present before the first `cargo build`.
COPY rust-toolchain.toml Cargo.toml Cargo.lock ./
RUN set -eux; \
    rustup show; \
    case "${TARGETARCH}" in \
        amd64) rustup target add x86_64-unknown-linux-gnu ;; \
        arm64) rustup target add aarch64-unknown-linux-gnu ;; \
        *) echo "unsupported TARGETARCH '${TARGETARCH}'" >&2; exit 1 ;; \
    esac

COPY crates ./crates

##########################################
##### BINARY BUILDER STAGE (ALL TARGETS)
##########################################
FROM --platform=$BUILDPLATFORM builder-base AS binary-builder

# Production runtime targets keep release artifacts unless a caller such as
# Tilt explicitly selects the faster behavior-testing profile.
ARG CARGO_PROFILE=release

# Generated schema outputs in the cached target dir must not race concurrent
# Tilt/CI image builds using different source revisions.
RUN --mount=type=bind,source=.,target=/build-context,readonly \
    --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/usr/src/stargate/target,sharing=locked \
    set -eux; \
    if [ -d /build-context/.git ]; then \
        ln -s /build-context/.git .git; \
    fi; \
    case "${TARGETARCH}" in \
        amd64) rust_target=x86_64-unknown-linux-gnu ;; \
        arm64) rust_target=aarch64-unknown-linux-gnu ;; \
        *) echo "unsupported TARGETARCH '${TARGETARCH}'" >&2; exit 1 ;; \
    esac; \
    echo ":: building stargate binaries for ${TARGETARCH} (${rust_target}) with profile ${CARGO_PROFILE}"; \
    touch crates/proto/proto/*.proto crates/protocol/quic.capnp; \
    mkdir -p /out; \
    cargo build -p stargate -p stargate-k8s-router -p mock-dynamo -p pylon \
      --profile "${CARGO_PROFILE}" --target "${rust_target}"; \
    cp "target/${rust_target}/${CARGO_PROFILE}/stargate" \
       "target/${rust_target}/${CARGO_PROFILE}/stargate-worker-auth-gateway" \
       "target/${rust_target}/${CARGO_PROFILE}/stargate-k8s-router" \
       "target/${rust_target}/${CARGO_PROFILE}/mock-dynamo" \
       "target/${rust_target}/${CARGO_PROFILE}/pylon" \
       /out/; \
    rm -f .git

##########################################
##### GRPC HEALTH PROBE (BUILDPLATFORM)
##########################################
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS health-probe-downloader

ARG TARGETARCH
ARG GRPC_HEALTH_PROBE_VERSION

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

RUN curl -sSfL -o /usr/local/bin/grpc_health_probe \
      "https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-linux-${TARGETARCH}" \
    && chmod +x /usr/local/bin/grpc_health_probe

#########################
### BASE RUNTIME STAGE ##
#########################
FROM nvcr.io/nvidia/distroless/cc:v4.0.8 AS runtime-base

################################
### TILT INTEGRATION RUNTIME ###
################################
FROM runtime-base AS integration-runtime

COPY --from=health-probe-downloader /usr/local/bin/grpc_health_probe /usr/local/bin/grpc_health_probe
COPY --from=binary-builder /out/ /usr/local/bin/

ENTRYPOINT []
CMD []

############################
### STARGATE RUNTIME #######
############################
FROM runtime-base AS stargate-runtime
ARG TARGETARCH

# Health probe changes less often than the binary, so copy it first for layer caching.
COPY --from=health-probe-downloader /usr/local/bin/grpc_health_probe /usr/local/bin/grpc_health_probe
COPY --from=binary-builder /out/stargate /usr/local/bin/stargate

ENTRYPOINT ["stargate"]
CMD []

####################################
### STARGATE K8S ROUTER RUNTIME ####
####################################
FROM runtime-base AS stargate-k8s-router-runtime
ARG TARGETARCH

COPY --from=binary-builder /out/stargate-k8s-router /usr/local/bin/stargate-k8s-router

ENTRYPOINT ["stargate-k8s-router"]
CMD []

####################################
### STARGATE CLIENT RUNTIME ########
####################################
FROM runtime-base AS pylon-runtime
ARG TARGETARCH

# Health probe changes less often than the binary, so copy it first for layer caching.
COPY --from=health-probe-downloader /usr/local/bin/grpc_health_probe /usr/local/bin/grpc_health_probe
COPY --from=binary-builder /out/pylon /usr/local/bin/pylon

ENTRYPOINT ["pylon"]
CMD []

############################
### MOCK DYNAMO RUNTIME ####
############################
FROM runtime-base AS mock-dynamo-runtime
ARG TARGETARCH

# Health probe changes less often than the binary, so copy it first for layer caching.
COPY --from=health-probe-downloader /usr/local/bin/grpc_health_probe /usr/local/bin/grpc_health_probe
COPY --from=binary-builder /out/mock-dynamo /usr/local/bin/mock-dynamo

ENTRYPOINT ["mock-dynamo"]
CMD []
