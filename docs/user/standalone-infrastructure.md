# Phase 1: Infrastructure Dependencies

This phase installs the three infrastructure services that all NVCF core services depend on:
NATS (messaging), OpenBao (secrets management), and Cassandra (persistence).

<Info>
Complete all steps in [standalone-prerequisites](./standalone-prerequisites.md) before proceeding. You should have
your shell variables (`REGISTRY`, `REPOSITORY`, `STORAGE_CLASS`, `STORAGE_SIZE`,
`CASSANDRA_PASSWORD`, `REGISTRY_CREDENTIAL_B64`) exported and namespaces created.

</Info>

## NATS

NATS provides the messaging backbone for inter-service communication across all NVCF
components.

| **Chart** | `helm-nvcf-nats` |
| --- | --- |
| **Version** | `0.6.1` |
| **Namespace** | `nats-system` |
| **Depends on** | None |

### Configuration

Create `nats-values.yaml` with your registry settings ([download template](samples/configs/standalone/nats-values.yaml)):

<Accordion title="nats-values.yaml">
</Accordion>
```yaml title="nats-values.yaml"
# NATS values for standalone installation
# Replace <REGISTRY> and <REPOSITORY> with your container registry settings.
#
# Example:
#   REGISTRY: nvcr.io
#   REPOSITORY: YOUR_ORG/YOUR_TEAM

nats:
  container:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/nats-server"

  reloader:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/nats-server-config-reloader"

  natsBox:
    container:
      image:
        registry: "<REGISTRY>"
        repository: "<REPOSITORY>/nats-box"

  nkeyJob:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/alpine-k8s"

  # Uncomment and set if using node selectors
  # podTemplate:
  #   merge:
  #     spec:
  #       nodeSelector:
  #         nvcf.nvidia.com/workload: control-plane

  # Uncomment and set to configure storage class for JetStream
  # config:
  #   jetstream:
  #     fileStore:
  #       pvc:
  #         storageClassName: "<STORAGE_CLASS>"
```

Replace all `<REGISTRY>` and `<REPOSITORY>` placeholders with your actual registry values.

If you are using a custom storage class, uncomment the `config.jetstream.fileStore.pvc.storageClassName`
section and set it to your storage class.

If you are using node selectors for dedicated NVCF node pools, uncomment the
`podTemplate` section.

### Install

```bash
helm upgrade --install nats \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-nats \
  --version 0.6.1 \
  --namespace nats-system \
  --wait --timeout 15m \
  -f nats-values.yaml
```

### Verify

```bash
kubectl get pods -n nats-system

# Expected output (3 replicas by default):
# NAME     READY   STATUS    RESTARTS   AGE
# nats-0   2/2     Running   0          2m
# nats-1   2/2     Running   0          2m
# nats-2   2/2     Running   0          2m
```

Verify the NATS cluster has formed:

```bash
kubectl logs nats-0 -n nats-system -c nats | grep "Cluster Name"
```

<Tip>
If pods remain in `Pending` state, check that your storage class is available and that
nodes satisfy any configured node selectors.

</Tip>

## OpenBao

OpenBao provides Vault-compatible secrets management. It handles secret injection into NVCF
service pods and stores sensitive configuration such as Cassandra credentials and registry
pull secrets.

| **Chart** | `helm-nvcf-openbao-server` |
| --- | --- |
| **Version** | `0.30.11` |
| **Namespace** | `vault-system` |
| **Depends on** | NATS (must be running) |

<Info>
NATS must be running and healthy before installing OpenBao. The OpenBao migration job
communicates with NATS during initialization.

</Info>

### Configuration

Create `openbao-values.yaml` with your registry and secret settings ([download template](samples/configs/standalone/openbao-values.yaml)):

<Accordion title="openbao-values.yaml">
</Accordion>
```yaml title="openbao-values.yaml"
# OpenBao values for standalone installation
# Replace <REGISTRY>, <REPOSITORY>, and secret values with your settings.
#
# Example:
#   REGISTRY: nvcr.io
#   REPOSITORY: YOUR_ORG/YOUR_TEAM

openbao:
  migrations:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/nvcf-openbao-migrations"
    issuerDiscovery:
      enabled: true  # Recommended true for EKS (discovers OIDC issuer automatically)
    env:
      - name: DEFAULT_CASSANDRA_PASSWORD
        value: "ch@ng3m3"  # Must match Cassandra superuser password
      - name: NVCF_API_SIDECARS_IMAGE_PULL_SECRET
        value: "<REGISTRY_CREDENTIAL_B64>"  # base64 of $oauthtoken:<NGC_API_KEY>
      - name: ADMIN_CLIENT_ID
        value: ncp  # Do not change

  injector:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/oss-vault-k8s"
    agentImage:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/nvcf-openbao"
    replicas: 2
    podDisruptionBudget:
      minAvailable: 1
    # Uncomment for node selectors
    # nodeSelector:
    #   nvcf.nvidia.com/workload: vault

  server:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/nvcf-openbao"
    dataStorage:
      size: "10Gi"  # 20-50Gi recommended for production
      # storageClass: "<STORAGE_CLASS>"
    # Uncomment for node selectors
    # nodeSelector:
    #   nvcf.nvidia.com/workload: vault
    extraContainers:
      - name: auto-unseal-sidecar
        image: "<REGISTRY>/<REPOSITORY>/nvcf-openbao:2.5.1-nv-1.2.1"
        volumeMounts:
          - name: openbao-server-unseal
            mountPath: /vault/userconfig/unseal
            readOnly: true
        command: ["/bin/sh", "-c"]
        args:
          - |
            echo "Starting auto-unseal monitor..."
            export BAO_ADDR=http://$HOSTNAME:8200
            while true; do
              if [ -f /vault/userconfig/unseal/unseal_key ]; then
                UNSEAL_KEY=$(cat /vault/userconfig/unseal/unseal_key)
                if [ ! -z "$UNSEAL_KEY" ]; then
                    bao operator unseal $UNSEAL_KEY
                    sleep 60
                    continue
                else
                  echo "Unseal key is empty, waiting..."
                fi
              else
                echo "Unseal key file not found, waiting..."
              fi
              sleep 10
            done
```

