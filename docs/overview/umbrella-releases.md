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

Start a dedicated Bash child shell with `bash`, then run the examples below.
Fail-fast mode returns to the original terminal on an error. Inspect any
partial download or extraction before retrying; keep existing deployment
directories intact. Set the release version to an available published version
and choose a persistent location:

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
# Self-Managed 1.0.10
ngc registry resource download-version "nvidia/nvcf/nvcf-self-managed-stack:1.0.10" && \
  tar -xzf "nvcf-self-managed-stack_v1.0.10/nvcf-self-managed-stack-1.0.10.tar.gz" \
    -C "$BUNDLE_DIR/deploy/stacks/self-managed"

# Compute Plane 1.0.5
ngc registry resource download-version "nvidia/nvcf/nvcf-compute-plane-stack:1.0.5" && \
  tar -xzf "nvcf-compute-plane-stack_v1.0.5/nvcf-compute-plane-stack-1.0.5.tar.gz" \
    -C "$BUNDLE_DIR/deploy/stacks/nvcf-compute-plane"

# Observability 1.0.0
ngc registry resource download-version "nvidia/nvcf/nvcf-observability-stack:1.0.0" && \
  tar -xzf "nvcf-observability-stack_v1.0.0/nvcf-observability-stack-1.0.0.tar.gz" \
    -C "$BUNDLE_DIR/deploy/stacks/observability"

```

{/*docs-version-sync:END stack-package-downloads*/}
