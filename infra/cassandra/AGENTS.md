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