Replace the following placeholders:

| `<REGISTRY>` | Your container image registry |
| --- | --- |
| `<REPOSITORY>` | Your image repository path |
| `<REGISTRY_CREDENTIAL_B64>` | Base64-encoded registry credential (see [standalone-prerequisites](./standalone-prerequisites.md)) |

If you are using a custom storage class, uncomment `dataStorage.storageClass` and set it
appropriately.

If you are using node selectors, uncomment the `nodeSelector` sections under both
`injector` and `server`.

### Install

```bash
helm upgrade --install openbao-server \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-openbao-server \
  --version 0.30.11 \
  --namespace vault-system \
  --wait --wait-for-jobs --timeout 15m \
  -f openbao-values.yaml
```

<Warning>
The release name **must** be `openbao-server`. Other NVCF charts reference this name
for service discovery.

</Warning>

#### Post-Install Hooks

The OpenBao chart runs two post-install jobs automatically. The `--wait-for-jobs` flag
ensures helm waits for both to complete before returning.

**1. Initialize Cluster** (`openbao-server-initialize-cluster`)

This job initializes the OpenBao (Vault) cluster on first install:

- Initializes the vault and generates unseal keys
- Unseals all server replicas
- Saves the unseal key to a Kubernetes secret (`openbao-server-unseal`) for the auto-unseal sidecar
- Enables the Raft storage backend for HA
- Registers and enables the JWT secrets plugin
- Saves the JWT signing key to a Kubernetes secret (`cluster-jwt`)

**2. Migrations** (`openbao-server-migrations`)

This job runs after the cluster is initialized and configures OpenBao for NVCF services:

- Creates KV secret stores for each NVCF service (api, sis, ess, invocation-service, etc.)
- Writes the Cassandra password and registry pull secret (from your values file) into the vault
- Configures Kubernetes JWT authentication backends so each service can authenticate using its service account
- Creates service-specific policies that control which secrets each service can access
- Sets up JWT signing roles used by SIS for cluster agent authentication

<Note>
Both jobs must complete successfully before core services can start. If either job fails,
the core services will not be able to authenticate with OpenBao. Check job logs for
troubleshooting (see below).

</Note>

### Verify

```bash
kubectl get pods -n vault-system

# Expected output (3 server replicas + 2 injector replicas + 2 completed jobs):
# NAME                                        READY   STATUS      RESTARTS   AGE
# openbao-server-0                            2/2     Running     0          5m
# openbao-server-1                            2/2     Running     0          5m
# openbao-server-2                            2/2     Running     0          5m
# openbao-server-agent-injector-...           1/1     Running     0          5m
# openbao-server-agent-injector-...           1/1     Running     0          5m
# openbao-server-initialize-cluster-...       0/1     Completed   0          5m
# openbao-server-migrations-...               0/1     Completed   0          4m
```

Verify both post-install jobs completed:

```bash
kubectl get jobs -n vault-system

# Both jobs should show COMPLETIONS 1/1:
# NAME                                STATUS     COMPLETIONS   DURATION   AGE
# openbao-server-initialize-cluster   Complete   1/1           21s        5m
# openbao-server-migrations           Complete   1/1           8s         4m
```

Check that OpenBao is initialized and unsealed:

```bash
kubectl exec -n vault-system openbao-server-0 -- bao status

# Look for:
#   Initialized     true
#   Sealed          false
```

### Troubleshooting

- **Initialize cluster job fails**: Check the init job logs:

  ```bash
  kubectl logs -n vault-system -l job-name=openbao-server-initialize-cluster --tail=100
  ```

- **Migration job fails**: Check the migration job logs for details:

  ```bash
  kubectl logs -n vault-system -l job-name=openbao-server-migrations --tail=100
  ```

