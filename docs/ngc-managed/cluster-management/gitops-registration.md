# Register a Cluster with GitOps

Use this workflow to register a cluster from an external NGC organization without
using the NGC UI. The workflow uses a scoped API key (SAK) for authentication and
Helm-managed mode for cluster configuration. It does not require an SSA client or
Vault.

<Note>

NGC cluster registration is an API operation, not a Kubernetes resource. Run the
registration command as an idempotent CI bootstrap step. After registration, keep
the operator release and cluster configuration in Git and let a GitOps controller
reconcile them.

</Note>

## Prerequisites

Before you begin, you need:

- An external NGC organization with NVIDIA Cloud Functions enabled
- An NGC team in that organization
- A SAK with access to administer NVCF clusters and read the NVCF BYOC chart and
  container repositories
- A current NGC CLI, `jq`, and access to the target Kubernetes cluster
- A GitOps controller that can install Helm charts
- A GitOps-compatible secret mechanism, such as SOPS, Sealed Secrets, or an
  external secret store
- An approved NVCA operator chart version and NVCA version from the same release

Do not select the newest chart and agent versions independently. Use a tested
version pair from the release manifest for your environment.

Verify that the installed NGC CLI supports the external-organization flow:

```bash
ngc cf cluster create --help
```

The `--ssa-client-id` option must be optional. If it appears under required
arguments, update the NGC CLI before continuing.

## 1. Configure the CI environment

Store the SAK in a masked CI variable. Expose it to the NGC CLI as
`NGC_CLI_API_KEY`. Do not write it to the NGC CLI configuration file in a shared
runner.

Set the non-secret registration inputs:

```bash
export NGC_ORG="<org-name>"
export NGC_TEAM="<team-name>"
export CLUSTER_NAME="<cluster-name>"
export CLUSTER_GROUP_NAME="<cluster-group-name>"
export CLOUD_PROVIDER="ON-PREM"
export CLUSTER_REGION="us-west-1"
export NVCA_VERSION="<approved-nvca-version>"
export NGC_CLI_API_KEY="${NVCF_SAK}"
```

`NGC_ORG` is the NGC organization name, not its display name. For an on-premises
cluster, `CLUSTER_REGION` is a logical location and must be one of the values
accepted by `ngc cf cluster create --help`.

Verify the selected account and SAK before changing anything:

```bash
ngc config current
ngc cf cluster list \
  --org "${NGC_ORG}" \
  --team "${NGC_TEAM}" \
  --format_type json
```

## 2. Register the cluster idempotently

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

If `CLUSTER_ID` is empty, register the cluster:

```bash
registration_output="$(mktemp)"
chmod 600 "${registration_output}"
trap 'rm -f "${registration_output}"' EXIT HUP INT TERM

ngc cf cluster create \
  --org "${NGC_ORG}" \
  --team "${NGC_TEAM}" \
  --cluster-name "${CLUSTER_NAME}" \
  --cluster-group-name "${CLUSTER_GROUP_NAME}" \
  --cloud-provider "${CLOUD_PROVIDER}" \
  --region "${CLUSTER_REGION}" \
  --nvca-version "${NVCA_VERSION}" \
  --capability DynamicGPUDiscovery \
  --format_type json >"${registration_output}"

CLUSTER_ID="$(
  ngc cf cluster list \
    --org "${NGC_ORG}" \
    --team "${NGC_TEAM}" \
    --format_type json |
  jq -er --arg name "${CLUSTER_NAME}" \
    '[.[] | select(.clusterName == $name)][0].clusterId'
)"

rm -f "${registration_output}"
trap - EXIT HUP INT TERM
```

Do not pass `--ssa-client-id` for an external organization. The registration
output can contain a generated cluster credential. Keep it out of CI logs and
Git even when this workflow uses the pre-existing SAK instead.

Read the normalized cluster record and retain only the non-secret fields needed
by Helm:

```bash
ngc cf cluster info "${CLUSTER_ID}" \
  --org "${NGC_ORG}" \
  --team "${NGC_TEAM}" \
  --format_type json >cluster-info.json

jq '{
  ncaID,
  clusterID: .clusterId,
  clusterName,
  clusterGroupID: .clusterGroupId,
  clusterGroupName,
  cloudProvider,
  clusterRegion: .region,
  nvcaVersion
}' cluster-info.json
```

Commit those fields to the cluster's Helm values. Do not commit the complete
registration response.

## 3. Materialize the SAK as Kubernetes secrets

Configure the GitOps secret mechanism to create these secrets:

| Secret | Namespace | Required data | Consumer |
| --- | --- | --- | --- |
| `ngc-service-key` | `nvca-operator` | `ngcServiceKey` containing the SAK | NVCA operator |
| `nvca-operator-image-pull` | `nvca-operator` | A `kubernetes.io/dockerconfigjson` credential for `nvcr.io` with username `$oauthtoken` and the SAK as password | Operator and agent images |
| Chart repository credential | GitOps controller namespace | Username `$oauthtoken` and the SAK as password | GitOps Helm source |

Keep all three credentials encrypted or externally materialized. Do not put the
SAK in a Helm values file, command-line `--set` argument, or plain Kubernetes
Secret in Git.

## 4. Commit the Helm values

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

Leaving `ngcConfig.serviceKey` empty prevents Helm from storing the SAK in the
release values. Disabling `generateImagePullSecret` prevents the chart from
requiring the SAK as a Helm value. The two pre-created secrets provide the
runtime API and registry credentials instead.

## 5. Define the GitOps Helm release

Configure the GitOps controller with these Helm source settings:

| Setting | Value |
| --- | --- |
| Repository | `https://helm.ngc.nvidia.com/nvidia/nvcf-byoc` |
| Chart | `nvca-operator` |
| Version | The approved, pinned chart version |
| Release | `nvca-operator` |
| Namespace | `nvca-operator` |
| Values | The file from the previous step |

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

## 6. Verify registration

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

## Related Documentation

- [Helm-Managed Clusters](./helm-managed.md)
- [NGC-Managed Clusters](./ngc-managed.md)
- [Helm Values Reference](./reference.md)
- [Service Keys](../service-keys.md)
