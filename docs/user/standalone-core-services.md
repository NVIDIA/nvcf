# Phase 2: Core Services

This phase installs the NVCF control plane services. These services depend on the
infrastructure components installed in [standalone-infrastructure](./standalone-infrastructure.md).

<Info>
All three infrastructure dependencies (NATS, OpenBao, Cassandra) must be running and healthy
before proceeding. Verify with:

```bash
kubectl get pods -n nats-system -n vault-system -n cassandra-system
```

</Info>

Install the services in the order shown below. Services with dependencies are
noted. Wait for the dependency to be healthy before installing the dependent
service.

## API Keys

API Keys provides authentication token management for all NVCF API interactions.

| **Chart** | `helm-nvcf-api-keys` |
| --- | --- |
| **Version** | `1.5.1` |
| **Namespace** | `api-keys` |
| **Depends on** | Infrastructure only |

### Configuration

Create `api-keys-values.yaml` ([download template](samples/configs/standalone/api-keys-values.yaml)):

<Accordion title="api-keys-values.yaml">
```yaml title="api-keys-values.yaml"
# API Keys values for standalone installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.

apikeys:
  fullnameOverride: api-keys
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/nv-api-keys"

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: control-plane
```
</Accordion>

Replace `<REGISTRY>` and `<REPOSITORY>` with your registry settings.

### Install

```bash
helm upgrade --install api-keys \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-api-keys \
  --version 1.5.1 \
  --namespace api-keys \
  --wait --timeout 10m \
  -f api-keys-values.yaml
```

### Verify

```bash
kubectl get pods -n api-keys

# Expected: api-keys pod Running
```

## SIS

The Spot Instance Service (SIS) handles cluster registration and GPU resource management.

| **Chart** | `helm-nvcf-sis` |
| --- | --- |
| **Version** | `1.14.4` |
| **Namespace** | `sis` |
| **Depends on** | Infrastructure only |

### Configuration

Create `sis-values.yaml` ([download template](samples/configs/standalone/sis-values.yaml)):

<Accordion title="sis-values.yaml">
```yaml title="sis-values.yaml"
# SIS (Spot Instance Service) values for standalone installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.

sis:
  fullnameOverride: spot-instance-service
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/spot"

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: control-plane
```
</Accordion>

### Install

```bash
helm upgrade --install sis \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-sis \
  --version 1.14.4 \
  --namespace sis \
  --wait --timeout 10m \
  -f sis-values.yaml
```

### Verify

```bash
kubectl get pods -n sis

# Expected: spot-instance-service pod Running
```

## ESS API

The ESS (Enterprise Secrets Service) API distributes secrets to NVCF services via OpenBao.

| **Chart** | `helm-nvcf-ess-api` |
| --- | --- |
| **Version** | `1.5.3` |
| **Namespace** | `ess` |
| **Depends on** | Infrastructure only |

### Configuration

Create `ess-api-values.yaml` ([download template](samples/configs/standalone/ess-api-values.yaml)):

<Accordion title="ess-api-values.yaml">
```yaml title="ess-api-values.yaml"
# ESS API values for standalone installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.

ess:
  fullnameOverride: ess-api
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/ess-api"
```
</Accordion>

### Install

```bash
helm upgrade --install ess-api \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-ess-api \
  --version 1.5.3 \
  --namespace ess \
  --wait --timeout 10m \
  -f ess-api-values.yaml
```

### Verify

```bash
kubectl get pods -n ess

# Expected: ess-api pod Running
```

## NVCF API

The NVCF API is the primary control plane service. It manages functions, deployments, and
account configuration. The API chart includes an account bootstrap job that runs on first
install to initialize the NVCF account with registry credentials.

| **Chart** | `helm-nvcf-api` |
| --- | --- |
| **Version** | `1.19.3` |
| **Namespace** | `nvcf` |
| **Depends on** | ESS API (must be running) |

<Info>
The ESS API must be running before installing the NVCF API. The account bootstrap job
communicates with ESS during initialization.

</Info>

### Configuration

Create `nvcf-api-values.yaml` ([download template](samples/configs/standalone/nvcf-api-values.yaml)):