- **Server remains sealed**: The auto-unseal sidecar reads from a Kubernetes secret. Verify
  the unseal key secret exists:

  ```bash
  kubectl get secret -n vault-system | grep unseal
  ```

- **Stale resources from previous install**: If reinstalling OpenBao after a failed attempt,
  delete all resources in the namespace first to avoid conflicts with leftover secrets,
  configmaps, and jobs:

  ```bash
  helm uninstall openbao-server -n vault-system
  kubectl delete all,cm,secret,pvc,job --all -n vault-system --ignore-not-found
  ```

## Cassandra

Apache Cassandra provides the persistence layer for NVCF. It stores function metadata,
deployment state, and other operational data.

| **Chart** | `helm-nvcf-cassandra` |
| --- | --- |
| **Version** | `0.14.5` |
| **Namespace** | `cassandra-system` |
| **Depends on** | None (can be installed in parallel with NATS) |

### Configuration

Create `cassandra-values.yaml` with your registry and storage settings ([download template](samples/configs/standalone/cassandra-values.yaml)):

<Accordion title="cassandra-values.yaml">
</Accordion>
```yaml title="cassandra-values.yaml"
# Cassandra values for standalone installation
# Replace <REGISTRY>, <REPOSITORY>, and storage settings with your configuration.
#
# Example:
#   REGISTRY: nvcr.io
#   REPOSITORY: YOUR_ORG/YOUR_TEAM

cassandra:
  global:
    security:
      allowInsecureImages: true
    # Uncomment to set default storage class
    # defaultStorageClass: "<STORAGE_CLASS>"

  replicaCount: 3  # Use 1 for local development only

  image:
    registry: "<REGISTRY>"
    repository: "<REPOSITORY>/bitnami-cassandra"

  dynamicSeedDiscovery:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/bitnami-cassandra"

  migrations:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/nvcf-cassandra-migrations"

  initialization:
    image:
      registry: "<REGISTRY>"
      repository: "<REPOSITORY>/alpine-k8s"

  persistence:
    size: "10Gi"  # 50-100Gi recommended for production

  # Uncomment for node selectors
  # nodeSelector:
  #   nvcf.nvidia.com/workload: cassandra
```

Replace all `<REGISTRY>` and `<REPOSITORY>` placeholders with your actual registry values.

Adjust `persistence.size` based on your expected data volume (50-100Gi recommended for
production).

If you are using node selectors, uncomment the `nodeSelector` section.

<Note>
For local development with a single node, set `replicaCount: 1`. Production deployments
should use a minimum of 3 replicas.

</Note>

### Install

```bash
helm upgrade --install cassandra \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-cassandra \
  --version 0.14.5 \
  --namespace cassandra-system \
  --wait --wait-for-jobs --timeout 15m \
  -f cassandra-values.yaml
```

### Verify

```bash
kubectl get pods -n cassandra-system

# Expected output (3 replicas by default):
# NAME          READY   STATUS      RESTARTS   AGE
# cassandra-0   1/1     Running     0          8m
# cassandra-1   1/1     Running     0          6m
# cassandra-2   1/1     Running     0          4m
```

<Note>
**Cassandra initialization pods showing "Error" is expected.** The `cassandra-initialize-cluster`
job runs multiple pods in parallel and retries on failure. It is normal to see one or more pods
with `Error` status. The deployment is healthy as long as at least one initialization pod
reaches `Completed` and the `cassandra-migrations` job completes successfully.

</Note>

Check the initialization and migration jobs:

```bash
kubectl get jobs -n cassandra-system

# Both jobs should show COMPLETIONS 1/1:
# NAME                             COMPLETIONS   DURATION   AGE
# cassandra-initialize-cluster     1/1           45s        8m
# cassandra-migrations             1/1           30s        7m
```

Verify Cassandra is accepting connections:

```bash
kubectl exec -n cassandra-system cassandra-0 -- nodetool status

# All nodes should show UN (Up/Normal) status
```

### Troubleshooting

- **Pods stuck in Pending**: Verify your storage class can provision PVCs of the requested size.
  Some cloud providers (e.g., AWS EBS gp3) have minimum PVC size requirements.

- **Initialization job retries**: This is normal. The initialization job may fail several times
  while Cassandra nodes are still starting. As long as one pod eventually reaches `Completed`,
  the cluster is healthy.

- **Migration job fails**: Check migration logs:

  ```bash
  kubectl logs -n cassandra-system -l job-name=cassandra-migrations --tail=100
  ```

## Verify All Infrastructure

Before proceeding to the core services, confirm all three infrastructure components are healthy:

```bash
echo "=== NATS ==="
kubectl get pods -n nats-system

echo "=== OpenBao ==="
kubectl get pods -n vault-system

echo "=== Cassandra ==="
kubectl get pods -n cassandra-system
```

All pods should be in `Running` or `Completed` state. If any pods are unhealthy, resolve
the issues before continuing.

## Next Steps

Once all infrastructure dependencies are running, proceed to
[standalone-core-services](./standalone-core-services.md) to install the NVCF control plane services.
