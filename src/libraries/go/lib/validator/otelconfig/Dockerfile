# syntax=docker/dockerfile:1

FROM golang:1.22 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/validate ./cmd/validate

FROM nvcr.io/nvidia/distroless/go:v4.0.3
WORKDIR /app
ENV GOLDEN_DIR=/golden
COPY --from=builder /out/validate /usr/local/bin/validate
COPY validator/validator-config.yaml /app/validator-config.yaml
COPY validator/golden/ /golden/
ENTRYPOINT ["/usr/local/bin/validate"]
