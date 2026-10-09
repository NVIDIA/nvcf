# LLM Gateway Stack Helm Chart

This directory contains `llm-gateway-stack`, an umbrella chart that installs the
LLM API gateway and the LLM request router in one namespace for a
single-cluster deployment with Pylon Operator. The defaults need no Vault, no
cert-manager and no NVCF API.

## Overview

The chart installs:

- the `llm-request-router` chart (alias `llm-request-router`) in static
  credentials mode: it verifies the cluster token that Pylon transport pods
  present against SHA-256 hashes in its worker auth file, with no Vault Agent
- the `llm-api-gateway` chart (alias `llm-api-gateway`) in `staticKeys` mode
  with bare model names: it verifies caller API keys against SHA-256 digests,
  serves the model list and registry without a key, serves HTTPS, and runs
  without the NVCF API or Vault
- Secret `llm-gateway-stack-worker-credentials`, key `credentials.yaml`, the
  router's worker auth file:

  ```yaml
  clusters:
    <clusterId>:
    - sha256:<64 hex>
  ```

- Secret `llm-gateway-stack-api-keys`, the gateway's key file
  `{"keys": [{"id": "<id>", "sha256": "<sha256>"}]}`

- with `tls.selfSigned.enabled` (default), a CA and two certificates it signs:
  Secret `llm-gateway-stack-ca` (the CA, kept on uninstall), ConfigMap
  `llm-gateway-stack-ca` (the CA certificate in key `ca.crt`), Secret
  `llm-gateway-stack-router-tls` (router QUIC) and Secret
  `llm-gateway-stack-gateway-tls` (gateway listener)

The subcharts are `file://` dependencies on the sibling chart sources
`deploy/helm/llm-request-router/llm-request-router` and
`deploy/helm/llm-api-gateway/llm-api-gateway`. Run `helm dependency build`
before installing from a checkout; the `make` targets do this.

Monitoring (an OpenTelemetry collector and Grafana) and the demo UI are not
part of the chart yet.

To run this chart with Pylon Operator on a local k3d cluster, see the
[Inference Endpoints quickstart](../../../docs/dev/inference-endpoints-quickstart.md).

## Prerequisites

- Kubernetes cluster and Helm 3.x
- The request router and gateway images in a registry the cluster can pull
  from. The chart sets no image registry or repository. The router image must
  support `--worker-auth-file` with the YAML worker auth file, and the gateway image must support
  `API_KEYS_PATH`, `PUBLIC_READ_ENDPOINTS` and `TLS_CERT_FILE`.
- The Pylon Operator chart, `deploy/helm/pylon-operator`, installed first: its
  generated cluster token is an input to this chart.

## Install

