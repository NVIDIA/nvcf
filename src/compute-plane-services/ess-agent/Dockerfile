# SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0
# ===================================
#
#   Multi-stage build: compile + release
#
# ===================================
#
# Not a contribution
# Changes made by NVIDIA CORPORATION & AFFILIATES enabling ESS agent rebrand and packaging or otherwise documented as
# NVIDIA-proprietary are not a contribution and subject to the following terms and conditions:
# <NVIDIA-proprietary license from NVIDIA Proprietary - Legal - Confluence>

# --- stage 1: build the binary ---
FROM golang:1.25.6-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
COPY api/ api/
COPY sdk/ sdk/
RUN go mod download
COPY . .

ARG BIN_NAME=ess-agent
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -o /bin/${BIN_NAME} .

# --- stage 2: minimal release image ---
FROM alpine:latest AS release-default

ARG BIN_NAME=ess-agent
ENV BIN_NAME=$BIN_NAME
ARG PRODUCT_VERSION
ARG PRODUCT_REVISION
ARG PRODUCT_NAME=$BIN_NAME

LABEL version=$PRODUCT_VERSION
LABEL revision=$PRODUCT_REVISION

ARG UID=100
ARG GID=1000
RUN addgroup -g ${GID} ${BIN_NAME} \
    && adduser -u ${UID} -S -G ${BIN_NAME} ${BIN_NAME}

COPY --from=builder /bin/${BIN_NAME} /bin/${BIN_NAME}

COPY ./.release/docker-entrypoint.sh /bin/
ENTRYPOINT ["/bin/docker-entrypoint.sh"]

USER ${BIN_NAME}:${BIN_NAME}
CMD ["/bin/ess-agent"]
