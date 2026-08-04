# NVCF OpenBao

Container image used by NVCF deployments to run [OpenBao](https://openbao.org/), bundled with the additional vault plugin(s) NVCF expects at runtime.

## Overview

This repository ships:

- A multi-arch container image definition (`Dockerfile`) layered on top of `openbao/openbao`
- A directory (`files/plugins/`) where the user supplies the vault plugin binary at build time

## Plugin binaries

The image expects an OS-specific plugin binary at build time, placed at:

- `files/plugins/vault-plugin-secrets-jwt-linux-amd64` (for `--platform linux/amd64`)
- `files/plugins/vault-plugin-secrets-jwt-linux-arm64` (for `--platform linux/arm64`)

Build a compatible `vault-plugin-secrets-jwt` plugin for each target architecture, place the resulting binary at the path above, and ensure it is executable. For example:

```bash
git clone https://github.com/outfoxx/vault-plugin-secrets-jwt
cd vault-plugin-secrets-jwt

# amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -o ../files/plugins/vault-plugin-secrets-jwt-linux-amd64 ./cmd/vault-plugin-secrets-jwt
chmod +x ../files/plugins/vault-plugin-secrets-jwt-linux-amd64

# arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build \
  -o ../files/plugins/vault-plugin-secrets-jwt-linux-arm64 ./cmd/vault-plugin-secrets-jwt
chmod +x ../files/plugins/vault-plugin-secrets-jwt-linux-arm64
```

## Prerequisites

- Docker or another OCI-compatible builder (with `buildx` for multi-arch)
- A built copy of `vault-plugin-secrets-jwt` for each platform you target, placed in `files/plugins/`

## Building the container

The `Dockerfile` defaults to the `openbao/openbao:2.5.5` base image. Override the `BAO_VERSION` build-arg to track a different upstream tag.

```bash
docker build \
  --build-arg TARGETARCH=amd64 \
  --build-arg BAO_VERSION=2.5.5 \
  -t <your-registry>/<your-org>/nvcf-openbao:<version> .
```

For multi-arch builds:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg BAO_VERSION=2.5.5 \
  -t <your-registry>/<your-org>/nvcf-openbao:<version> \
  --push .
```

## Image contents

At runtime the image provides:

- The upstream OpenBao server (`/usr/local/bin/bao`)
- Alpine packages `curl`, `jq`, and `bash` (used by entrypoint scripts in consumers such as the migrations Job)
- `/openbao/plugins/vault-plugin-secrets-jwt` - the JWT secrets plugin built from `outfoxx/vault-plugin-secrets-jwt`
