# AGENTS.md - cassandra runtime image

Container image that runs Apache Cassandra for NVCF, built on the official
`cassandra` image. Layers a Prometheus exporter agent (supplied at build time),
`yq` for the chart's config init container, and an NCP rack-from-pod
`cassandra-env.sh`.

## Image facts

- Base: `cassandra:5.0.8` (official Apache Cassandra, Docker Hub). Bump the
  Cassandra version by editing the `FROM cassandra:<version>` line; that tag is
  the single source of truth, there is no version build-arg.
- yq: fetched in a pinned `alpine:3.21` build stage and checksum-verified. The
  chart's config init container uses it to deep-merge cassandra.yaml overrides.
- Exporter agent: not redistributed here. `files/` ships only `.gitkeep`, and the
  `Dockerfile` defaults `EXPORTER_JAR` to it, so a bare build succeeds with no
  metrics agent. To enable metrics on port 9500, supply a jar under `files/` and
  pass both `--build-arg EXPORTER_JAR=files/<jar>` and
  `--build-arg EXPORTER_JAVAAGENT=-javaagent:/opt/cassandra/lib/cassandra-exporter-agent.jar`.
  Both args are required together; either alone runs without metrics.

## Build

```sh
# OSS build (no metrics agent)
docker build -t nvcf-cassandra:dev infra/cassandra

# multi-arch
docker buildx build --platform linux/amd64,linux/arm64 -t <ref> infra/cassandra
```

The `--platform=$BUILDPLATFORM` on the yq stage is intentional: it lets yq
cross-download on the host arch without QEMU.

## Bazel image (rules_oci)

This subtree is also a self-contained Bazel module that builds the same image
via `rules_oci`, so it ships and releases like every other NVCF image (no
buildah). It is a nested module: the umbrella ignores it (root `.bazelignore`
lists `infra/cassandra`), so run Bazel from this directory.

```sh
cd infra/cassandra

# Public multi-arch image (amd64 + arm64), OSS-safe (no exporter jar).
bazel build //:image_index      # or //:cassandra (non-manual alias)

# yq exec-bit guard.
bazel test //:yq_exec_bit_test
```

Translation of the Dockerfile:
- `FROM cassandra:5.0.8` -> `oci.pull` of the Docker Hub manifest-list digest
  in `MODULE.bazel`. The digest is authoritative for the built image; the
  Dockerfile tag is a transitional mirror for manual/dev builds. To bump
  Cassandra, change these together: (1) the `oci.pull` digest in `MODULE.bazel`
  (re-resolve the new tag's multi-arch manifest-list digest), (2) the
  `# cassandra-version:` marker beside it, and (3) the Dockerfile
  `FROM cassandra:<new>` tag. `cassandra_version_consistency_test` fails the
  build if the Dockerfile tag and the marker disagree.
- pinned `yq` -> `http_file` per arch in `rules/repos.bzl` (same version +
  sha256s as the Dockerfile), packaged at `/usr/local/bin/yq`.
- `scripts/cassandra-env.sh` -> `pkg_tar` layer over `/etc/cassandra/`.
- `--add-opens` JVM flags -> `JVM_EXTRA_OPTS` env, shared as `JVM_ADD_OPENS`
  in `rules/oci/defs.bzl`.

The exporter agent and NGC push destinations are internal-only and do not
live in this repo. The private release repo (nvcf-internal) owns the exporter
jar and a `nvidia-internal/` overlay package; at release time it injects that
overlay into this module (reusing the public `//:yq_layer` and `//:env_layer`
targets), adds the exporter layer plus the `-javaagent` flag, and pushes the
with-exporter image. Nothing exporter-related is declared in this OSS tree.

## Pairs with

- Helm chart `deploy/helm/cassandra` deploys this image; its config init
  container relies on the `yq` this image provides.
- Schema migrations image `migrations/cassandra`.

## cassandra-env.sh

`scripts/cassandra-env.sh` is the official Apache Cassandra `cassandra-env.sh`
modified to derive `CASSANDRA_RACK` from the pod hostname suffix for
`GossipingPropertyFileSnitch` (`-0/-3/-6 -> r1`, `-1/-4/-7 -> r2`,
`-2/-5/-8 -> r3`) and write `cassandra-rackdc.properties`. It is Apache-2.0,
derived from upstream; keep the SPDX header.
