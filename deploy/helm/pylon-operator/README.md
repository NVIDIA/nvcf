# Pylon Operator Helm Chart

This directory contains the Helm chart for Pylon Operator, the Kubernetes
operator that publishes models served in the cluster to the LLM invocation
plane. The operator source is `src/compute-plane-services/pylon-operator`.

## Overview

The operator reconciles `InferenceEndpoint` objects
(`pylon.nvidia.com/v1alpha1`). For each one it probes the backend Service,
resolves the GPU type from node labels, runs Pylon transport pods that register
the model with the LLM request router, and reports status.

The chart installs:

- the `InferenceEndpoint` CRD, when `installCRDs` is true
- the operator Deployment, one replica with leader election
- a ServiceAccount, a ClusterRole and ClusterRoleBinding for the reconciler,
  and a Role and RoleBinding for leader election Leases and Events in the
  release namespace
- a Service for the metrics endpoint
- the cluster credential Secret, unless you supply your own

To run this chart with the LLM gateway stack on a local k3d cluster, see the
[Inference Endpoints quickstart](../../../docs/dev/inference-endpoints-quickstart.md).

## Endpoint canary timing

Pylon checks inference during bringup, recovery, and normal operation. Slow models or a backend with one inference slot can need more time because a canary waits behind an active request. Set `spec.canary` on that `InferenceEndpoint` to override its timeout and check interval:

```yaml
spec:
  canary:
    timeoutSeconds: 180
    intervalSeconds: 60
```

Omitted fields use Pylon's defaults and leave other endpoints unchanged. The timeout must be between 1 and 300 seconds. The interval must be between 1 and 3600 seconds. These settings retain inference checks and the existing generation limit.

## Prerequisites

- Kubernetes cluster and Helm 3.x
- The LLM gateway stack in the same cluster: the `llm-request-router` chart
  and the `llm-api-gateway` chart. Transport pods register with the router's
  gRPC port, `50071` by default.