<Accordion title="nvcf-api-values.yaml">
```yaml title="nvcf-api-values.yaml"
# NVCF API values for standalone installation
# Replace <REGISTRY>, <REPOSITORY>, and credential placeholders with your settings.

api:
  fullnameOverride: nvcf-api
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/strap"

  accountBootstrap:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/alpine-k8s"
      tag: 1.30.12
      pullPolicy: IfNotPresent

    # Registry credentials for function container/chart deployments.
    # NGC credentials are used by default. Additional registries (ECR, etc.)
    # can be added post-install via the NVCF CLI or API.
    registryCredentials:
      - registryHostname: nvcr.io
        secret:
          name: nvcr-containers
          value: "<REGISTRY_CREDENTIAL_B64>"  # base64 of $oauthtoken:<NGC_API_KEY>
        artifactTypes: ["CONTAINER"]
        tags: []
        description: "NGC Container registry"
      - registryHostname: helm.ngc.nvidia.com
        secret:
          name: nvcr-helmcharts
          value: "<REGISTRY_CREDENTIAL_B64>"  # base64 of $oauthtoken:<NGC_API_KEY>
        artifactTypes: ["HELM"]
        tags: []
        description: "NGC Helm chart registry"

    limits:
      maxFunctions: 10
      maxTasks: 10
      maxTelemetries: 10
      maxRegistryCreds: 10

  env:
    NVCF_NATS_REGION_PLACEMENT_TAG: "dc"
    NVCF_SIDECARS_HOSTNAME: "<REGISTRY>"
    NVCF_SIDECARS_REPOSITORY: "<REPOSITORY>"

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: control-plane
```
</Accordion>

Replace the following placeholders:

| `<REGISTRY>` | Your container image registry |
| --- | --- |
| `<REPOSITORY>` | Your image repository path |
| `<REGISTRY_CREDENTIAL_B64>` | Base64-encoded registry credential (see [standalone-prerequisites](./standalone-prerequisites.md)) |
| `<HELM_REGISTRY>` | Hostname for your Helm chart registry (e.g., `helm.ngc.nvidia.com` or your ECR hostname) |

### Install

```bash
helm upgrade --install api \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-api \
  --version 1.19.3 \
  --namespace nvcf \
  --wait --wait-for-jobs --timeout 15m \
  -f nvcf-api-values.yaml
```

<Info>
**Monitor for account bootstrap failures.** Open a separate terminal and watch events:

```bash
kubectl get events -n nvcf -w
```

The account bootstrap job is the most common failure point (usually due to misconfigured
registry credentials in the values file).

</Info>

### Verify

```bash
kubectl get pods -n nvcf

# Expected: nvcf-api pod Running
```

Check the bootstrap job completed:

```bash
kubectl get jobs -n nvcf

# The nvcf-api-account-bootstrap job should show COMPLETIONS 1/1
```

<Note>
The bootstrap job auto-deletes after approximately 5 minutes. Monitor events in real-time
to catch failures.

</Note>

### Troubleshooting

- **Bootstrap job fails**: Check the job logs:

  ```bash
  kubectl logs job/nvcf-api-account-bootstrap -n nvcf
  ```

- **Registry credential errors**: Verify your `<REGISTRY_CREDENTIAL_B64>` value is correct.
  The base64-encoded credential should decode to `username:password` format.

- **Recovering from bootstrap failure**: Uninstall the API chart, fix the values, and reinstall:

  ```bash
  helm uninstall api -n nvcf
  # Fix nvcf-api-values.yaml
  helm upgrade --install api ...
  ```

## Invocation Service

The Invocation Service handles function invocation requests and routes them to worker nodes.

| **Chart** | `helm-nvcf-invocation-service` |
| --- | --- |
| **Version** | `1.5.4` |
| **Namespace** | `nvcf` |
| **Depends on** | NVCF API (must be running) |

### Configuration

Create `invocation-service-values.yaml` ([download template](samples/configs/standalone/invocation-service-values.yaml)):

<Accordion title="invocation-service-values.yaml">
```yaml title="invocation-service-values.yaml"
# Invocation Service values for standalone installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.

invocation:
  fullnameOverride: invocation-service
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/nvcf-invocation-service"

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: control-plane
```
</Accordion>

### Install

```bash
helm upgrade --install invocation-service \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-invocation-service \
  --version 1.5.4 \
  --namespace nvcf \
  --wait --timeout 10m \
  -f invocation-service-values.yaml
```

### Verify

```bash
kubectl get pods -n nvcf -l app.kubernetes.io/name=invocation-service

# Expected: invocation-service pod Running
```

## gRPC Proxy

The gRPC Proxy enables streaming workloads over gRPC connections.

| **Chart** | `helm-nvcf-grpc-proxy` |
| --- | --- |
| **Version** | `1.6.4` |
| **Namespace** | `nvcf` |
| **Depends on** | NVCF API (must be running) |

### Configuration

Create `grpc-proxy-values.yaml` ([download template](samples/configs/standalone/grpc-proxy-values.yaml)):

<Accordion title="grpc-proxy-values.yaml">
```yaml title="grpc-proxy-values.yaml"
# gRPC Proxy values for standalone installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.

grpcproxy:
  fullnameOverride: grpc-proxy
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/nvcf-grpc-proxy"

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: control-plane
```
</Accordion>

### Install

```bash
helm upgrade --install grpc-proxy \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-grpc-proxy \
  --version 1.6.4 \
  --namespace nvcf \
  --wait --timeout 10m \
  -f grpc-proxy-values.yaml
```

### Verify

```bash
kubectl get pods -n nvcf -l app.kubernetes.io/name=grpc-proxy

# Expected: grpc-proxy pod Running
```

## Notary Service

