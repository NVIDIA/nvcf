# NVIDIA Cloud Functions Agent (NVCA)

<!-- Review-routing touchpoint for compute-plane-owned NVCA docs. -->

NVCA is a Kubernetes agent that manages NVCF Bring-Your-Own-Compute (BYOC) Kubernetes clusters. This repository also contains the **NVCA Operator**, which automates the deployment and lifecycle management of NVCA.

## Overview

The NVCA agent handles workload scheduling, queue message processing, storage management, and lifecycle operations for NVIDIA Cloud Functions. It enables organizations to run serverless GPU workloads on their own Kubernetes infrastructure while integrating with NVIDIA's cloud services.

Key features include queue-based workload management via SQS/NATS, MiniService controller for Helm chart lifecycle, storage request handling, GPU resource discovery, and comprehensive Prometheus metrics for observability.

The NVCA Operator automates deployment, upgrades, and health monitoring of NVCA, significantly reducing operational overhead. It provides seamless integration with NVIDIA Cloud services via the NGC platform, customizable Helm-based deployment, support for custom network policies, and secure credential management.

## Configuration

### NVCA Attributes and Feature Flags

For information on available cluster-wide attributes and feature flags,
and how to enable/disable them, see [Feature Flags Documentation](./docs/users/byoc/featureflags.md).

### Operator Cluster Configuration Keys

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

## Deploying

### NVIDIA Managed NVCF (BYOC)

Follow the [official docs](https://docs.nvidia.com/cloud-functions/user-guide/latest/cloud-function/cluster-management.html) to register your cluster and deploy the operator.

### Self-Hosted Control Plane

Enable self-hosted on an already running cluster:

```bash
helm upgrade nvca-operator -n nvca-operator --create-namespace -i --reset-values \
  ./deployments/nvca-operator \
  --set ngcConfig.serviceKey=${NGC_KEY} \
  --set ngcConfig.clusterSource=self-managed \
  --set selfManaged.nvcaVersion=${NVCA_VERSION}
```

### Local Kind Cluster

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
  "https://helm.ngc.nvidia.com/qtfpt1h0bieu/byoc/charts/nvca-operator-${OPERATOR_VERSION}.tgz" \
  -f values.yaml \
  --username="\$oauthtoken" \
  --password="${CLUSTER_KEY}" \
  --set ngcConfig.serviceKey="${CLUSTER_KEY}" \
  --set ncaID="${NCA_ID}" \
  --set clusterID="${CLUSTER_ID}" \
  --set-file 'networkPolicy.customPolicies={allow-all-ingress.yaml}'
```

## Building and Testing

NVCA builds via Bazel. The legacy Dockerfile and `goreleaser` paths are
retired; CI runs `bazel test //...` and publishes images via
`bazel run //cmd/<bin>:image_push`.

### Bazel

Requires [bazelisk](https://github.com/bazelbuild/bazelisk) (`bazel` on PATH
delegating to the version pinned in `.bazelversion`). MODULE.bazel pulls
the Go toolchain (1.25.0), `rules_go`, Gazelle, `rules_oci`, and the
distroless Go base image — no host toolchains are needed beyond a recent
Linux (or macOS) and the `bazel` shim.

OSS contributors building from the GitHub mirror: the default `oci.pull`
in `MODULE.bazel` points at `urm.nvidia.com/sw-gpu-ucs-hardened-docker/distroless/go`,
which is NVIDIA-internal Artifactory. To build off-network, swap that
entry for a public base such as `gcr.io/distroless/static-debian12` (then
`bazel mod tidy` to refresh the lockfile). `bazel build //...` and
`bazel test //...` work without modification.

Common commands:

```sh
# Build every Bazel target.
bazel build //...

# Build the four OCI images (multi-arch index format):
bazel build //cmd/nvca:image_index \
            //cmd/nvca-operator:image_index \
            //cmd/cluster-validator:image_index \
            //cmd/tools:image_index

# Run all unit + envtest-backed tests:
eval "$(./scripts/setup_envtest)"  # exports KUBEBUILDER_ASSETS
bazel test //... --test_env=KUBEBUILDER_ASSETS

# Re-run Gazelle after adding new Go imports or files. Updates
# srcs / deps lists and refreshes go_deps overrides in MODULE.bazel.
bazel run @rules_go//go -- mod tidy
bazel run @rules_go//go -- mod vendor
find vendor -name BUILD.bazel -delete
bazel run //:gazelle
bazel mod tidy

# Load an image into the local Docker daemon for ad-hoc testing:
bazel run //cmd/nvca:image_load
```

Push targets live under `//nvidia-internal:image_push*` (see
`nvidia-internal/BUILD.bazel`); the registry coordinates are
NVIDIA-internal and intentionally kept out of the public mirror.
Credentials come from the Docker config that `DOCKER_CONFIG` points
at — CI fills these from vault-fetched tokens.

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

When running under Bazel, the `storage_test` and `miniservice_test`
targets inherit `KUBEBUILDER_ASSETS` via `env_inherit` and run with
`rundir = "."`; you must export it (via `setup_envtest`) in the shell
where you launch `bazel test`.

### Monitoring and Metrics

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
