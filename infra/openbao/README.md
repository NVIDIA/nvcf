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

## Releases

Release-worthy changes under `infra/openbao` publish the `nvcf-openbao`
image. GitHub source releases use tags in the `infra/openbao/vX.Y.Z` stream.
Each release rebuilds both supported architectures from the source and
dependency pins in this directory.

## Plugin binaries

The plugin is built from source in this repository, at
`plugins/vault-plugin-secrets-jwt`. Nothing needs to be cloned or placed by
hand, and no binaries are committed.

The bridge also copies the exact legacy plugin from a digest-pinned published
image matching the chart's published pre-bridge baseline.
`jwt-plugin-versions.env` records its explicit old and target catalog
versions. Do not rebuild the legacy source or overwrite an existing target
version with a different binary. Bump the target version for changes to source,
dependencies, toolchain, or build flags.

The image writes `/openbao/plugin-config/jwt.hcl` from the packaged binaries,
including their architecture-specific SHA-256 values. The image entrypoint
adds `-config=/openbao/plugin-config` alongside the normal server config.
Custom commands that bypass that entrypoint must supply the argument.
All members in one OpenBao cluster must use the same CPU architecture because
catalog digests are cluster-wide.

Use [the JWT plugin upgrade procedure](../../docs/self-managed/runbooks/openbao-jwt-plugin-upgrade.md)
for bridge rollout, version pinning, rollback, and later cleanup. The bridge
keeps old binaries by default. A later qualified release can build with
`--build-arg JWT_INCLUDE_LEGACY=false` after the metadata gate passes. Keep
the target source and build inputs unchanged so its digest remains identical.
The bridge still includes the old Go 1.27.0 plugin for existing catalogs.
Do not treat that bridge image as complete removal of vulnerable binaries.
After all mounts use the target version, verify a target-only image with
`EXPECT_TARGET_ONLY=true RUNTIME_IMAGE=<image> sh scripts/test-runtime-packages.sh`.

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

## Upgrading to OpenBao 2.6.3

This source update addresses the security advisories in the
[OpenBao 2.6.3 release notes](https://github.com/openbao/openbao/releases/tag/v2.6.3).
The JWT plugin source and dependency pins are unchanged. The default NVCF
configuration needs no HCL, policy, JWT role, or agent-template changes for
the 2.6.2-to-2.6.3 upgrade.

The OpenBao runtime version, NVCF image release, chart version, and stack
version are separate. Publish the server and migrations images before updating
chart references. Keep the chart's server and injected-agent image tags aligned;
update the migrations image tag from its own release stream.

The chart uses `OnDelete`: applying new values does not replace existing
server pods or agents already injected into application pods. Follow the
[OpenBao patch upgrade runbook](../../docs/self-managed/runbooks/openbao-patch-upgrade.md)
when the target stack release includes the new images.

## OpenBao server binary

The server build downloads the official OpenBao 2.6.3 distribution source,
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
- Go 1.27.3 or newer when building the server or plugin outside the container
- `curl`, `tar` with xz support, and `sha256sum` or `shasum` for a local server build

## Building the container

The `Dockerfile` defaults to the digest-pinned `openbao/openbao:2.6.3` base
image and the matching checksum-pinned distribution source. A version update
must also pin the runtime digest, source commit, source checksum, and commit
date.

```bash
docker build \
  --build-arg TARGETARCH=amd64 \
  --build-arg BAO_VERSION=2.6.3 \
  -t <your-registry>/<your-org>/nvcf-openbao:<version> .
```

For multi-arch builds:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg BAO_VERSION=2.6.3 \
  -t <your-registry>/<your-org>/nvcf-openbao:<version> \
  --push .
```

## Runtime package verification

The final Alpine stage requires `brotli-libs>=1.2.1-r0`,
`ca-certificates>=20261020-r0`, and
`ca-certificates-bundle>=20261020-r0`. These package requirements also
invalidate the earlier Docker package layer. The engine and plugin source pins
are unchanged; a source rebuild can still change embedded metadata and the
plugin hash stored in an existing catalog.

Run `sh scripts/test-runtime-packages.sh` for the source check. Set
`RUNTIME_IMAGE` to test a built image, and optionally `BASELINE_IMAGE` to
require identical legacy JWT plugin bytes. The rebuilt server has a different
hash. Repeat for both architectures. Persistent-state rollout and rollback
qualification remain required before consuming a published image.

## Image contents

At runtime the image provides:

- The OpenBao server at `/usr/bin/bao`, built from the upstream 2.6.3 source
- Alpine packages `curl`, `jq`, and `bash` (used by entrypoint scripts in consumers such as the migrations Job)
- `/openbao/plugins/vault-plugin-secrets-jwt` - the exact legacy plugin retained through the rollback window
- `/openbao/plugins/vault-plugin-secrets-jwt-v<version>` - explicit old and target commands from the version manifest
- `/openbao/plugin-config/jwt.hcl` - declarative version registration with binary hashes
- `/usr/local/bin/jwt-plugin-lifecycle` - state checks, explicit mount tuning, and gated catalog cleanup

## Targeted Raft leadership transfer

Servers built from this source expose an operator endpoint for checking each
member after a JWT plugin transition. Older images do not have this endpoint;
use a published, qualified server image that includes it before changing an
upgrade procedure.

```bash
bao write sys/storage/raft/transfer-leadership server_id=<existing-voter-id>
```

The caller needs `update` and `sudo` on this path in the root namespace.
The target must be a different, healthy voter in current Autopilot state.
Raft catches it up before requesting an election. Unknown, non-voting and
unhealthy targets are rejected.
The request never changes membership or falls back to a different target.
Raft's transfer wait uses its configured election timeout. A timed-out transfer
may still complete; recheck leadership before further actions. An HTTP success
means the Raft transfer completed, not that OpenBao active setup is ready.

Poll the target's local `sys/leader` response for `is_self: true`, then require
healthy Autopilot state and unchanged voters before issuing a JWT. Stop on a
failed request or unmet checks. A standby response can be forwarded and does
not verify that member's running plugin. Retain the existing per-member
version, SHA and JWT signature checks at every transition.