- The router in static credentials mode, with an entry for this cluster in its
  credentials file. See [Cluster credential](#cluster-credential).
- When a private CA issues the router's TLS certificate, a ConfigMap in the
  release namespace with that CA in key `ca.crt`. See
  [Router TLS trust](#router-tls-trust).
- The operator and Pylon images in a registry the cluster can pull from. The
  chart sets no image repository.

## Install

Create a values file with the required values:

```yaml
image:
  repository: <your-registry>/<your-org>/pylon-operator
clusterId: spark-berlin
router:
  grpcAddress: llm-request-router.nvcf.svc.cluster.local:50071
pylon:
  image:
    repository: <your-registry>/<your-org>/pylon
    tag: <pylon version>
```

Install the release:

```bash
helm install pylon-operator pylon-operator \
  --namespace pylon-operator \
  --create-namespace \
  --values path/to/values.yaml \
  --wait
```

With the release name `pylon-operator`, the generated credential Secret is
`pylon-operator-cluster-credential`, which is also the operator's default. The
release notes print the commands to read the token and its sha256.

Upgrade and uninstall:

```bash
helm upgrade pylon-operator pylon-operator --namespace pylon-operator --values path/to/values.yaml --wait
helm uninstall pylon-operator --namespace pylon-operator
```

## Values

Rendering fails when a required value is empty. `values.schema.json` checks
types, the `clusterId` and namespace formats and the durations.

| Key | Default | Description |
| --- | --- | --- |
| `image.repository` | `""` | Operator image repository. Required. |
| `image.tag` | `""` | Operator image tag. Defaults to the chart `appVersion`. A `sha256:` value is used as a digest. |
| `image.pullPolicy` | `IfNotPresent` | Operator image pull policy. |
| `imagePullSecrets` | `[]` | Pull Secrets for the operator pod. |
| `clusterId` | `""` | Cluster name, a DNS label. Required. `--cluster-id`. |
| `router.grpcAddress` | `""` | Router gRPC address that transport pods register with. Required. `--router-grpc-address`. |
| `pylon.image.repository` | `""` | Pylon transport image repository. Required. |
| `pylon.image.tag` | `""` | Pylon transport image tag or `sha256:` digest. Required. With the repository, `--pylon-image`. |
| `pylon.image.pullPolicy` | `IfNotPresent` | Transport container pull policy. `--pylon-image-pull-policy`. |
| `watchNamespaces` | `[]` | Namespaces to watch. Empty watches all. `--watch-namespaces`, comma-separated. |
| `transport.replicas` | `1` | Replicas of each transport Deployment. `--transport-replicas`. |
| `transport.initialInputTPS` | `100` | Input tokens per second Pylon assumes until it has measured the backend. `--initial-input-tps`. |
| `probeInterval` | `10s` | Backend health probe period. `--probe-interval`. |
| `scrapeInterval` | `5s` | Transport metrics scrape period. `--scrape-interval`. |
| `devInsecureTransport` | `false` | Adds `--dev-insecure-transport`, which runs transport pods with `--quic-insecure`. Local k3d clusters only. |
| `leaderElection.enabled` | `true` | Adds `--leader-elect`. When false, the Deployment uses the `Recreate` strategy. |
| `credential.existingSecret` | `""` | Existing Secret with the cluster credential. When set, the chart creates no Secret. |
| `credential.key` | `cluster-token` | Secret key that holds the token. The operator reads only `cluster-token`, so any other value fails the render. |
| `credential.generate` | `true` | Generate `<fullname>-cluster-credential` when `existingSecret` is empty. |
| `trustBundle.configMap` | `""` | Existing ConfigMap in the release namespace whose `ca.crt` key holds the router CA bundle. Empty mounts none. `--trust-bundle-configmap`. |
| `installCRDs` | `true` | Render the `InferenceEndpoint` CRD. |
| `logLevel` | `info` | `--zap-log-level`. |
| `extraArgs` | `[]` | Extra operator arguments, appended last. |
| `serviceAccount.create` | `true` | Create the operator ServiceAccount. Its token is mounted. |
| `serviceAccount.name` | `""` | ServiceAccount name. Defaults to the release full name. |
| `serviceAccount.annotations` | `{}` | ServiceAccount annotations. |
| `metrics.port` | `8080` | Metrics port and Service port. `--metrics-bind-address`. |
| `healthProbe.port` | `8081` | `/healthz` and `/readyz` port. `--health-probe-bind-address`. |
| `resources` | 50m CPU, 64Mi request; 256Mi limit | Operator container resources. |
| `podAnnotations` | `{}` | Pod annotations. |
| `podSecurityContext` | non-root, `RuntimeDefault` seccomp | Pod security context. |
| `securityContext` | non-root uid 1000, read-only root, drop `ALL` | Container security context. |
| `nodeSelector`, `tolerations`, `affinity` | empty | Pod scheduling. |

The operator always receives `--cluster-credential-secret` with the Secret name
the chart resolved. `POD_NAMESPACE`, set from the downward API, gives the
operator its own namespace (`--operator-namespace`), where it reads the
credential Secret and the trust bundle ConfigMap.

## Cluster credential

Transport pods present a bearer token to the router on every registration
stream. The router stores only the token's sha256 and checks it against the
cluster id the pod registers under.

1. The chart creates Secret `<fullname>-cluster-credential` with key
   `cluster-token` and a random 48 character value on first install. Upgrades
   keep the value: the template reads the installed Secret with `lookup`. The
   Secret carries `helm.sh/resource-policy: keep`, so uninstall leaves it and
   a reinstall under the same release name reuses it.
2. The operator reads the Secret in its own namespace and replicates it into
   each namespace that has an `InferenceEndpoint`, where the transport pods
   mount it. While the Secret or its `cluster-token` key is missing, the
   operator emits Warning Events with reason `ClusterCredentialMissing` and
   creates no transport Deployments.
3. Add the entry `sha256:<hex SHA-256 of the token>` under the cluster id in
   the router's worker auth file Secret, key `credentials.yaml` (see the
   llm-request-router chart README for the format and rotation):

   ```bash
   TOKEN_HASH="sha256:$(kubectl -n pylon-operator get secret pylon-operator-cluster-credential \
     -o jsonpath='{.data.cluster-token}' | base64 -d | shasum -a 256 | cut -d' ' -f1)"
   printf 'clusters:\n  spark-berlin:\n    - %s\n' "${TOKEN_HASH}" > credentials.yaml
   kubectl -n nvcf create secret generic llm-request-router-worker-credentials \
     --from-file=credentials.yaml
   ```

4. Point the router chart at that Secret:

   ```yaml
   llmRequestRouter:
     auth:
       workerAuthEndpoint: ""
       credentialsSecret:
         name: llm-request-router-worker-credentials
   ```

To supply your own token, create the Secret before installing and set
`credential.existingSecret`. Tools that render charts without cluster access,
such as Argo CD, cannot run `lookup` and would generate a new token on every
render. Use `credential.existingSecret` with them.

To rotate the token, write the new value into the Secret and update the hash
in the router's credentials file. Pylon re-reads the token file and the router
re-reads its credentials file, so no pod restarts are needed.

Until the router trusts the hash, InferenceEndpoints report `Registered=False`
with reason `RegistrationRejected`.

## Router TLS trust

Transport pods verify the router's TLS certificate on the QUIC tunnel, and on
the gRPC registration stream when `router.grpcAddress` starts with `https://`.
A bare `host:port` or an `http://` address is plaintext gRPC, which is what the
`llm-request-router` chart serves on port `50071`. When a private CA, such as
the cert-manager issuer of the `llm-request-router` chart, signs that
certificate, give the pods its CA:

```bash
kubectl -n pylon-operator create configmap router-ca --from-file=ca.crt=path/to/ca.crt
```

```yaml
trustBundle:
  configMap: router-ca
```

The operator replicates the ConfigMap next to each transport Deployment and
mounts it. While it or its `ca.crt` key is missing, the operator emits Warning
Events with reason `TrustBundleMissing` and creates no transport Deployments.
`devInsecureTransport` skips verification instead, for local k3d clusters
only.

## CRD and RBAC

`templates/crds/pylon.nvidia.com_inferenceendpoints.yaml` and
`templates/clusterrole.yaml` are copies of the operator's generated
`config/crd/bases/pylon.nvidia.com_inferenceendpoints.yaml` and
`config/rbac/role.yaml`. `scripts/sync-crd.sh` writes them and adds only the
chart wrapping: the `installCRDs` gate, chart labels, the keep policy on the
CRD and the release-scoped ClusterRole name. Do not edit them by hand.

After `make codegen-update` in the operator module:

```bash
make -C deploy/helm/pylon-operator sync-crd   # copy the CRD and ClusterRole
make -C deploy/helm/pylon-operator check-crd  # fail when the chart has drifted
```

The ClusterRole is bound cluster-wide even when `watchNamespaces` is set,
because Nodes are cluster-scoped and the generated role has no namespace
split. It includes create and update on Secrets, ConfigMaps and Deployments,
which the operator needs to replicate the credential and trust bundle and to
run transport Deployments. The operator caches only the Secrets and ConfigMaps
with the configured names and the Deployments it labels.

## Upgrade notes

The CRD is a regular template, not a file in the chart's `crds/` directory, so
`helm upgrade` applies CRD changes.

The CRD carries `helm.sh/resource-policy: keep`. Deleting the CRD deletes every
`InferenceEndpoint` and, through owner references, every transport Deployment,
which unpublishes every model. The keep policy prevents that on:

- `helm uninstall`
- an upgrade that sets `installCRDs=false`

In both cases the CRD stays in the cluster and Helm stops managing it. Setting
`installCRDs=true` again adopts it back into the release. To remove the CRD,
delete it with `kubectl delete crd inferenceendpoints.pylon.nvidia.com`.

Set `installCRDs=false` on first install only when another tool manages the
CRD.

## Development

From the repository root:

```bash
make -C deploy/helm/pylon-operator lint       # helm lint with the CI values
make -C deploy/helm/pylon-operator template   # render to bin/manifest.yaml
make -C deploy/helm/pylon-operator validate   # kubeconform, when installed
make -C deploy/helm/pylon-operator test       # scripts/check-render.sh
make -C deploy/helm/pylon-operator check-crd  # CRD and ClusterRole drift
tools/ci/check-helm-charts                    # every chart with CI values
```

The CI values are in `tools/ci/helm-validate-values/pylon-operator.yaml`.
