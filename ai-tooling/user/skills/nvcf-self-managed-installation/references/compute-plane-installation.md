# Split Compute-Plane Installation

The control plane and compute plane are separate stacks. Install the control
plane first, then register and install each compute cluster. Choose one handoff
below and use it for the full lifecycle. Do not combine a CLI-generated profile
with the Helmfile values flow.

## Choose a Handoff

| Flow | Source of control-plane endpoints | Registration artifact |
|------|-----------------------------------|-----------------------|
| Helmfile values | Separate control-plane and compute-plane environment files | `deploy/stacks/nvcf-compute-plane/registration/<cluster>-register-values.yaml` |
| CLI profile | CLI-generated `deploy/stacks/self-managed/out/control-plane-profile.yaml` | CLI-generated `deploy/stacks/nvcf-compute-plane/out/<cluster>-register-values.yaml` |

The CLI profile is mandatory for the CLI flow and must not be hand-authored.
The Helmfile flow does not consume a control-plane profile.

Commands below use monorepo paths from the repository root. Operators using
extracted packages need matching-version `nvcf-self-managed-stack` and
`nvcf-compute-plane-stack` bundles. Substitute those bundle roots for
`deploy/stacks/self-managed` and `deploy/stacks/nvcf-compute-plane`.

## Helmfile Values Flow

Create both environment files before installation:

- `deploy/stacks/self-managed/environments/<env>.yaml` for control-plane
  registry, domain, Gateway, routes, and service settings.
- `deploy/stacks/nvcf-compute-plane/environments/<env>.yaml` for compute-plane
  registry, pull secrets, and compute-reachable ICMS, ReVal, and NATS values.

