# NVCF LLM Request Router Helm Chart

This repository contains the Helm chart for deploying the NVCF LLM Request Router (Stargate) on Kubernetes.

## Overview

The chart packages the LLM Request Router as either a Deployment or a
StatefulSet, with Deployment as the default. It includes HTTP and gRPC
services, a metrics endpoint, and a headless service. It can also deploy the
Stargate Kubernetes backend router for worker gRPC registration and reverse
QUIC tunnels through a shared Gateway or load balancer. The backend router
selects the correct Stargate pod from gRPC authority and QUIC SNI.

The `llm-request-router` Service carries API Gateway HTTP requests and honors
pod readiness. The `llm-request-router-headless` Service publishes gRPC and
QUIC addresses for warming and ready pods, so Pylons can register and establish
tunnels before a new replica receives request traffic. The backend router and
direct multi-replica discovery use the headless Service.

A Vault Agent sidecar is configured to fetch a service token from a Vault or
OpenBao backend. In the default worker authentication mode, the application
reads `nvcfApiToken` from `/vault/secrets/secrets.json` and attaches it as a
Bearer token to outgoing worker authentication gRPC calls. See
[Worker Authentication](#worker-authentication) for the static credentials
mode, which does not use Vault.

The default chart values do not set the required image registry and repository. They must be supplied through an additional values file at install time, and access to those images must be arranged separately.

Example:

```yaml
llmRequestRouter:
  image:
    registry: <your-registry>
    repository: <your-org>/llm-request-router
    tag: <appVersion>
```

Single-replica Deployments automatically use self-only discovery so their
headless Service cannot introduce dashed-IP SRV aliases. A multi-replica
Deployment requires the backend router. The default `null` value for
`llmRequestRouter.backendRouter.enabled` enables it automatically in that
topology; explicitly setting `false` is rejected. The backend router builds
Watch responses and forwarding routes from the same
EndpointSlice snapshot. Each ready endpoint is keyed by its Pod
`targetRef.name`, so one pod produces one canonical identity even when DNS also
exposes a dashed-IP SRV alias. A multi-replica StatefulSet can instead run
without the backend router and retain direct headless Service SRV discovery.
`llmRequestRouter.discovery.watchHeartbeatMs` controls the maximum interval
between unchanged Watch snapshots from both Stargate and the backend router.
`llmRequestRouter.discovery.remoteWatchUrls` accepts only explicit `https://`
Watch URIs. Development plaintext endpoints require an explicit `http://` URI
and `allowInsecureRemoteWatchHttp=true`; scheme-less and unsupported values are
rejected instead of defaulting to plaintext.

`llmRequestRouter.kubernetes.advertisedHostnameTemplate` supports the Stargate
placeholders `{pod_name}` and `{namespace}`. Stargate resolves both placeholders
at runtime. For certificate validation, the chart substitutes the deployment
namespace and a representative pod name. `{pod_name}` must stay within the
leftmost DNS label when certificate coverage relies on a wildcard. When
`llmRequestRouter.certificate.enabled=true`, `certificate.dnsNames` must cover
the advertised hostname with either a case-insensitive exact name or a valid
leftmost `*.` wildcard. A wildcard covers exactly one label and requires at
least two suffix labels. For example, `*.nvcf.example.internal` covers
`{pod_name}.nvcf.example.internal`, but `*.example.internal` does not cover
`{pod_name}.nvcf.example.internal`.

Existing installations that currently run the StatefulSet must set
`llmRequestRouter.workload.kind=StatefulSet` before upgrading to this chart.
Changing `workload.kind` is a controlled migration, not an in-place Kubernetes
mutation. Plan a maintenance window, remove or rename the old workload, and
verify that only the selected kind owns the request-router Pods before scaling
it. A plain Helm upgrade across workload kinds can briefly run both workloads.

## Prerequisites

- Kubernetes cluster
- Helm 3.x
- `kubectl`
- A reachable Vault or OpenBao instance with a JWT authentication path configured for this service (or set `llmRequestRouter.vault.noVaultAnnotations: true` to disable Vault Agent injection)

## Getting Started

Install the chart with the default values plus your own overrides:

```bash
helm install llm-request-router llm-request-router \
  --namespace llm-request-router \
  --create-namespace \
  --values llm-request-router/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Upgrade an existing release:

```bash
helm upgrade llm-request-router llm-request-router \
  --namespace llm-request-router \
  --values llm-request-router/values.yaml \
  --values path/to/values.yaml \
  --wait \
  --timeout 10m
```

Uninstall the release:

```bash
helm uninstall llm-request-router --namespace llm-request-router
```

## Configuration

The default chart configuration lives in `llm-request-router/values.yaml`.

Important settings to review before deployment:

- `llmRequestRouter.image.*` for the router container image
- `llmRequestRouter.imagePullSecrets` for private registry access
- `llmRequestRouter.workload.kind` to select `Deployment` (default) or `StatefulSet`
- `llmRequestRouter.workload.deployment.strategy` and `llmRequestRouter.workload.statefulSet.*` for workload-specific rollout settings
- `llmRequestRouter.replicaCount`, resource requests, and limits for your environment
- `llmRequestRouter.service.*` for HTTP, gRPC, metrics, and headless service ports
- `llmRequestRouter.backendRouter.*` for multi-replica worker gRPC and reverse-tunnel routing
- `llmRequestRouter.readiness.warmupMs` for the startup readiness delay in milliseconds (default: `60000`; `0` disables the timed delay)
- `llmRequestRouter.metrics.enabled` to expose the metrics port on the Service (default: `false`)
- `llmRequestRouter.metrics.serviceMonitor.enabled` to create a Prometheus `ServiceMonitor` (requires `metrics.enabled`)
- `llmRequestRouter.certificate.*` to let cert-manager issue the Stargate QUIC server certificate
- `llmRequestRouter.tls.*` to mount the TLS Secret and pass cert/key paths to Stargate
- `llmRequestRouter.tls.mode` to choose the source of the QUIC server identity. `certManager` (default) mounts the Secret cert-manager writes for `certificate.*`. `existingSecret` mounts a pre-created Secret instead: the chart renders no `Certificate` and adds no issuer dependency, `certificate.enabled` must stay `false`, `tls.secretName`, `tls.certPath`, and `tls.keyPath` are required, and the operator owns issuance, renewal, rotation, and recovery. The Secret must provide the `tls.crt` and `tls.key` entries. The chart cannot read a pre-created Secret, so it does not validate its SANs or expiry.
- `llmRequestRouter.pki.*` to provision the OpenBao service-issuing PKI hierarchy that cert-manager mints the Certificate from. Opt-in via `pki.enabled=true`. Mirrors the SIS chart's `hook-lls-migrations.yaml` pattern: a Helm pre-install/pre-upgrade Job runs the `nvcf-openbao-migrations` image with `CORE_MIGRATIONS_ENABLED=false` + `ADDONS_LLM_ENABLED=true` so only the LLM addon executes. `pki.allowedDomains` (comma-separated DNS suffixes) is required when enabled and is the OpenBao PKI role's `allowed_domains` security constraint. Typically this is `<customer-domain>,cluster.local`. Job-level fail-hard is handled by `restartPolicy: OnFailure` + `pki.backoffLimit` combined with the migrations image's `FAILED_MIGRATIONS` accumulator (image `>= 0.12.1`).
- `llmRequestRouter.auth.*` to select how the router authenticates workers; see [Worker Authentication](#worker-authentication)
- `llmRequestRouter.vault.audience` for the projected ServiceAccount token audience used to authenticate to OpenBao
- `llmRequestRouter.vault.noVaultAnnotations` to disable Vault Agent injection (useful for local testing without OpenBao)

The default values include development-oriented placeholders. Override them before using the chart in any shared or production environment.

## Backend Worker Routing

The backend router is enabled automatically for a multi-replica Deployment.
Set `llmRequestRouter.backendRouter.enabled=true` explicitly when workers reach
another supported workload topology through a shared endpoint. Set both pylon
dial addresses to the external endpoints that workers can resolve:

```yaml
llmRequestRouter:
  backendRouter:
    enabled: true
    pylonGrpcDialAddress: https://llm-router.example.com:443
    pylonReverseTunnelDialAddress: llm-router.example.com:8080
```

The chart uses the main Stargate image for both workloads. The backend router
inherits the main image registry, repository, and tag, falling back to the
chart appVersion. That image must contain
`/usr/local/bin/stargate-k8s-router`; override `backendRouter.image.*` only to
validate a different Stargate build.

The backend router watches the headless Service's EndpointSlices and publishes
their non-terminating targets directly through `WatchStargates`, including
warming pods published by `publishNotReadyAddresses`. It uses the same snapshot
for gRPC and QUIC forwarding, so a removed or replaced Pod cannot remain as a
discovery-only target. The chart creates a dedicated ServiceAccount by default
and binds a namespaced Role to it when
`llmRequestRouter.rbac.create=true`. When
`llmRequestRouter.backendRouter.serviceAccount.create=false`, set
`llmRequestRouter.backendRouter.serviceAccount.name` to an existing account.
When `rbac.create=false`, grant `get`, `list`, and `watch` on
`discovery.k8s.io/endpointslices` to that account outside this chart.

Route HTTPS gRPC traffic on port `50071` and UDP traffic on port `50072` to the
`llm-request-router-backend-router` Service. The NVCF gateway-routes chart can
terminate TLS on a dedicated HTTPS listener and create the matching
`GRPCRoute`, `UDPRoute`, `ReferenceGrant`, gRPC `Certificate`, and stream
timeout policy. The gRPC route forwards h2c to the Service. The Gateway
implementation must support Gateway API `GRPCRoute` and `UDPRoute`. A legacy
plaintext `TCPRoute` is available only with an explicit development opt-in.

Use the same explicit `https://host:port` URI for the API worker bootstrap and
`pylonGrpcDialAddress`. The external hostname is used for TLS SNI and server
verification. The selected Stargate identity remains the HTTP/2 `:authority`,
so the secure `GRPCRoute` intentionally has no hostname match. The gRPC
listener certificate is distinct from the request-router QUIC certificate.

When QUIC verification is enabled, the mounted certificate must cover the
advertised per-pod hostname produced by
`llmRequestRouter.kubernetes.advertisedHostnameTemplate`. The default template
is `{pod_name}.llm-request-router-headless.<namespace>.svc.cluster.local`. The
external `pylonReverseTunnelDialAddress` selects the UDP network path and needs
a SAN only when the advertised identity is also changed to that hostname.

Stargate and the backend router poll the mounted TLS certificate and key and
reload the server identity for new connections. Client trust-bundle changes
are not hot-reloaded; roll out workers and other Pylon clients after changing
the CA bundle they use to verify the router.

## Worker Authentication

The router authenticates every worker registration and reverse tunnel. Select
exactly one mode under `llmRequestRouter.auth`; the chart fails to render
otherwise, and the router refuses to start without an authenticator.

| Mode | Values | Router arguments |
| --- | --- | --- |
| Gateway (default) | `workerAuthEndpoint` set | `--worker-auth-endpoint`, plus the Vault `--secrets-path` token unless `vault.noVaultAnnotations=true` |
| Static credentials | `workerAuthEndpoint: ""`, `credentialsSecret.name` set | `--worker-auth-file=/etc/stargate/worker-auth/<key>` |
| Open (development only) | `workerAuthEndpoint: ""`, `allowOpen: true` | `--allow-open-worker-auth` |

Setting both `workerAuthEndpoint` and `credentialsSecret.name` fails the
render. `allowOpen: true` next to either of them also fails the render, because
the router refuses `--allow-open-worker-auth` together with a configured
authenticator.

### Static credentials file

In static credentials mode, the chart mounts key `credentialsSecret.key`
(default `credentials.yaml`) of the existing Secret `credentialsSecret.name`
read-only at `/etc/stargate/worker-auth` and passes it as `--worker-auth-file`.
The key holds YAML that binds each cluster id to the SHA-256 hashes of that
cluster's worker bearer tokens:

```yaml
clusters:
  spark-berlin-01:
    - sha256:<64 hex>
    - sha256:<64 hex>    # next credential during rotation
```

Rules the router enforces:

- Each entry is `sha256:` followed by exactly 64 hex characters of the SHA-256
  of the token. Hex case is ignored.
- A cluster lists at least one hash, and `clusters` lists at least one cluster
  with a non-empty id.
- A hash appears once in the whole file; one credential identifies one cluster.
- No other top-level keys.

The router refuses to start when the file is missing or invalid. After start it
re-reads the file every 30 seconds and swaps in a valid changed set. A missing
or invalid update keeps the last good set active and logs one error per
distinct failure.

Create the Secret from the hash, not the token:

```bash
hash="$(printf '%s' "${WORKER_TOKEN}" | sha256sum | cut -d' ' -f1)"
printf 'clusters:\n  spark-berlin-01:\n    - sha256:%s\n' "${hash}" > credentials.yaml
kubectl -n nvcf create secret generic llm-request-router-worker-credentials \
  --from-file=credentials.yaml
```

```yaml
llmRequestRouter:
  auth:
    workerAuthEndpoint: ""
    credentialsSecret:
      name: llm-request-router-worker-credentials
  vault:
    noVaultAnnotations: true
```

A worker presenting a matching token registers without a routing key and only
under its bound cluster id; a registration with another `cluster_id` is
rejected. Static credentials mode does not read Vault; set
`vault.noVaultAnnotations=true` when no Vault or OpenBao is available so the
Vault Agent sidecar is not injected.

### Rotating a cluster credential

Rotation needs no router restart and no registration gap:

1. Add the hash of the new token under the cluster id, next to the current
   hash, and update the Secret. Within about a minute of kubelet projecting the
   update, plus up to 30 seconds, the router accepts both tokens.
2. Rotate the token Secret on the compute side, for example the pylon-operator
   credential Secret. Transport pods reconnect with the new token.
3. Remove the old hash and update the Secret again. The old token stops working
   at the next reload.

### Metrics

- `stargate_registration_auth_failures_total{reason}` counts rejected
  registrations: `missing_token`, `unknown_credential`, or `cluster_mismatch`.
  The claimed cluster id is never a label.
- `stargate_worker_auth_reloads_total{outcome}` counts reloads that activated a
  changed set (`success`) and reloads that kept the last good set because the
  file could not be read or parsed (`rejected`, once per distinct failure).

## Load Balancer Configuration

The chart can pass a Stargate load-balancer config in either of two ways:

- `llmRequestRouter.loadBalancer.config` embeds JSON directly in the release. The chart writes it to a ConfigMap and starts Stargate with `--lb-config-path=/etc/llm-request-router/lb-config.json`.
- `llmRequestRouter.loadBalancer.configPath` points Stargate at an existing file path and starts it with `--lb-config-path=<configPath>`.

`config` takes precedence over `configPath` when both are set. If neither value is set, Stargate uses its built-in default algorithm, `power-of-two`.

See the
[Stargate load balancer configuration](../../../src/libraries/rust/stargate/docs/load-balancer-configuration.md)
for the JSON schema, algorithm behavior, and tuning fields. See
[LLM Request Router Load Balancing](../../../docs/self-managed/llm-request-router-load-balancing.md)
for stack ownership, trusted headers, rollout checks, and troubleshooting.

## Local Render

```bash
helm template llm-request-router llm-request-router
```

## Notes

- If you publish or mirror the required images into another registry, set the image registry, repository, tag, and pull secret values explicitly in your override file.
