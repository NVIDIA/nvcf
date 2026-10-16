# Non-root dependency images

These images preserve the released NATS server, NATS config reloader, and
Kubernetes helper binaries. They declare UID/GID `1000:1000` in OCI metadata,
so image scanners and Kubernetes admission checks can verify the runtime user.
The helper also uses `/tmp` for its working directory, home, and cache.

| Build target | Output repository | Upstream version |
| --- | --- | --- |
| `nats-server` | `nvcf-nats-server-nonroot` | `nats:2.14.6-alpine3.22` |
| `nats-reloader` | `nvcf-nats-config-reloader-nonroot` | `natsio/nats-server-config-reloader:0.24.0` |
| `k8s-helper` | `nvcf-k8s-helper-nonroot` | `alpine/k8s:1.37.0` |

The three outputs use the same producer version from
`infra/nonroot-images/vX.Y.Z`. Their upstream versions remain independent.
Release automation assigns stable versions from changes merged to `main`,
starting at `0.1.0`. All three outputs share each source release.
Each Dockerfile pins its upstream manifest digest and records it in image
labels. `docker-bake.hcl` builds all outputs together. `NOTICE` records the source and attribution requirements.

## Build and test

```sh
cd infra/nonroot-images
docker buildx bake --print
docker buildx bake
```

The default creates OCI archives for Linux amd64 and arm64 in this directory.
It does not publish images. To run the local container checks, build and load
the host architecture, then test:

```sh
docker buildx bake --set '*.platform=linux/arm64' --set '*.output=type=docker'
./test.sh
```

Use `linux/amd64` on an amd64 host. Set `REGISTRY` and `VERSION` consistently
for builds and tests when using names other than `local/nvcf` and `dev`.

## Release and chart requirements

The release publisher must build all three outputs for both architectures,
retain the upstream SBOM and licenses, run security scans, and sign the final
manifest digests. Verify the numeric user on each resulting platform digest.
A source tag alone is not evidence that a distributable image exists.

NATS server and reloader must run under the same numeric UID so the reloader
can signal the server. JetStream storage must be writable by UID/GID 1000.
Charts using the helper with a read-only root filesystem must mount a writable
`emptyDir` at `/tmp`. Validate NATS reload and persistence and every consuming
Helm hook on fresh installs and upgrades before changing released image pins.