For EKS, use the canonical
[CSP End-to-End Example](https://docs.nvidia.com/nvcf/v0.6.0-rc/csp-end-to-end-example).
The control-plane environment keeps the Envoy Gateway controller in
`envoy-gateway-system`, attaches routes to `Gateway/nvcf-gateway` in
`envoy-gateway`, and enables the NATS route. The compute-plane environment uses
the external address plus the service host overrides. Here,
`<gateway-address>` is the NLB address and `<gateway-domain>` is the DNS base
whose service subdomains resolve to that NLB:

```yaml
# deploy/stacks/self-managed/environments/<env>.yaml
global:
  domain: "<gateway-domain>"
  workerEndpoints:
    nvcfServiceURL: "http://api.<gateway-domain>"
    nvcfGrpcServiceURL: "http://worker-api.<gateway-domain>"
    nvcfNatsServiceURL: "nats://<gateway-address>:4222"
    nvctServiceURL: "http://tasks.<gateway-domain>"
    nvctGrpcServiceURL: "http://worker-tasks.<gateway-domain>"
    invocationServiceURL: "http://invocation.<gateway-domain>"

grpcproxy:
  nvcfGrpcServiceURL: "http://api.nvcf.svc.cluster.local:9090"

ingress:
  gatewayApi:
    controllerNamespace: envoy-gateway-system
    gateways:
      shared: {name: nvcf-gateway, namespace: envoy-gateway}
      grpc: {name: nvcf-gateway, namespace: envoy-gateway}
      nats:
        name: nvcf-gateway
        namespace: envoy-gateway
        listenerName: nats
    routes:
      nvcfApi:
        grpc:
          enabled: true
          hostnames: ["worker-api.<gateway-domain>"]
      nvctApi:
        grpc:
          enabled: true
          hostnames: ["worker-tasks.<gateway-domain>"]
      nats:
        enabled: true

openbao:
  migrations:
    issuerDiscovery:
      enabled: true
```

```yaml
# deploy/stacks/nvcf-compute-plane/environments/<env>.yaml
global:
  nvcaOperator:
    selfManaged:
      icmsServiceURL: "http://<gateway-address>"
      icmsServiceHostHeaderOverride: "sis.<gateway-domain>"
      revalServiceURL: "http://<gateway-address>"
      revalServiceHostHeaderOverride: "reval.<gateway-domain>"
      natsURL: "nats://<gateway-address>:4222"
      natsHostOverride: "nats.<gateway-domain>"
```

Preview and install the control plane against its context:

```bash
make -C deploy/stacks/self-managed template \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=<control-plane-kubeconfig>

make -C deploy/stacks/self-managed install \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=<control-plane-kubeconfig>
```

Before registering through a bare Gateway address, create or update the CLI
config passed as `NVCF_CLI_CONFIG`:

```yaml
base_http_url: "http://<gateway-address>"
invoke_url: "http://<gateway-address>"
base_grpc_url: "<gateway-address>:10081"
api_keys_service_url: "http://<gateway-address>"
icms_url: "http://<gateway-address>"
api_host: "api.<gateway-domain>"
api_keys_host: "api-keys.<gateway-domain>"
invoke_host: "invocation.<gateway-domain>"
icms_host: "sis.<gateway-domain>"
```

Host fields contain hostnames only, without a URL scheme. `ICMS_URL` maps only
to the registration command's `--icms-url`; `icms_host` in the config supplies
the Gateway Host header. There is no `--icms-host` Make or CLI flag.

Register from the compute context, then install the compute plane:

```bash
make -C deploy/stacks/nvcf-compute-plane register-cluster \
  CLUSTER_NAME=<cluster-name> \
  NCA_ID=<nca-id> \
  CLUSTER_REGION=<region> \
  ICMS_URL=http://<gateway-address> \
  KUBECONFIG_FILE=<compute-plane-kubeconfig> \
  NVCF_CLI=<path-to-nvcf-cli> \
  NVCF_CLI_CONFIG=<path-to-nvcf-cli-config>

make -C deploy/stacks/nvcf-compute-plane install \
  CLUSTER_NAME=<cluster-name> \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=<compute-plane-kubeconfig>
```

Registration discovers the OIDC issuer and JWKS from the compute kube context.
In a multi-cluster deployment, `KUBECONFIG_FILE` is required on both
compute-plane commands. In a same-cluster deployment it may be omitted only
when the ambient context is already the target cluster.

## CLI Profile Flow

Run the CLI control-plane install first. It creates the profile consumed by
compute-plane registration:

```bash
nvcf-cli --config <config> self-hosted \
  --control-plane-stack deploy/stacks/self-managed \
  --compute-plane-stack deploy/stacks/nvcf-compute-plane \
  --env <env> \
  --plain \
  --control-plane-context <control-plane-context> \
  --compute-plane-context <compute-plane-context> \
  install --control-plane \
  --cluster-name <control-plane-cluster-name> \
  --region <region> \
  --nca-id <nca-id>

test -s deploy/stacks/self-managed/out/control-plane-profile.yaml
```

Use the full persistent prefix for both compute-plane commands:

```bash
nvcf-cli --config <config> self-hosted \
  --control-plane-stack deploy/stacks/self-managed \
  --compute-plane-stack deploy/stacks/nvcf-compute-plane \
  --env <env> \
  --plain \
  compute-plane register \
  --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml \
  --cluster-name <compute-plane-cluster-name> \
  --kube-context <compute-plane-context> \
  --region <region> \
  --output deploy/stacks/nvcf-compute-plane/out/<compute-plane-cluster-name>-register-values.yaml

nvcf-cli --config <config> self-hosted \
  --control-plane-stack deploy/stacks/self-managed \
  --compute-plane-stack deploy/stacks/nvcf-compute-plane \
  --env <env> \
  --plain \
  compute-plane install \
  --values deploy/stacks/nvcf-compute-plane/out/<compute-plane-cluster-name>-register-values.yaml \
  --kube-context <compute-plane-context> \
  --cluster-name <compute-plane-cluster-name>
```

Registration selects the profile's in-cluster or compute-reachable endpoints
from the compute context and probes its JWKS. Do not replace profile endpoints
with hand-authored Helmfile endpoint values.

## Pull Secrets

Create the control-plane secret in these namespaces on the control-plane
context:

```text
cassandra-system nats-system nvcf api-keys ess sis vault-system cert-manager
```

On the compute context, create the secret only in `nvca-operator` and reference
it through `global.imagePullSecrets` in the compute-plane environment. The chart
and operator use it in `nvca-operator` and mirror it to `nvca-system`. Do not add
either namespace to the control-plane list. For commands and policy examples,
see [pull-secrets.md](pull-secrets.md).

## Verification

Use the explicit Gateway name and namespace:

```bash
kubectl --context <control-plane-context> get gateway nvcf-gateway \
  -n envoy-gateway -o wide
kubectl --context <control-plane-context> get tcproute -n envoy-gateway
kubectl --context <compute-plane-context> rollout status \
  deployment/nvca-operator -n nvca-operator --timeout=10m
kubectl --context <compute-plane-context> wait \
  nvcfbackend/<compute-plane-cluster-name> -n nvca-operator \
  --for=jsonpath='{.status.agentStatus}'=healthy --timeout=10m
kubectl --context <compute-plane-context> get secret <pull-secret> -n nvca-system
```

## Teardown

Destroy and deregister every compute plane before the control plane. Both
handoffs can destroy the control plane, so apply this preflight before either
one. Preserve the `cert-manager` namespace by default. Namespace preservation
does not preserve the Helm release. Identify whether cert-manager is external.
For an external installation, require effective `certManager.enabled: false`
and abort if it is true. For an intended stack-managed release with effective
`certManager.enabled: true`, verify matching stack Helm release metadata and
abort on missing or mismatched evidence. Only plan namespace removal after
verifying namespace provenance and getting explicit confirmation; otherwise
do not delete it.

For the Helmfile values flow, retain each registration values file until
cleanup is verified:

```bash
make -C deploy/stacks/nvcf-compute-plane destroy \
  CLUSTER_NAME=<cluster-name> \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=<compute-plane-kubeconfig>

<nvcf-cli> --config <config> cluster delete \
  --cluster-id <cluster-id-from-registration-values> \
  --nca-id <nca-id> \
  --ignore-missing
```

Read `clusterID` and `ncaID` from
`deploy/stacks/nvcf-compute-plane/registration/<cluster-name>-register-values.yaml`.
Show the exact cluster ID and get confirmation before `cluster delete`. Repeat
compute destroy and deregistration for every compute cluster, then destroy the
control plane.

```bash
KUBECONFIG=<control-plane-kubeconfig> \
make -C deploy/stacks/self-managed destroy \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=<control-plane-kubeconfig>
```

Both `KUBECONFIG` and `KUBECONFIG_FILE` are required on the control-plane
destroy because its namespace cleanup uses ambient `kubectl`.

For the CLI profile flow, keep
`deploy/stacks/nvcf-compute-plane/out/<cluster-name>-register-values.yaml`
until teardown completes because `down` reads its cluster ID. First query the
live registration set with the same complete persistent prefix:

```bash
nvcf-cli --config <config> self-hosted \
  --control-plane-stack deploy/stacks/self-managed \
  --compute-plane-stack deploy/stacks/nvcf-compute-plane \
  --env <env> \
  --plain \
  --control-plane-context <control-plane-context> \
  --compute-plane-context <compute-plane-context> \
  --icms-url <compute-reachable-icms-url> \
  status \
  --cluster-name <compute-plane-cluster-name> \
  --nca-id <nca-id>
```

Show the registered compute-plane list. Named-cluster `down --plan-only` does
not query ICMS, so it previews only that compute plane and cannot show the
conditional control-plane removal. If the target is the last registered
cluster, real `down` also destroys the control plane. Get explicit confirmation
for both removals before continuing. Then preview and execute with the same
prefix:

```bash
nvcf-cli --config <config> self-hosted \
  --control-plane-stack deploy/stacks/self-managed \
  --compute-plane-stack deploy/stacks/nvcf-compute-plane \
  --env <env> \
  --plain \
  --control-plane-context <control-plane-context> \
  --compute-plane-context <compute-plane-context> \
  --icms-url <compute-reachable-icms-url> \
  down --plan-only \
  --cluster-name <compute-plane-cluster-name> \
  --nca-id <nca-id>

nvcf-cli --config <config> self-hosted \
  --control-plane-stack deploy/stacks/self-managed \
  --compute-plane-stack deploy/stacks/nvcf-compute-plane \
  --env <env> \
  --plain \
  --control-plane-context <control-plane-context> \
  --compute-plane-context <compute-plane-context> \
  --icms-url <compute-reachable-icms-url> \
  down \
  --cluster-name <compute-plane-cluster-name> \
  --nca-id <nca-id>
```

Repeat `down` for each compute cluster. Each run destroys that compute plane and
unregisters its ICMS row. It removes the control plane only after no cluster
registrations remain.
