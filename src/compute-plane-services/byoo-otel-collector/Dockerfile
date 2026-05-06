#####
### Stage 1: Build binaries for byoo-otel-collector
#####

FROM --platform=$BUILDPLATFORM golang:1.26.2-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH

ENV CGO_ENABLED=0

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -a -o /app/bin/byoo-otel-collector ./cmd/byoo-otel-collector

####
### Stage 2: Build OpenTelemetry Collector
####
FROM --platform=$BUILDPLATFORM golang:1.26.2-bookworm AS builder-otelcol

ARG TARGETOS=linux
ARG TARGETARCH
ARG OTEL_BUILDER_VERSION=v0.147.0

ENV CGO_ENABLED=0

WORKDIR /app

COPY ./otel-collector-build.yaml ./otel-collector-build.yaml
RUN go install go.opentelemetry.io/collector/cmd/builder@${OTEL_BUILDER_VERSION}
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} builder --config=./otel-collector-build.yaml

####
### Stage 3: Create the final Distroless image for byoo-otel-collector
####
FROM nvcr.io/nvidia/distroless/go:v4.0.4 AS byoo-otel-collector

WORKDIR /app

COPY --from=builder /app/bin/byoo-otel-collector .
COPY --from=builder-otelcol /app/output/otelcol-contrib .

EXPOSE 14357/tcp 14358/tcp 13133/tcp 18888/tcp 19090/tcp

ENTRYPOINT ["/app/byoo-otel-collector"]