The Notary Service handles request signing and validation for secure inter-service communication.

| **Chart** | `helm-nvcf-notary-service` |
| --- | --- |
| **Version** | `1.4.1` |
| **Namespace** | `nvcf` |
| **Depends on** | Infrastructure only |

### Configuration

Create `notary-service-values.yaml` ([download template](samples/configs/standalone/notary-service-values.yaml)):

<Accordion title="notary-service-values.yaml">
```yaml title="notary-service-values.yaml"
# Notary Service values for standalone installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.

notary:
  fullnameOverride: notary-service
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/notary-service"

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: control-plane
```
</Accordion>

### Install

```bash
helm upgrade --install notary-service \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-notary-service \
  --version 1.4.1 \
  --namespace nvcf \
  --wait --timeout 10m \
  -f notary-service-values.yaml
```

### Verify

```bash
kubectl get pods -n nvcf -l app.kubernetes.io/name=notary-service

# Expected: notary-service pod Running
```

## Reval

Reval renders Helm chart functions without requiring direct cluster access. It is
installed in the `nvcf` namespace with the `helm-reval` chart.

| **Chart** | `helm-reval` |
| --- | --- |
| **Version** | `1.3.8` |
| **Namespace** | `nvcf` |
| **Depends on** | Infrastructure only |

### Configuration

Create `reval-values.yaml` ([download template](samples/configs/standalone/reval-values.yaml)):

<Accordion title="reval-values.yaml">
```yaml title="reval-values.yaml"
# Reval values for standalone installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.

reval:
  fullnameOverride: reval
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/reval-server"

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: control-plane
```
</Accordion>

Replace `<REGISTRY>` and `<REPOSITORY>` with your registry settings.

### Install

```bash
helm upgrade --install reval \
  oci://${REGISTRY}/${REPOSITORY}/helm-reval \
  --version 1.3.8 \
  --namespace nvcf \
  --wait --timeout 10m \
  -f reval-values.yaml
```

### Verify

```bash
kubectl get pods -n nvcf -l app.kubernetes.io/name=reval

# Expected: reval pod Running
```

## Admin Token Issuer Proxy

The Admin Token Issuer Proxy provides an admin endpoint for generating API keys without
requiring pre-existing credentials. It is used for initial setup and emergency access.

| **Chart** | `helm-admin-token-issuer-proxy` |
| --- | --- |
| **Version** | `1.3.2` |
| **Namespace** | `api-keys` |
| **Depends on** | API Keys (must be running) |

### Configuration

Create `admin-issuer-proxy-values.yaml` ([download template](samples/configs/standalone/admin-issuer-proxy-values.yaml)):

<Accordion title="admin-issuer-proxy-values.yaml">
```yaml title="admin-issuer-proxy-values.yaml"
# Admin Token Issuer Proxy values for standalone installation
# Replace <REGISTRY>, <REPOSITORY>, and <DOMAIN> with your settings.

adminIssuerProxy:
  fullnameOverride: admin-token-issuer-proxy
  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/admin-token-issuer-proxy"

  # Gateway is disabled during Phase 2 (core services) because the Gateway
  # resource and CRDs are not yet installed. The gateway route for the admin
  # endpoint is created in Phase 3 when the Gateway Routes chart is installed.
  gateway:
    enabled: false

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: control-plane
```
</Accordion>

<Note>
The `gateway` setting is `false` during this phase because the Gateway API CRDs and
Gateway resource are not yet installed. The admin endpoint HTTPRoute will be created in
[standalone-gateway](./standalone-gateway.md) when the Gateway Routes chart is deployed.

</Note>

### Install

```bash
helm upgrade --install admin-issuer-proxy \
  oci://${REGISTRY}/${REPOSITORY}/helm-admin-token-issuer-proxy \
  --version 1.3.2 \
  --namespace api-keys \
  --wait --timeout 10m \
  -f admin-issuer-proxy-values.yaml
```

### Verify

```bash
kubectl get pods -n api-keys

# Expected: api-keys and admin-token-issuer-proxy pods both Running
```

## Verify All Core Services

Before proceeding to gateway configuration, confirm all core services are healthy:

```bash
echo "=== NVCF namespace ==="
kubectl get pods -n nvcf

echo "=== API Keys namespace ==="
kubectl get pods -n api-keys

echo "=== ESS namespace ==="
kubectl get pods -n ess

echo "=== SIS namespace ==="
kubectl get pods -n sis
```

All pods should be in `Running` state. Verify helm releases:

```bash
helm list -A

# All releases should show STATUS: deployed
```

<Tip>
If any pod is stuck in `CrashLoopBackOff`, check its logs with
`kubectl logs <pod-name> -n <namespace> --tail=100`. Common causes include
misconfigured secrets or unreachable infrastructure services.

</Tip>

## Next Steps

Once all core services are running, proceed to [standalone-gateway](./standalone-gateway.md) to configure
ingress and verify end-to-end API connectivity.
