# NVCF Cassandra Base Image

Container image used by NVCF deployments to run Apache Cassandra. Layers a Prometheus metrics agent and NCP-specific rack configuration on top of the official Apache Cassandra image.

## Overview

This repository ships:

- A container image definition (`Dockerfile`) on top of the official `cassandra` image from Docker Hub
- A directory (`files/`) where you supply the Prometheus exporter agent jar at build time
- A modified copy of the official `cassandra-env.sh` (`scripts/cassandra-env.sh`) that derives a Cassandra rack name from the pod's hostname suffix, useful when the image runs in a `StatefulSet` and you want `GossipingPropertyFileSnitch` rack assignments without a separate config layer

Once built, the image:

- Runs Apache Cassandra at the version pinned by the `FROM cassandra:<version>` line in the `Dockerfile`
- Wires a Prometheus exporter Java agent into `JVM_EXTRA_OPTS` and exposes port `9500` for scraping, when an exporter jar is supplied
- Adds the `--add-opens` JVM flags the exporter agent needs to reflect into JDK-internal `com.sun.jmx` classes on the JDK 17 runtime shipped in the official image

## Exporter agent jar

The image expects a Prometheus exporter Java agent jar at build time. This repository does not redistribute the jar: the OSS snapshot ships only `files/.gitkeep` so the directory exists, and the `Dockerfile` defaults `ARG EXPORTER_JAR` to that placeholder.

A plain `docker build` with no build args succeeds, but the resulting image runs without metrics: no exporter agent is loaded, and port `9500` has nothing listening on it. Cassandra itself starts normally in this mode; only the metrics agent is absent. The `-javaagent` flag that loads the agent is controlled separately by `ARG EXPORTER_JAVAAGENT`, which also defaults to empty, so a bare build never points the JVM at the empty `.gitkeep` placeholder.

To build with the exporter enabled, place your own compatible jar under `files/` and pass both build args:

```bash
cp /path/to/your-exporter-agent.jar files/
docker build \
  --build-arg EXPORTER_JAR=files/your-exporter-agent.jar \
  --build-arg EXPORTER_JAVAAGENT="-javaagent:/opt/cassandra/lib/cassandra-exporter-agent.jar" \
  -t <your-registry>/<your-org>/nvcf-cassandra:<version> .
```

Supplying only `EXPORTER_JAR` without `EXPORTER_JAVAAGENT` copies the jar into the image but never loads it; the image still runs without metrics. Both build args must be set together to get a working exporter.

The jar must expose the same metrics interface the image's `JVM_EXTRA_OPTS` javaagent wiring expects (see `Dockerfile`). If you change the jar, update the `EXPORTER_JAR` build-arg accordingly; the source path and filename are entirely up to you.

## Prerequisites

- Docker or another OCI-compatible builder (with `buildx` for multi-arch)
- Optional: a Prometheus exporter agent jar under `files/`, if you want metrics

## Building the container

A bare build with no build args succeeds and uses the official `cassandra` base pinned in the `Dockerfile`. Cassandra starts normally; the image simply runs without the metrics exporter, as described in the "Exporter agent jar" section above:

```bash
docker build -t <your-registry>/<your-org>/nvcf-cassandra:<version> .
```

To build with the exporter agent enabled, see the "Exporter agent jar" section above.

For multi-arch builds:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t <your-registry>/<your-org>/nvcf-cassandra:<version> \
  --push .
```

To track a different upstream Cassandra release, edit the `FROM cassandra:<version>` line in the `Dockerfile` directly. The official image tag is the single source of truth for the Cassandra version; there is no separate version build-arg.

## Image contents

At runtime the image provides:

- Apache Cassandra at the version pinned by the `Dockerfile`'s `FROM cassandra:<version>` line, using the official image's runtime layout (`/opt/cassandra/`)
- A Prometheus exporter agent, when supplied at build time, loaded via `-javaagent` and exposed on port `9500`
- `scripts/cassandra-env.sh`, copied over the official image's `/etc/cassandra/cassandra-env.sh`, which derives `CASSANDRA_RACK` from the pod's hostname suffix (`-0`/`-3`/`-6` -> `r1`, `-1`/`-4`/`-7` -> `r2`, `-2`/`-5`/`-8` -> `r3`) and writes it directly into `cassandra-rackdc.properties`