The steps install both charts in namespace `llm-gateway`. One namespace lets the
operator read this chart's CA ConfigMap directly; see
[Router TLS trust](#router-tls-trust) for separate namespaces.

1. Install Pylon Operator. Point it at the router Service this chart creates
   and at the CA ConfigMap it will create:

   ```yaml
   # pylon-operator-values.yaml
   image:
     repository: <your-registry>/<your-org>/pylon-operator
   clusterId: spark-berlin
   router:
     grpcAddress: llm-request-router.llm-gateway.svc.cluster.local:50071
   pylon:
     image:
       repository: <your-registry>/<your-org>/pylon
       tag: <pylon version>
   trustBundle:
     configMap: llm-gateway-stack-ca
   ```

   ```bash
   helm install pylon-operator deploy/helm/pylon-operator/pylon-operator \
     --namespace llm-gateway --create-namespace \
     --values pylon-operator-values.yaml --wait
   ```

   Until this chart creates the CA ConfigMap, the operator reports Warning
   Events with reason `TrustBundleMissing` and creates no transport pods.

2. Read the generated cluster token and compute its SHA-256. Only the digest
   goes into this chart:

   ```bash
   TOKEN_SHA256="$(kubectl -n llm-gateway get secret pylon-operator-cluster-credential \
     -o jsonpath='{.data.cluster-token}' | base64 -d | shasum -a 256 | cut -d' ' -f1)"
   ```

3. Create an API key for callers and compute its SHA-256. Keep the key; the
   chart stores only the digest:

   ```bash
   API_KEY="$(openssl rand -hex 32)"
   API_KEY_SHA256="$(printf '%s' "${API_KEY}" | shasum -a 256 | cut -d' ' -f1)"
   ```

4. Install the stack with the same `clusterId` as the operator:

   ```yaml
   # llm-gateway-stack-values.yaml
   clusterId: spark-berlin
   llm-request-router:
     llmRequestRouter:
       image:
         registry: <your-registry>
         repository: <your-org>/stargate
   llm-api-gateway:
     llmApiGateway:
       image:
         registry: <your-registry>
         repository: <your-org>/llm-api-gateway
   ```

   ```bash
   helm dependency build deploy/helm/llm-gateway-stack/llm-gateway-stack
   helm install llm-gateway-stack deploy/helm/llm-gateway-stack/llm-gateway-stack \
     --namespace llm-gateway \
     --values llm-gateway-stack-values.yaml \
     --set clusterCredential.sha256="${TOKEN_SHA256}" \
     --set 'apiKeys[0].id=demo' \
     --set "apiKeys[0].sha256=${API_KEY_SHA256}" \
     --wait
   ```

   The release notes print the router address and trust bundle settings for
   the operator and a `curl` command for the gateway.

5. Call the gateway with the key and the CA:

   ```bash
   kubectl -n llm-gateway get configmap llm-gateway-stack-ca -o jsonpath='{.data.ca\.crt}' > ca.crt
   kubectl -n llm-gateway port-forward svc/llm-api-gateway 8080:8080
   curl --cacert ca.crt https://localhost:8080/v1/chat/completions \
     -H "Authorization: Bearer ${API_KEY}" \
     -H 'Content-Type: application/json' \
     -d '{"model": "<modelName of an InferenceEndpoint>", "messages": [{"role": "user", "content": "Hello"}]}'
   ```

Upgrade and uninstall:

```bash
helm upgrade llm-gateway-stack deploy/helm/llm-gateway-stack/llm-gateway-stack \
  --namespace llm-gateway --values llm-gateway-stack-values.yaml --reuse-values --wait
helm uninstall llm-gateway-stack --namespace llm-gateway
```

## Values

| Value | Default | Description |
| --- | --- | --- |
| `clusterId` | `""` | Cluster id, a DNS label. Required while `clusterCredential.create` is true. Use the operator's `clusterId` |
| `clusterCredential.create` | `true` | Render the router worker auth file Secret from `clusterId`, `clusterCredential.sha256` and `clusterCredential.sha256Hashes` |
| `clusterCredential.sha256` | `""` | Hex SHA-256 of the operator's cluster token, 64 hex characters with or without the `sha256:` prefix. A single-hash convenience |
| `clusterCredential.sha256Hashes` | `[]` | More hashes in the same forms, merged after `sha256`. At least one hash from either value is required while `create` is true; a hash may appear once. Use during rotation |
| `apiKeys` | `[]` | Caller keys as `{id, sha256}`. At least one is required while `apiKeysSecret.create` is true |
| `apiKeysSecret.create` | `true` | Render the gateway key file Secret from `apiKeys` |
| `demoUiApiKey` | `""` | Create the demo UI key Secret. Requires `apiKeysSecret.create=true` and a matching `demo-ui` hash in `apiKeys` |
| `tls.selfSigned.enabled` | `true` | Generate the CA and the router and gateway certificates |
| `tls.selfSigned.validityDays` | `3650` | Validity of the generated CA and certificates |
| `tls.selfSigned.caName` | `llm-gateway-stack-ca` | Name of the CA Secret and of the CA ConfigMap (key `ca.crt`) |
| `tls.selfSigned.gatewayDnsNames` | `["localhost"]` | Extra DNS names on the gateway certificate. The Service names are always included |
| `tls.selfSigned.gatewayIPs` | `["127.0.0.1"]` | Extra IP addresses on the gateway certificate |
| `tls.selfSigned.routerDnsNames` | `[]` | Extra DNS names on the router certificate |
| `llm-request-router.*` | see below | Values of the `llm-request-router` chart |
| `llm-api-gateway.*` | see below | Values of the `llm-api-gateway` chart |

The stack sets these subchart values. The Secret names are where the stack
reads the names of the Secrets it renders, so change them there.

| Subchart value | Stack default |
| --- | --- |
| `llm-request-router.llmRequestRouter.replicaCount` | `1` |
| `llm-request-router.llmRequestRouter.auth.workerAuthEndpoint` | `""` |
| `llm-request-router.llmRequestRouter.auth.credentialsSecret.name` | `llm-gateway-stack-worker-credentials` |
| `llm-request-router.llmRequestRouter.vault.noVaultAnnotations` | `true` |
| `llm-request-router.llmRequestRouter.tls.mode` | `existingSecret` |
| `llm-request-router.llmRequestRouter.tls.secretName` | `llm-gateway-stack-router-tls` |
| `llm-request-router.llmRequestRouter.tls.quicInsecure` | `false` |
| `llm-api-gateway.llmApiGateway.replicaCount` | `1` |
| `llm-api-gateway.llmApiGateway.config.requestRouterUrl` | `http://llm-request-router:8000` |
| `llm-api-gateway.llmApiGateway.config.bareModelNamesEnabled` | `true` |
| `llm-api-gateway.llmApiGateway.config.publicReadEndpoints` | `true` |
| `llm-api-gateway.llmApiGateway.config.rateLimitEnabled` | `false` |
| `llm-api-gateway.llmApiGateway.olric.enabled` | `false` |
| `llm-api-gateway.llmApiGateway.auth.mode` | `staticKeys` |
| `llm-api-gateway.llmApiGateway.auth.staticKeys.existingSecret` | `llm-gateway-stack-api-keys` |
| `llm-api-gateway.llmApiGateway.vault.enabled` | `false` |
| `llm-api-gateway.llmApiGateway.tls.enabled` | `true` |
| `llm-api-gateway.llmApiGateway.tls.existingSecret` | `llm-gateway-stack-gateway-tls` |

Both subcharts must run in the release namespace; setting their `namespace`
value to another namespace fails the render. The image tags are pinned in this
chart's `values.yaml` as well as in the subcharts, so a release of either image
moves this chart too.

## Router TLS trust

Pylon transport pods verify the router's QUIC certificate against the CA in
the operator's `trustBundle.configMap`, which must be in the operator's
namespace. With both charts in one namespace, set
`trustBundle.configMap: llm-gateway-stack-ca` in the operator values, as in the
install steps.

When the operator runs in another namespace, copy the CA ConfigMap there and
point `trustBundle.configMap` at the copy:

```bash
kubectl -n llm-gateway get configmap llm-gateway-stack-ca -o jsonpath='{.data.ca\.crt}' > ca.crt
kubectl -n pylon-operator create configmap llm-gateway-stack-ca --from-file=ca.crt=ca.crt
```

Repeat the copy whenever the CA changes. Pylon does not reload its trust
bundle, so restart the transport Deployments after a CA change.

## Certificates

The self-signed certificates cover the in-cluster names:

- router: `llm-request-router`, `llm-request-router-backend-router` and their
  `.<namespace>`, `.<namespace>.svc` and `.<namespace>.svc.cluster.local`
  forms, and `*.llm-request-router-headless.<namespace>.svc.cluster.local`
  for the per-pod names of a multi-replica router
- gateway: `llm-api-gateway` in the same four forms, plus
  `tls.selfSigned.gatewayDnsNames` and `tls.selfSigned.gatewayIPs`

Each server Secret records its names in the annotation
`llm-gateway-stack.nvidia.com/subject-alt-names`.

Upgrades keep the installed CA and certificates: the templates read them with
`lookup`. A server certificate is issued again only when its names change or
the CA Secret is gone. The CA Secret carries `helm.sh/resource-policy: keep`,
so it survives uninstall, and a reinstall under the same release name and
namespace adopts it and keeps the trust transport pods already have. To issue a new CA, delete Secret
`llm-gateway-stack-ca` and upgrade.

The router and the gateway both reload their certificates without a restart;
the gateway checks every 30 seconds.

Tools that render charts without cluster access, such as Argo CD, cannot run
`lookup` and would issue a new CA on every render. With them, and whenever
cert-manager or another issuer should own the certificates, set
`tls.selfSigned.enabled=false` and create the two `kubernetes.io/tls` Secrets
yourself under the names in `llm-request-router.llmRequestRouter.tls.secretName`
and `llm-api-gateway.llmApiGateway.tls.existingSecret`. The chart then renders
no TLS Secret and no CA ConfigMap. For example, with a cert-manager issuer:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: llm-gateway-stack-router-tls
  namespace: llm-gateway
spec:
  secretName: llm-gateway-stack-router-tls
  dnsNames:
    - llm-request-router.llm-gateway.svc.cluster.local
    - llm-request-router-backend-router.llm-gateway.svc.cluster.local
    - "*.llm-request-router-headless.llm-gateway.svc.cluster.local"
  issuerRef:
    kind: Issuer
    name: <your-issuer>
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: llm-gateway-stack-gateway-tls
  namespace: llm-gateway
spec:
  secretName: llm-gateway-stack-gateway-tls
  dnsNames:
    - llm-api-gateway.llm-gateway.svc.cluster.local
  issuerRef:
    kind: Issuer
    name: <your-issuer>
```

Give the operator the issuer's CA through its `trustBundle.configMap`.

## Rotation

- Cluster token, without a registration gap:
  1. Upgrade this release with the new token's hash added to
     `clusterCredential.sha256Hashes`. The rendered file then lists the old and
     the new hash, and the router accepts both once it re-reads the file
     (every 30 seconds, after kubelet projects the Secret update).
  2. Write the new token into the operator's credential Secret. Pylon re-reads
     it and reconnects with the new token.
  3. Upgrade again with only the new hash, for example as
     `clusterCredential.sha256` with `sha256Hashes` emptied.

  No pod restarts. A hash may appear only once across `sha256` and
  `sha256Hashes`; the render fails otherwise.
- API keys: upgrade with the changed `apiKeys`. The gateway re-reads its key
  file every 30 seconds; a file that fails to load keeps the previous keys.
- To manage either Secret outside this chart, set `clusterCredential.create` or
  `apiKeysSecret.create` to false and create the Secret under the name in the
  subchart values. When managing the API key Secret outside the chart, also
  provide the demo UI key outside the chart; `demoUiApiKey` requires the
  chart-managed API key Secret.

## Development

From the repository root:

```bash
make -C deploy/helm/llm-gateway-stack dependency-build   # package the file:// subcharts into charts/
make -C deploy/helm/llm-gateway-stack lint               # helm lint with the CI values
make -C deploy/helm/llm-gateway-stack template           # render to bin/manifest.yaml
make -C deploy/helm/llm-gateway-stack test               # scripts/check-render.sh
make -C deploy/helm/llm-gateway-stack dependency-update  # regenerate Chart.lock after changing dependencies
```

`charts/` is ignored by git and rebuilt from the sibling chart sources, so the
render checks always test the current subchart templates. `Chart.lock` is
committed. The CI values are in
`tools/ci/helm-validate-values/llm-gateway-stack.yaml`.
