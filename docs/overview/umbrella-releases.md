# Consume an NVCF umbrella release

An umbrella release selects one exact combination of the Self-Managed, Compute
Plane and Observability stacks. Its version does not need to match any stack,
chart or image version. Use the matching docs edition, product `manifest.json`
and [artifact manifest](manifest.md) together. Choose the docs edition for the
umbrella version you are deploying; Development follows main and can describe
different artifact pins.

<Warning>
This preview describes the NVCF 1.0.4 candidate: Self-Managed 1.0.7,
Compute Plane 1.0.3 and Observability 1.0.0. It remains pending QA and customer
publication. The published-release download below is for an available final
release; it is not a download claim for this candidate.
</Warning>

Docs edition 1.0.2 selected Self-Managed 1.0.1, Compute Plane 1.0.0 and
Observability 1.0.0. To upgrade that installation, follow the
[staged upgrade procedure](release-notes/1.0.2-to-1.0.4-upgrade.md). A docs-only
edition change does not require an installed stack change. See the
[compatibility policy](compatibility-matrix.md#compatibility-and-maintenance-policy)
for the distinction between exact selection and supported version ranges.

## Obtain the bundle

For this candidate, download the QA artifact from the
[assembly run linked in the manifest](manifest.md#candidate-identity). Retain
its candidate commit, original inventory hashes and bundle SHA-256 with the
results. This CI artifact is not a final published release. Qualification and
final assembly can change its manifest and archive hashes.

For a published umbrella release, open the matching `nvcf/X.Y.Z` tag in
[GitHub Releases](https://github.com/NVIDIA/nvcf/releases) and download its
`nvcf-X.Y.Z.tar.gz`, `manifest.json` and `release-assets.json` assets. Use the
packaged bundle asset, not GitHub's automatically generated source archive.
The commands below use the GitHub CLI, `jq`, and `shasum` for checksum
verification. The equivalent download command is:

```bash
RELEASE_VERSION="X.Y.Z" # Replace with an available published umbrella version.
ASSET_DIR="$(mktemp -d)"
gh release download "nvcf/${RELEASE_VERSION}" --repo NVIDIA/nvcf \
  --pattern "nvcf-${RELEASE_VERSION}.tar.gz" \
  --pattern manifest.json --pattern release-assets.json \
  --dir "$ASSET_DIR"
```

An umbrella bundle is distributed through its GitHub release assets. The
separately versioned NGC stack resources are not an umbrella download command.
Do not assemble a replacement combination from whichever stack releases are
latest.

## Verify and extract

Set `ASSET_DIR` to the directory containing the downloaded assets and
`RELEASE_VERSION` to their umbrella version. Verify both the archive and
manifest against `release-assets.json` obtained from the trusted release or
candidate artifact:

```bash
(
  set -eu
  cd "$ASSET_DIR"
  for asset in "nvcf-${RELEASE_VERSION}.tar.gz" manifest.json; do
    digest="$(jq -er --arg asset "$asset" \
      '.assets[$asset] | select(test("^sha256:[0-9a-f]{64}$")) | sub("^sha256:"; "")' \
      release-assets.json)"
    printf '%s  %s\n' "$digest" "$asset" | shasum -a 256 -c -
  done
)
```

Stop if either check fails. For the current QA artifact, also compare the
archive digest to the [candidate identity](manifest.md#candidate-identity).
If a source checkout is available, the repository's `umbrella verify-bundle`
command additionally validates the embedded manifest and file index; see the
[verification command](https://github.com/NVIDIA/nvcf/blob/main/docs/dev/umbrella-releases.md#install-with-existing-stack-interfaces).

Extract into a new directory after verification:

```bash
BUNDLE_DIR="$(mktemp -d)"
tar -xzf "$ASSET_DIR/nvcf-${RELEASE_VERSION}.tar.gz" -C "$BUNDLE_DIR"
```

Keep this layout intact:

```text
README.md
VERSION
manifest.json
inventory.json
bundle-files.json
inventories/
deploy/stacks/self-managed/
deploy/stacks/nvcf-compute-plane/
deploy/stacks/observability/
```

Root `VERSION` is the umbrella version. Each stack directory's `VERSION` is
that stack's independently released version. The archive includes configuration
and secret templates, but not customer credentials, external chart/image
payloads or the CLI binary. Install `nvcf-cli` separately when using registration
or CLI-assisted operations. Pass its existing stack-path options explicitly;
the CLI does not select or verify an umbrella release automatically.

## Configure customer artifact access

The candidate's base environments contain `YOUR_ORG/YOUR_TEAM` placeholders.
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
and the default resource and security settings when qualifying the candidate.

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

Use Helm 3.21.4 and Helmfile 1.1.9 for this candidate, as recorded in its
manifest. Select each cluster context explicitly. Read the extracted README
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
for profile and existing-backend requirements. Bundle membership alone does not
enable or qualify every telemetry path.

For an existing installation, use the
[upgrade notes](release-notes/1.0.2-to-1.0.4-upgrade.md) instead of the fresh
installation order. They require hostname-policy preparation, patched NVCA
before shared worker defaults, queue draining and explicit rollback limits.
