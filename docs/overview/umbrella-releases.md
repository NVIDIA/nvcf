# Consume an NVCF release

An umbrella release selects one exact combination of the Self-Managed, Compute
Plane and Observability stacks. Its version does not need to match any stack,
chart or image version. Use the matching docs edition, product `manifest.json`
and [artifact manifest](manifest.md) together. Choose the docs edition for the
umbrella version you are deploying; Development follows main and can describe
different artifact pins.

Choose either an umbrella archive or the separate stack packages listed in
this docs edition's [artifact manifest](manifest.md). Use one selected
combination. Mixing the latest versions of each stack can select a different
combination from the one documented here.

For the 1.0.4 selection and changes from docs edition 1.0.2, read the
[release notes](../../fern/changelog/2026-10-26.mdx) and
[upgrade procedure](release-notes/1.0.2-to-1.0.4-upgrade.md).
The [compatibility policy](compatibility-matrix.md#compatibility-and-maintenance-policy)
explains supported version ranges separately from exact bundle membership.

## Choose an installation directory

Keep the extracted stack files, environment files and registration state in a
named directory that survives workstation cleanup. Use a new directory for
each installation or upgrade. Keep the previous directory through the rollback
window; do not extract over an existing deployment's configuration.

Run the examples in Bash. Set the release version to an available published
version and choose a persistent location:

```bash
set -euo pipefail
RELEASE_VERSION="X.Y.Z"
INSTALL_ROOT="$HOME/nvcf/releases/$RELEASE_VERSION"
ASSET_DIR="$INSTALL_ROOT/downloads"
BUNDLE_DIR="$INSTALL_ROOT/stack-files"
mkdir -p "$ASSET_DIR"
```

Choose one of the following download paths.

## Download an umbrella archive

For a published umbrella release, open the matching `nvcf/X.Y.Z` tag in
[GitHub Releases](https://github.com/NVIDIA/nvcf/releases) and download its
`nvcf-X.Y.Z.tar.gz` and `manifest.json` assets. Use the
packaged bundle asset, not GitHub's automatically generated source archive.
The equivalent download command uses the GitHub CLI:

```bash
gh release download "nvcf/${RELEASE_VERSION}" --repo NVIDIA/nvcf \
  --pattern "nvcf-${RELEASE_VERSION}.tar.gz" \
  --pattern manifest.json \
  --dir "$ASSET_DIR"
```

### Extract the archive

Set `ASSET_DIR` to the downloaded assets directory and `RELEASE_VERSION` to the
umbrella version. Extract the archive into a new directory.

```bash
mkdir "$BUNDLE_DIR"
tar -xzf "$ASSET_DIR/nvcf-${RELEASE_VERSION}.tar.gz" -C "$BUNDLE_DIR"
```

## Download separate stack packages

Use [NGC CLI](https://org.ngc.nvidia.com/setup/installers/cli) configured with
a customer API key that can read the selected resource packages. Download into
a new directory and extract each stack to its expected sibling path:

```bash
mkdir "$BUNDLE_DIR"
mkdir -p "$BUNDLE_DIR/deploy/stacks/self-managed" \
  "$BUNDLE_DIR/deploy/stacks/nvcf-compute-plane" \
  "$BUNDLE_DIR/deploy/stacks/observability"
cd "$ASSET_DIR"
```

Run the commands for the packages you need. Obtain every required package
before deployment. A resource marked `Publication pending` has no verified
download command in this catalog.

{/*docs-version-sync:BEGIN stack-package-downloads*/}

```bash
# Self-Managed 1.0.7
# Publication pending: nvcf-self-managed-stack 1.0.7 is not yet available for download.

# Compute Plane 1.0.4
# Publication pending: nvcf-compute-plane-stack 1.0.4 is not yet available for download.

# Observability 1.0.0
ngc registry resource download-version "nvidia/nvcf/nvcf-observability-stack:1.0.0" && \
  tar -xzf "nvcf-observability-stack_v1.0.0/nvcf-observability-stack-1.0.0.tar.gz" \
    -C "$BUNDLE_DIR/deploy/stacks/observability"

```

{/*docs-version-sync:END stack-package-downloads*/}

## Keep the stack directories together

Umbrella archives and separate stack packages use these same paths:

```text
deploy/stacks/self-managed/
deploy/stacks/nvcf-compute-plane/
deploy/stacks/observability/
```

An umbrella archive also contains a root `VERSION`, `manifest.json`, `README.md`,
`LICENSE` and `NOTICE`. Each stack directory's `VERSION` is its independently released
version. Packages include configuration and secret templates. Customer
credentials, external charts and images, and the CLI binary are separate.
Install `nvcf-cli` separately when using registration
or CLI-assisted operations. Pass its existing stack-path options explicitly;
the CLI does not select or verify an umbrella release automatically.

## Configure customer artifact access

The base environments contain `YOUR_ORG/YOUR_TEAM` placeholders.
Create `environments/<name>.yaml` in each selected stack directory and set
`HELMFILE_ENV=<name>`. Complete the deployment-specific endpoints, storage,
network and identity settings described by the
[Helmfile installation guide](../self-managed/helmfile-installation.md).
For the public NVCF catalog, include these values in the Self-Managed and
Compute Plane environments:

```yaml
global:
  helm:
    sources:
      url: https://helm.ngc.nvidia.com/nvidia/nvcf
      username: "$oauthtoken"
      password: "<customer NGC API key>"
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
  imagePullSecrets:
    - name: nvcf-registry
```

Replace the password placeholder in local configuration with a customer
credential authorized for the selected catalog. Keep credential-bearing files
out of source control. The HTTPS chart URL requires authentication; the URL
alone can return 403. These are required installation inputs, not version
changes or substitutions for producer artifacts. Preserve selected versions
and the default resource and security settings unless the deployment requires
a reviewed override.

The credentials have different consumers:

| Consumer | Configuration |
| --- | --- |
| Helmfile downloading HTTPS charts | `global.helm.sources.username` and `password` in the selected Helmfile environment. A Docker or OCI registry login does not configure HTTPS chart authentication. |
| Kubernetes pulling stack images | Create the named `global.imagePullSecrets` secret in every namespace where it is used, with customer access to `nvcr.io/nvidia/nvcf`. |
| Worker sidecars and account bootstrap | Fill the bundled Self-Managed `secrets/secrets.yaml.template` and save it as `secrets/<name>-secrets.yaml`. Follow its raw base64 `username:password` format for `NVCF_API_SIDECARS_IMAGE_PULL_SECRET` and bootstrap registry credentials. |
| User function/task artifacts | Register credentials for the customer's workload registries separately. Product catalog credentials do not grant access to arbitrary workload artifacts. |

Worker sidecar registry settings inherit `global.image` unless explicitly
overridden. Clear obsolete `global.sidecars` overrides when they would route
workers to a different repository. Review the template's database credentials
and other required secrets as well.

A configured HTTPS source takes precedence over OCI source fields. If using
a mirror instead, omit `global.helm.sources.url`, configure the mirror's
`registry` and `repository`, and authenticate to it. Preserve the exact chart
and image versions. Some dependencies retain upstream registries; see
[upstream image configuration](manifest.md#supporting-images-that-ship-from-docker-hub)
and [image mirroring](image-mirroring.md).

## Deploy the selected stacks

Use the Helm and Helmfile versions recorded in the release's manifest. NVCF
1.0.4 uses Helm 3.21.4 and Helmfile 1.1.9. Select each cluster context explicitly. Read the extracted README
and render with the packaged Helmfiles before applying.

For a fresh installation:

1. Configure the selected Observability profile. The Self-Managed stack
   delegates enabled Observability infrastructure to its sibling directory
   before installing dependencies and core services. Do not install the same
   Observability resources twice on one cluster.
2. Render and install the Self-Managed stack with its packaged Makefile/Helmfile
   entry points. Its ordered stages install enabled Observability infrastructure,
   dependencies, core services, enabled state-metrics/autoscaling services and
   the final upgrade receipt. Wait for services and migration jobs to complete.
3. Export the control-plane profile from the extracted stack, then register each
   GPU cluster using its own kubeconfig/context and install the packaged Compute
   Plane stack. Follow [cluster registration](../compute-plane/cluster-management/self-managed.md#register-the-cluster)
   from `BUNDLE_DIR`, with `NVCF_CLI` pointing to an installed binary. Use
   `--control-plane-stack "$BUNDLE_DIR/deploy/stacks/self-managed"` for profile
   export and `make -C "$BUNDLE_DIR/deploy/stacks/nvcf-compute-plane"` for the
   packaged registration and install targets. Do not replace these paths with
   a checkout of current main.
4. Validate cluster registration, a representative function and task, secret
   delivery and the telemetry paths enabled for this deployment. Record the
   manifest and archive digest with the installed configuration.

Observability 1.0.0 is included in the selection even when its deployment is
disabled. Profiles are `disabled`, `control`, `compute` and `all`; component
modes can use existing infrastructure where supported. For a separate compute
cluster, configure and run the sibling Observability stack on that cluster if
its profile is enabled. Follow the [Observability guide](../observability/observability.md)
for profile and existing-backend requirements. Enable only the telemetry paths required for the deployment.

For an existing installation, use the
[upgrade notes](release-notes/1.0.2-to-1.0.4-upgrade.md) instead of the fresh
installation order. They require hostname-policy preparation, patched NVCA
before shared worker defaults, queue draining and explicit rollback limits.

## Optional integrity verification

Published umbrella releases can include `release-assets.json` with archive and
manifest hashes. Use it to verify downloaded files before extraction when your
installation process requires an integrity check. Packaging details are in the
[release tooling reference](https://github.com/NVIDIA/nvcf/blob/main/docs/dev/umbrella-releases.md#install-with-existing-stack-interfaces).
