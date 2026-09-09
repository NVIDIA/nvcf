# Register a Cluster with GitOps

Use this workflow to register a cluster in any NGC organization without using the
NGC cluster-registration UI. Sign in through the NGC CLI email authentication
flow, complete the browser-based OAuth login, and select the organization that
will own the cluster. The login creates a temporary Starfleet session for NGC
CLI cluster operations. It does not create or convert a service account key
(SAK).

Successful registration generates a separate, cluster-scoped NGC Cluster Key
for the operator and NGC artifact access. You must capture and securely store
this key because the registration output shows it only once.

<Note>

NGC cluster registration is an API operation, not a Kubernetes resource. Run the
interactive login and registration from a secured administrator workstation.
The generated Cluster Key cannot be recovered with `ngc cf cluster info`, so
import it into your secret mechanism before discarding the registration output.
After registration, keep the operator release and non-secret cluster
configuration in Git and let a GitOps controller reconcile them.

</Note>

## Prerequisites

Before you begin, you need:

- An NGC organization with NVIDIA Cloud Functions enabled
- An NGC user account that can register clusters in that organization
- An NGC team in that organization, if the organization uses teams
- A browser that can complete your organization's sign-in requirements
- The current [NGC CLI](https://docs.ngc.nvidia.com/cli/index.html), `jq`, and
  access to the target Kubernetes cluster
- A GitOps controller that can install Helm charts
- A GitOps-compatible secret mechanism, such as SOPS, Sealed Secrets, or an
  external secret store, that can import the generated Cluster Key without
  printing it
- An approved NVCA operator chart version and NVCA version from the same release

Do not select the newest chart and agent versions independently. Use a tested
version pair from the release manifest for your environment.

Check the installed NGC CLI version and email-authentication interface:

```bash
ngc --version
ngc config set --help
```

The help output must list `email` as a value for `--auth-option`:

```text
--auth-option {api-key,email}
```

## 1. Sign in to NGC

The NGC CLI uses email authentication to establish a temporary Starfleet
session for cluster create and delete operations. This session is separate from
both an organization SAK and the Cluster Key generated later.

Make sure `NGC_API_KEY` and `NGC_CLI_API_KEY` are not set. Either environment
API key can take precedence over the saved OAuth session. An API-key-authenticated
`ngc cf cluster create` request is rejected before it contacts the API.

```bash
unset NGC_API_KEY NGC_CLI_API_KEY
ngc config set --auth-option email
```

Run this command without `--org` or `--team`. Select the organization and team
after the browser authentication completes. Supplying those flags before an
authenticated Starfleet session exists can save an unauthenticated configuration
and fail with `Invalid org - If not Authenticated, org cannot be set.`

At the prompts:

1. Enter the email address for your NGC user account.
2. Open the login URL if the CLI does not open it automatically. The CLI can
   skip this step when a valid Starfleet session already exists.
3. Complete your organization's browser sign-in flow when prompted.
4. Return to the terminal and select the NGC organization and team that will own
   the cluster. Select `no-team` if the organization does not use teams.

The CLI saves the session and selected defaults in `~/.ngc/config`. The session
expires after 24 hours. Repeat this step when the CLI reports that the session
is missing or expired. Keep both API-key environment variables unset until you
finish the cluster registration.

Verify the selected organization and team:

```bash
ngc config current
```

Do not continue if the command shows a different organization. Rerun
`ngc config set --auth-option email` and select the intended organization.

## 2. Configure the registration inputs

Set explicit inputs so the registration commands do not inherit an unrelated
organization or team from an older NGC CLI configuration.

Set the non-secret registration inputs:

```bash
export NGC_ORG="<org-name>"
export NGC_TEAM="<team-name-or-no-team>"
export CLUSTER_NAME="<cluster-name>"
export CLUSTER_GROUP_NAME="<cluster-group-name>"
export CLOUD_PROVIDER="ON-PREM"
export CLUSTER_REGION="us-west-1"
export NVCA_OPERATOR_VERSION="<approved-operator-chart-version>"
export NVCA_VERSION="<approved-nvca-version>"
```

`NGC_ORG` is the NGC organization name, not its display name. Set `NGC_TEAM` to
`no-team` when the organization does not use teams. An explicit value prevents
the CLI from inheriting an unrelated team from the local NGC configuration.

For a cloud-hosted cluster, set `CLOUD_PROVIDER` and `CLUSTER_REGION` to the
actual platform and region. Use `ON-PREM` only for infrastructure that is not
represented by another provider value. For an on-premises cluster,
`CLUSTER_REGION` is a logical location. The provider and region are immutable,
and their values must be accepted by `ngc cf cluster create --help`.

If you add `--cluster-description` to the registration command, limit the value
to 32 characters. The service rejects a longer description with an HTTP `412`
response, but the current CLI help does not show this limit.

List the organizations available to your signed-in identity so you can confirm
the correct organization name:

```bash
ngc org list --format_type json |
  jq '.[] | {name, displayName}'
```

Then check whether the selected organization uses teams:

```bash
ngc team list \
  --org "${NGC_ORG}" \
  --format_type json |
  jq '.[] | {name, displayName}'
```

If this command returns an empty array, use `NGC_TEAM=no-team`.

Use explicit `--org` and `--team` values to verify that the signed-in identity
can read clusters in the selected organization:

```bash
ngc cf cluster list \
  --org "${NGC_ORG}" \
  --team "${NGC_TEAM}" \
  --format_type json |
  jq '[.[] | {clusterId, clusterName, status}]'
```

A successful `cluster list` confirms the organization selection and read access.
The email/OAuth session, not an API key supplied through `NGC_CLI_API_KEY`,
authorizes the create operation.

## 3. Register the cluster once and guard reruns

Registration creates the NGC cluster and cluster-group IDs that bind the Helm
release to the control plane. It also generates the Cluster Key used by the
operator. Check the immutable cluster name first so rerunning the bootstrap does
not create a duplicate.

First, look for an existing cluster with the requested immutable name:

```bash
CLUSTER_ID="$(
  ngc cf cluster list \
    --org "${NGC_ORG}" \
    --team "${NGC_TEAM}" \
    --format_type json |
  jq -r --arg name "${CLUSTER_NAME}" \
    '[.[] | select(.clusterName == $name)][0].clusterId // empty'
)"
```

If `CLUSTER_ID` is not empty, do not run `cluster create` again. Verify that the
Cluster Key from the original registration is already present in your secret
mechanism before continuing. The NGC CLI cannot retrieve the original key; if
the key was not saved, use **Rotate Key** in the NGC UI and import the new key.

<Warning>

Do not repoint an existing NVCA Helm release at a second NGC cluster record by
changing `clusterName`, `clusterID`, `clusterGroupID`, and the Cluster Key in
place. The operator supports one `NVCFBackend` per Kubernetes cluster. An
identity change can leave both the old and new backends in the cluster, which
stops the operator before it can rotate the agent credential. To register a new
control-plane identity, first use the supported unregister and cleanup workflow,
or install it on a fresh Kubernetes cluster. For a version-only upgrade, retain
the existing registration identifiers and Cluster Key and change only the
approved chart and NVCA versions.

</Warning>

If `CLUSTER_ID` is empty, register the cluster. The command emits a generated
Helm command containing the one-time Cluster Key, so redirect its output to
mode-`0600` files in a private temporary directory. Do not enable shell tracing
around this block. Keep the protected key file until you verify its import into
your secret mechanism; automatic cleanup on shell exit can permanently lose the
one-time key after the cluster record has already been created.

```bash
umask 077
registration_dir="$(mktemp -d)"
registration_output="${registration_dir}/registration-output"
cluster_key_file="${registration_dir}/cluster-key"
touch "${registration_output}" "${cluster_key_file}"
chmod 600 "${registration_output}" "${cluster_key_file}"

if ! ngc cf cluster create \
    --org "${NGC_ORG}" \
    --team "${NGC_TEAM}" \
    --cluster-name "${CLUSTER_NAME}" \
    --cluster-group-name "${CLUSTER_GROUP_NAME}" \
    --cloud-provider "${CLOUD_PROVIDER}" \
    --region "${CLUSTER_REGION}" \
    --nvca-version "${NVCA_VERSION}" \
    --capability DynamicGPUDiscovery \
    --format_type ascii >"${registration_output}"; then
  echo "Cluster registration failed; no Cluster Key was captured" >&2
  exit 1
fi

# NGC CLI returns the Cluster Key as the password in its generated Helm command.
# Extract it without writing it to stdout.
sed -n 's/.*--password="\([^"]*\)".*/\1/p' "${registration_output}" |
  head -n 1 >"${cluster_key_file}"

if ! grep -Eq '^nvapi[-_]' "${cluster_key_file}"; then
  echo "Registration did not return a recognizable Cluster Key" >&2
  exit 1
fi

chart_url="$(
  grep -Eo 'https://helm(\.stg)?\.ngc\.nvidia\.com/[^"[:space:]]+' \
    "${registration_output}" |
  head -n 1
)"
case "${chart_url}" in
  *.tgz) ;;
  "") echo "Registration did not return an operator chart URL" >&2; exit 1 ;;
  *) chart_url="${chart_url}.tgz" ;;
esac

case "${chart_url}" in
  */nvca-operator-"${NVCA_OPERATOR_VERSION}".tgz) ;;
  *)
    echo "Registration returned a chart version that does not match the approved version" >&2
    exit 1
    ;;
esac

CLUSTER_ID="$(
  ngc cf cluster list \
    --org "${NGC_ORG}" \
    --team "${NGC_TEAM}" \
    --format_type json |
  jq -er --arg name "${CLUSTER_NAME}" \
    '[.[] | select(.clusterName == $name)][0].clusterId'
)"

```

If the command reports that `NGC_CLI_API_KEY` is invalid, confirm that the
`NGC_API_KEY` and `NGC_CLI_API_KEY` variables are unset in the current shell. If
the CLI reports a missing or expired login, repeat step 1 and retry only after
confirming that no cluster record was created.

If the requested NVCA version is unavailable, the command fails without creating
the cluster and lists the versions available to that NGC environment. Do not
silently substitute a version. Select an approved version from the returned list
or arrange publication of the required version, then repeat the guarded
registration step.

Do not pass `--ssa-client-id`; the email/OAuth session authenticates this
workflow. Before deleting `cluster_key_file`, import it into the secret mechanism
selected in the prerequisites and verify that the stored value is non-empty
without displaying it. Do not store your OAuth session or an organization SAK in
Kubernetes.

For a newly registered cluster, use the generated Cluster Key to verify access
to the exact approved chart and NVCA agent image. These are the credentials and
artifacts the cluster will use at runtime. For an existing cluster, run the
equivalent checks through the secret mechanism without printing the key:

```bash
NGC_CLI_API_KEY="$(tr -d '\n' <"${cluster_key_file}")" \
  ngc registry chart info \
    "nvidia/nvcf-byoc/nvca-operator:${NVCA_OPERATOR_VERSION}" \
    --format_type json |
  jq '{version: .version.id, status: .version.status}'

NGC_CLI_API_KEY="$(tr -d '\n' <"${cluster_key_file}")" \
  ngc registry image info \
    "nvidia/nvcf-byoc/nvca:${NVCA_VERSION}" \
    --format_type json >/dev/null
```

Stop if either check returns `401 Unauthorized` or `403 Access Denied`. Do not
substitute an organization SAK; resolve the Cluster Key authorization or
approved version mismatch before installing the operator.

Read the normalized cluster record and retain only the non-secret fields needed
by Helm:

```bash
ngc cf cluster info "${CLUSTER_ID}" \
  --org "${NGC_ORG}" \
  --team "${NGC_TEAM}" \
  --format_type json >cluster-info.json

jq -e '{
  ncaID: .ncaId,
  clusterID: .clusterId,
  clusterName,
  clusterGroupID: .clusterGroupId,
  clusterGroupName,
  cloudProvider,
  clusterRegion: .region,
  nvcaVersion
} |
if all(.[]; . != null and . != "") then
  .
else
  error("cluster info is missing a required Helm value")
end' cluster-info.json
```

Commit those fields to the cluster's Helm values. Do not commit the complete
registration response.

## 4. Materialize the Cluster Key as Kubernetes secrets

The operator uses the generated Cluster Key to call NGC APIs and pull runtime
images. The GitOps controller also needs it to fetch the private Helm chart.
Project the one stored Cluster Key into separate secrets so each consumer
receives the credential in the format it expects.

Configure the GitOps secret mechanism to create these secrets:

| Secret | Namespace | Required data | Consumer |
| --- | --- | --- | --- |
| `ngc-service-key` | `nvca-operator` | `ngcServiceKey` containing the Cluster Key | NVCA operator |
| `nvca-operator-image-pull` | `nvca-operator` | A `kubernetes.io/dockerconfigjson` credential for `nvcr.io` with username `$oauthtoken` and the Cluster Key as password | Operator and agent images |
| Chart repository credential | GitOps controller namespace | Username `$oauthtoken` and the Cluster Key as password; for Flux, use an `Opaque` Secret with `username` and `password` keys | GitOps Helm source |

Keep all three credentials encrypted or externally materialized. Do not put the
Cluster Key in a Helm values file, command-line `--set` argument, or plain
Kubernetes Secret in Git. After all three projections have reconciled and been
verified, delete the protected local registration files and directory.

The runtime image-pull Secret starts in the operator namespace. The operator
mirrors it into the agent namespace when it creates the NVCA workload. This
ordering can cause an initial anonymous image-pull failure before the mirrored
Secret becomes available. The agent pull must recover without manual changes.

```bash
if test -n "${registration_dir:-}"; then
  rm -f "${registration_output}" "${cluster_key_file}"
  rmdir "${registration_dir}"
fi
```

## 5. Commit the Helm values

The values bind the operator release to the cluster record from step 3. Commit
only non-secret identifiers and configuration so Git remains safe to share.

Create a values file from the non-secret fields returned by the cluster API:

```yaml
ncaID: "<nca-id>"
clusterID: "<cluster-id>"
clusterName: "<cluster-name>"

ngcConfig:
  username: "$oauthtoken"
  serviceKey: ""
  serviceKeySecretName: ngc-service-key
  serviceKeySecretKeyName: ngcServiceKey
  apiURL: https://api.ngc.nvidia.com
  clusterSource: helm-managed

vaultConfig:
  oAuthClientMountPathTemplate: ""
  oAuthClientMountPath: ""

generateImagePullSecret: false
imagePullSecretName: nvca-operator-image-pull
imagePullSecrets:
  - name: nvca-operator-image-pull

helmManaged:
  cloudProvider: "<cloud-provider>"
  clusterRegion: "<region>"
  clusterGroupID: "<cluster-group-id>"
  clusterGroupName: "<cluster-group-name>"
  nvcaVersion: "<approved-nvca-version>"
  oAuthClientID: ""
  oAuthClientSecretKey: ""
  featureGateValues:
    - DynamicGPUDiscovery
```

Leaving `ngcConfig.serviceKey` empty prevents Helm from storing the Cluster Key
in the release values. Disabling `generateImagePullSecret` prevents the chart
from requiring the Cluster Key as a Helm value. The two pre-created secrets
provide the runtime API and registry credentials instead.

## 6. Define the GitOps Helm release

The Helm release installs the operator and keeps its version and configuration
reconciled from Git. Pin the exact chart version verified against `chart_url` so
an upstream release cannot change the cluster unexpectedly.

Configure the GitOps controller with these Helm source settings:

| Setting | Value |
| --- | --- |
| Repository | `https://helm.ngc.nvidia.com/nvidia/nvcf-byoc` |
| Chart | `nvca-operator` |
| Version | The approved, pinned chart version |
| Release | `nvca-operator` |
| Namespace | `nvca-operator` |
| Values | The file from the previous step |

For example, the following Flux resources use the chart credential from step 4
and pin the chart version. Put the complete non-secret values mapping from step
5 under `spec.values` in the `HelmRelease`.

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: nvca-ngc
  namespace: flux-system
spec:
  interval: 1h
  url: https://helm.ngc.nvidia.com/nvidia/nvcf-byoc
  secretRef:
    name: nvca-ngc-helm-auth
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: nvca-operator
  namespace: nvca-operator
spec:
  interval: 5m
  timeout: 10m
  chart:
    spec:
      chart: nvca-operator
      version: "<approved-operator-chart-version>"
      sourceRef:
        kind: HelmRepository
        name: nvca-ngc
        namespace: flux-system
      interval: 1h
  values:
    ncaID: "<nca-id>"
    clusterID: "<cluster-id>"
    clusterName: "<cluster-name>"
    ngcConfig:
      username: "$oauthtoken"
      serviceKey: ""
      serviceKeySecretName: ngc-service-key
      serviceKeySecretKeyName: ngcServiceKey
      apiURL: https://api.ngc.nvidia.com
      clusterSource: helm-managed
    vaultConfig:
      oAuthClientMountPathTemplate: ""
      oAuthClientMountPath: ""
    generateImagePullSecret: false
    imagePullSecretName: nvca-operator-image-pull
    imagePullSecrets:
      - name: nvca-operator-image-pull
    helmManaged:
      cloudProvider: "<cloud-provider>"
      clusterRegion: "<region>"
      clusterGroupID: "<cluster-group-id>"
      clusterGroupName: "<cluster-group-name>"
      nvcaVersion: "<approved-nvca-version>"
      oAuthClientID: ""
      oAuthClientSecretKey: ""
      featureGateValues:
        - DynamicGPUDiscovery
```

The `HelmRepository` must become Ready before the `HelmRelease` can resolve the
private chart. A successful source reconciliation also verifies the chart
credential without exposing the Cluster Key.

Make the namespace and both runtime secrets dependencies of the Helm release.
The GitOps controller must not attempt the release until those resources are
ready.

<Warning>

The NGC CLI does not currently expose a cluster management-mode option or a
command that marks the NGC UI read-only. Setting
`ngcConfig.clusterSource: helm-managed` makes the operator use only the
Git-managed cluster configuration. Do not make later configuration changes in
the NGC UI. If preventing UI changes is a hard requirement, the current public
CLI does not provide a fully equivalent replacement for the UI management-mode
switch.

</Warning>

## 7. Verify registration

Check both Kubernetes and NGC state. Kubernetes readiness proves that the
operator reconciled locally, while `nvcaLastConnected` proves that the agent
authenticated to the control plane.

After the GitOps controller reports a successful reconciliation, verify the
operator and cluster agent:

```bash
kubectl -n nvca-operator rollout status deployment/nvca-operator
kubectl -n nvca-operator get nvcfbackend
kubectl -n nvca-operator get pods

ngc cf cluster info "${CLUSTER_ID}" \
  --org "${NGC_ORG}" \
  --team "${NGC_TEAM}" \
  --format_type json |
  jq '{clusterId, clusterName, status, nvcaVersion, nvcaLastConnected}'
```

The `NVCFBackend` health should become `healthy`, and the NGC cluster record
should report a current `nvcaLastConnected` value.

The first agent image pull can report `FailedToRetrieveImagePullSecret`, an
anonymous registry `403`, or `ErrImagePull` while the operator mirrors
`nvca-operator-image-pull` into the `nvca-system` namespace. The pod should
recover and become Ready. If the error persists, verify the mirrored Secret and
pod state without displaying the credential:

```bash
kubectl -n nvca-system get secret nvca-operator-image-pull
kubectl -n nvca-system get pods
```

## 8. Plan Cluster Key rotation

The generated Cluster Key expires after 90 days. Record its expiration in your
secret-management process and rotate it before it expires. The NGC CLI does not
expose a cluster-key rotation command; use **Rotate Key** for the cluster in the
NGC UI, replace the stored Cluster Key, and verify that all three projections
from step 4 reconcile successfully.

## Related Documentation

- [Helm-Managed Clusters](./helm-managed.md)
- [NGC-Managed Clusters](./ngc-managed.md)
- [Helm Values Reference](./reference.md)
- [Service Keys](../service-keys.md)
