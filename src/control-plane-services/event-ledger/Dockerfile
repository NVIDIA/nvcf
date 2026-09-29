# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

ARG GO_VERSION=1.26.1
FROM golang:${GO_VERSION} AS builder

ENV CGO_ENABLED=0
WORKDIR /src

COPY go.mod go.sum ./
COPY common/go.mod common/go.sum ./common/
RUN go mod download

COPY . .
RUN go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/event-ledger-api ./cmd/api

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/event-ledger-api /event-ledger-api

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/event-ledger-api"]
