[![license](https://img.shields.io/github/license/NVIDIA/nvca?style=flat-square)](https://raw.githubusercontent.com/NVIDIA/nvca/main/LICENSE) ![GitHub Release](https://img.shields.io/github/v/release/nvidia/nvca)

#### [Getting Started](#getting-started) | [Local Development](#local-development) | [NVCA Operator](#nvca-operator) | [Monitoring & Metrics](#monitoring-and-metrics) | [Releases & Roadmap](#releases--roadmap) | [Contribution Guidelines](#contribution-guidelines) | [Governance & Maintainers](#governance--maintainers)


# NVIDIA Cloud Functions Agent (NVCA)

NVCA is a Kubernetes agent that manages NVCF Bring-Your-Own-Compute (BYOC) Kubernetes clusters. This repository also contains the **NVCA Operator**, which automates the deployment and lifecycle management of NVCA.

## Overview

The NVCA agent handles workload scheduling, queue message processing, storage management, and lifecycle operations for NVIDIA Cloud Functions. It enables organizations to run serverless GPU workloads on their own Kubernetes infrastructure while integrating with NVIDIA's cloud services.

Key features include queue-based workload management via SQS/NATS, MiniService controller for Helm chart lifecycle, storage request handling, GPU resource discovery, and comprehensive Prometheus metrics for observability.

The NVCA Operator automates deployment, upgrades, and health monitoring of NVCA, significantly reducing operational overhead. It provides seamless integration with NVIDIA Cloud services via the NGC platform, customizable Helm-based deployment, support for custom network policies, and secure credential management.

## Getting Started

In order to set up NVCA, you must have a Kubernetes cluster with GPU support. Follow the [official docs](https://docs.nvidia.com/cloud-functions/user-guide/latest/cloud-function/cluster-management.html#cluster-setup-management) for complete setup instructions.

### Attributes and Feature Flags

For information on available cluster-wide attributes and feature flags,
and how to enable/disable them, see [Feature Flags Documentation](./docs/users/byoc/featureflags.md).

## Local Dev Env Setup

`int_shell` provides a shell enviroment to replay each individual CI stages
within a local containerized environment. Private gitlab repo and container
registry access are required to use `./scripts/int_shell`. To configure:

### 1. GitLab Access

- Create and obtain your gitlab access token at
  https://github.com/NVIDIA/-/profile/personal_access_tokens, make sure it
  at least have `read_repository` permission.

- Either create a `.secrets` file under project root, add following into `.secrets`:

```
GITLAB_USER=<NVIDIA user name>
GITLAB_TOKEN=<Gitlab access token from github.com/NVIDIA>
```

- Or add the GitLab entry to your `~/.netrc` file:

```
machine github.com/NVIDIA
login <NVIDIA user name>
password <Gitlab access token from github.com/NVIDIA>
```

### 2. GitHub Access (required for private NVIDIA dependencies)

This repository depends on private modules hosted under `github.com/NVIDIA/` (e.g., `nvcf-go`, `nvcf-icms-translate`).
You must configure a GitHub Personal Access Token (classic) so `go mod` can fetch these dependencies.

1. Create a **classic** token at https://github.com/settings/tokens with the `repo` scope (full control of private repositories).
2. After creating the token, click **Configure SSO** next to it and **authorize** it for the **NVIDIA** organization.
3. Add the following entries to your `~/.netrc` file:

```
machine github.com
login <GitHub username>
password <GitHub classic PAT>

machine api.github.com
login <GitHub username>
password <GitHub classic PAT>
```

4. Set `GOPRIVATE` so Go knows to use authenticated access for NVIDIA modules:

```bash
export GOPRIVATE="github.com/NVIDIA/*,github.com/NVIDIA/*"
```

> **Tip:** Add the `GOPRIVATE` export to your shell profile (`~/.bashrc`, `~/.zshrc`, etc.) so it persists across sessions.

- Login gitlab registry with LDAP username and password:

```
docker login github.com/NVIDIA:5005
```

- Confirm access:

```
docker pull :latest
```

### Envtest

Some tests in the NVCA repository require [envtest]() to run.
The [`setup_envtest`](./scripts/setup_envtest) script will do this automatically, in both CI and on `make test`.

To configure VSCode or clones like Cursor so you can run these tests directly in your editor,
ensure the `KUBEBUILDER_ASSETS` environment variable is set to the output of the `setup_envtest` script.

For example, in VSCode `settings.json` add:

```json
	"go.testEnvVars": {
		"KUBEBUILDER_ASSETS": "/home/myuser/.local/share/kubebuilder-envtest/k8s/current"
	}
```

## NVCA Operator

The NVCA Operator installs and manages the lifecycle of NVCA in DGX Cloud or any Kubernetes environment.

### Deploying the Operator

#### NVIDIA Managed NVCF (BYOC)

Follow the [official docs](https://docs.nvidia.com/cloud-functions/user-guide/latest/cloud-function/cluster-management.html) to register your cluster and deploy the operator.

#### Self-Hosted Control Plane

```bash
helm upgrade nvca-operator -n nvca-operator --create-namespace -i --reset-values \
  ./deployments/nvca-operator \
  --set ngcConfig.serviceKey=<key> \
  --set ngcConfig.clusterSource=self-managed \
  --set selfManaged.nvcaVersion=<version>
```

### Local Development (Kind Cluster)

The typical cluster layout is 2 GPU worker nodes, 1 control-plane node, and a monitoring node. See [test/kind-env](./test/kind-env/) for pre-baked environments.

1. Setup Kind cluster

```bash
kind create cluster --image kindest/node:"${K8S_VERSION:-"v1.32.8"}" --config test/kind-env/r750x2-h100x8/kind-config.yaml
```

2. Install fake-gpu-operator

```bash
helm repo add fake-gpu-operator https://runai.jfrog.io/artifactory/api/helm/fake-gpu-operator-charts-prod --force-update
helm repo update
helm upgrade -i gpu-operator fake-gpu-operator/fake-gpu-operator --namespace gpu-operator --create-namespace --values test/kind-env/r750x2-h100x8/fake-gpu-values.yaml
```

3. Install SMB CSI Driver

```bash
helm repo add csi-driver-smb https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts
helm upgrade -i csi-driver-smb csi-driver-smb/csi-driver-smb --namespace kube-system --version v1.17.0 --set 'controller.nodeSelector.nodeGroup=monitoring'
```

4. Register the cluster at [NVCF Settings](https://nvcf.ngc.nvidia.com/settings) and install with the provided command, adding:

```
--set 'nodeSelector.key=nodeGroup' --set 'nodeSelector.value=monitoring'
```

5. Run E2E tests

```bash
make dev-shell
make test-e2e
```

> **Note:** Ensure there are no overriding localhost DNS docker configurations as that would cause the e2e tests to fail.

### Force NVCA Rollout

Force a rollout of NVCA by updating the timestamp annotation on the NVCFBackend resource:

```bash
kubectl annotate nvcfbackends --all -n nvca-operator --overwrite nvca.nvcf.nvidia.io/forcedRolloutAt="$(date)"
```

### Custom Network Policies

Add custom network policies during installation using the `--set-file` flag. These policies are added to each function namespace.

Example — create `allow-all-ingress.yaml`:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-all-ingress
spec:
  podSelector: {}
  policyTypes:
    - Ingress
  ingress:
    - {}
```

Then include it during installation:

```sh
helm upgrade nvca-operator -n nvca-operator --create-namespace -i --reset-values --wait \
  "https://helm.ngc.nvidia.com/qtfpt1h0bieu/byoc/charts/nvca-operator-<operator-version>.tgz" \
  -f values.yaml \
  --username="\$oauthtoken" \
  --password="$CLUSTER_KEY" \
  --set ngcConfig.serviceKey="$CLUSTER_KEY" \
  --set ncaID="<nca-id>" \
  --set clusterID="<cluster-id>" \
  --set-file 'networkPolicy.customPolicies={allow-all-ingress.yaml}'
```

### Cluster Configuration Keys

The operator supports cluster-level configuration options set via the NGC API during cluster registration. For the complete list, see [`pkg/operator/reconcile/clustermgmt/types.go`](pkg/operator/reconcile/clustermgmt/types.go).

| Configuration Key | Description |
|------------------|-------------|
| `AgentNodeSelectorLabelKey` | Label key for node selector to schedule NVCA agents |
| `AgentNodeSelectorLabelValue` | Label value for node selector to schedule NVCA agents |
| `AgentPriorityClassName` | Priority class name for NVCA agent pods |
| `ModelCacheVolumeMountOptionEnabled` | Enables custom mount options for model cache volumes |
| `ModelCacheVolumeMountOptions` | Mount options (e.g., `vers=3.0,dir_mode=0777`) for model cache volumes |
| `ClusterNetworkCIDRAllowedRange` | Allowed CIDR ranges for cluster network access |
| `NVCFWorkerDegradationPeriodMinutes` | Time before a worker is considered degraded |
| `NVCASecretMirrorSourceNamespace` | Source namespace for secret mirroring to function namespaces |
| `NVCASecretMirrorLabelSelector` | Label selector for secrets to mirror to function namespaces |

## Monitoring and Metrics

NVCA exposes Prometheus metrics for monitoring queue operations, instance capacity, container health, and more.

For complete metrics documentation including metric types, labels, usage examples, and alerting recommendations, see [internal/metrics/METRICS.md](internal/metrics/METRICS.md).

## Debugging NVCA

NVCA exposes the [`pprof` endpoint set](https://pkg.go.dev/net/http/pprof) for analyzing profile data at runtime.
To access these data, a convenience script is provided to download a particular profile
then visualize it with the `pprof` tool., The script requires `kubectl`, `go`, and sufficient privileges
to exec into a pod in the "nvca-system" namespace.

For example, to profile NVCA's heap:

```sh
./scripts/users/nvca-pprof.sh heap
```

## Enable Starship Terminal Prompt

To enable the [Starship] prompt perform the following:

- Set the env var `EGX_STARSHIP_ENABLED` to a non-empty value (e.g. `true`, `false`, `jensenIsAwesome`)
- Install a [nerd font](https://techviewleo.com/install-nerd-fonts-on-linux-macos/)
  from the [nerd fonts repo](https://github.com/ryanoasis/nerd-fonts)
  to ensure all terminal icons function properly. If you cannot decide which font
  go with `Meslo` as a reasonable default.

## Releases & Roadmap

- [Release Process](./RELEASE.md)
- Roadmap coming soon

## Contribution Guidelines

- [Contributing](./CONTRIBUTING.md)
- [Code of Conduct](./CODE_OF_CONDUCT.md)

## Governance & Maintainers

- Maintainers: nvidia/nvca-dev
