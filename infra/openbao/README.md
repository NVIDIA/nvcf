# NVCF OpenBao

Container image used by NVCF deployments to run [OpenBao](https://openbao.org/), bundled with the additional vault plugin(s) NVCF expects at runtime.

## Overview

This repository ships:

- A multi-arch container image definition (`Dockerfile`) layered on top of `openbao/openbao`
- A checksum-pinned OpenBao source build with reviewed dependency floors
- The additional JWT secrets plugin NVCF expects at runtime

The final image keeps the upstream runtime filesystem, entrypoint, default
configuration, and `openbao` user. It replaces `/usr/bin/bao` with a binary
built from the matching official distribution source. See
`OPENBAO_PROVENANCE.md` for the source identity and dependency floors.

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

## OpenBao server binary

The server build downloads the official OpenBao 2.6.2 distribution source,
checks its SHA-256 digest, applies the reviewed Go module floors, and builds
the same UI-enabled command for Linux amd64 and arm64:

```bash
scripts/build-openbao.sh     # writes both arch binaries to files/openbao/
scripts/verify-openbao.sh    # asserts target metadata and dependency floors
```

The distribution source is used because it contains the generated web UI that
is embedded in upstream release binaries. The auto-generated GitHub source
archive does not contain those assets.

## Prerequisites

- Docker or another OCI-compatible builder (with `buildx` for multi-arch)
- Go 1.27.0 or newer when building the server or plugin outside the container
- `curl`, `tar` with xz support, and `sha256sum` or `shasum` for a local server build

## Building the container

The `Dockerfile` defaults to the digest-pinned `openbao/openbao:2.6.2` base
image and the matching checksum-pinned distribution source. A version update
must also pin the runtime digest, source commit, source checksum, and commit
date.

```bash
docker build \
  --build-arg TARGETARCH=amd64 \
  --build-arg BAO_VERSION=2.6.2 \
  -t <your-registry>/<your-org>/nvcf-openbao:<version> .
```

For multi-arch builds:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg BAO_VERSION=2.6.2 \
  -t <your-registry>/<your-org>/nvcf-openbao:<version> \
  --push .
```

## Image contents

At runtime the image provides:

- The OpenBao server at `/usr/bin/bao`, built from the upstream 2.6.2 source
- Alpine packages `curl`, `jq`, and `bash` (used by entrypoint scripts in consumers such as the migrations Job)
- `/openbao/plugins/vault-plugin-secrets-jwt` - the JWT secrets plugin built from `github.com/NVIDIA/nvcf/infra/openbao/plugins/vault-plugin-secrets-jwt`
