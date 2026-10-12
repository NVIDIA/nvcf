# AGENTS.md - cassandra runtime image

Container image that runs Apache Cassandra for NVCF, built on the official
`cassandra` image. Layers a Prometheus exporter agent (supplied at build time),
`yq` for the chart's config init container, and an NCP rack-from-pod
`cassandra-env.sh`.

## Image facts

- Base: `cassandra:5.0.9` (official Apache Cassandra, Docker Hub). Bump the
  Cassandra version by editing the `FROM cassandra:<version>` line; that tag is
  the single source of truth, there is no version build-arg.
- yq: fetched in a pinned `alpine:3.21` build stage and checksum-verified. The
  chart's config init container uses it to deep-merge cassandra.yaml overrides.
- Exporter agent: not redistributed here. `files/` ships only `.gitkeep`, and the
  `Dockerfile` defaults `EXPORTER_JAR` to it, so a bare build succeeds with no
  metrics agent. To enable metrics on port 9500, supply a jar under `files/` and
  pass both `--build-arg EXPORTER_JAR=files/<jar>` and
  `--build-arg EXPORTER_JAVAAGENT=-javaagent:/opt/cassandra/lib/cassandra-exporter-agent.jar`.
  Both args are required together; the build rejects either mismatched
  combination. It replaces the jar's exact shaded Netty module set with
  checksum-pinned artifacts through `scripts/repack-exporter-netty.sh` and fails
  on an unexpected layout.
- Maven downloads default to Maven Central. To route them through a caching
  repository manager pass `--build-arg MAVEN_REPOSITORY_BASE=<base>`; the
  artifact paths from `java-libraries.lock` and the SHA-256 checks are unchanged.

## Build

```sh
# OSS build (no metrics agent)
docker build -t nvcf-cassandra:dev infra/cassandra

# multi-arch
docker buildx build --platform linux/amd64,linux/arm64 -t <ref> infra/cassandra

# download retry and checksum policy unit test (local server, no network)
infra/cassandra/scripts/fetch-verified-test.sh

# repository-base override and lock handling unit test (local server, no network)
infra/cassandra/scripts/fetch-java-libraries-test.sh

# exporter dependency unit test (downloads checksum-pinned Netty jars)
infra/cassandra/scripts/repack-exporter-netty-test.sh
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
