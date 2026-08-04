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

The plugin is built from source in this repository, at
`plugins/vault-plugin-secrets-jwt`. Nothing needs to be cloned or placed by
hand, and no binaries are committed.

The image build compiles it in a Dockerfile build stage, so `docker build .`
here produces the same image as the release pipeline:

```bash
docker build --build-arg TARGETARCH=amd64 -t nvcf-openbao:local .
```

To produce the binaries outside an image build, for local inspection or to run
the verifier:

```bash
scripts/build-jwt-plugin.sh     # writes both arch binaries to files/plugins/
scripts/verify-jwt-plugin.sh    # asserts module path, target, toolchain, deps
```

`files/plugins/` is gitignored apart from `.gitkeep`; see
`files/plugins/PROVENANCE.md` for the dependency floors the verifier enforces.

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
