# Patched upstream helper images

We patch Brotli in the upstream Kubernetes toolbox and NATS box package
layers. The Kubernetes helper also builds kubectl 1.37.2 from checksum-pinned
source with Go 1.26.10, x/net 0.60.0 and OpenTelemetry 1.44.0. It also includes patched kubectl
1.36.6 for Kubernetes 1.35 compatibility. Both images run as numeric UID/GID
`1000:1000`. NATS server and config reloader remain separate upstream images.

| Output | Upstream input | Package branch |
| --- | --- | --- |
| `nvcf-k8s-helper-nonroot` | `docker.io/alpine/k8s:1.37.1` | Alpine edge |
| `nvcf-nats-box-nonroot` | `docker.io/natsio/nats-box:0.20.1-nonroot` | Alpine 3.24 |

The Dockerfiles pin multi-architecture upstream digests. `brotli-checksums.txt`
pins signed `brotli-libs 1.2.1-r0` packages for amd64 and arm64. The build
checks SHA-256 before installing the APK without network resolution. If a
pinned APK disappears from the upstream repository, the build fails; review
and pin a replacement before rebuilding. Other packages and helper binaries
stay unchanged. The toolbox base is 1.37.1; its final kubectl is 1.37.2,
selected in `kubectl-version.txt`. Its source build preserves the migrations
producer's HTTP/2 security baseline and marks the source tree dirty. OCI base
labels and [NOTICE](NOTICE) retain upstream origin and license attribution.

## Build and verify

Run from this directory:

```sh
docker buildx bake --print
docker buildx bake
```

The default outputs are local OCI archives for both architectures. To load
and exercise one architecture, use a unique local version:

```sh
VERSION=validation docker buildx bake --set '*.platform=linux/arm64' --set '*.output=type=docker'
VERSION=validation PLATFORM=linux/arm64 ./test.sh
```

Repeat with `linux/amd64`. The checks inspect the final package version,
numeric identity, required tools, kubectl build metadata and checksum, and
unchanged upstream executable hashes. Containers run with a read-only root
filesystem, dropped capabilities, no network and a writable `/tmp` mount. Tests exercise ConfigMap client rendering, shell
encoding, NATS context storage and NKey generation. Live receipt install and
upgrade, account bootstrap and authenticated NATS operations still need
cluster qualification.

## Consumption and release

`infra/nonroot-images/vX.Y.Z` releases produce both outputs at the same
producer version. That version is independent of the upstream toolbox
version. Publisher routing, first-publication metadata, both-platform scans,
SBOMs and customer-registry digest readback must be ready before selecting
these outputs in charts. Source releases and local builds do not establish
published image availability.

Consumers can select the published images through the existing receipt
`image`, API `api.accountBootstrap.image`, NVSnap
`postInstallSmoke.image`, and NATS `nats.natsBox.container.image` values.
Keep upstream defaults until the derivative artifacts are published and
qualified. Regenerate inventories and catalogs from the released consumers.

The Kubernetes image is a CLI toolbox, not a cluster server requirement.
The default client is kubectl 1.37.2. For a Kubernetes 1.35 API server, set
`NVCF_KUBECTL_MINOR=1.36` in the helper container to select patched kubectl
1.36.6 from the same image. An unset selector uses 1.37; explicit 1.37 or
1.36 values are accepted. Empty and other values fail with exit code 64
before cluster access. Both PATH and `/usr/bin/kubectl` use the selector,
which forwards arguments and exit status to the chosen binary.

This preserves NVCF's documented cluster window without automatic server
probing or a second image release lane. Configure the selector explicitly
in receipt, bootstrap and smoke-hook Pod environments when consuming the
image on Kubernetes 1.35. Qualify both client choices with the target server
under the [upstream skew policy](https://kubernetes.io/releases/version-skew-policy/#kubectl).
