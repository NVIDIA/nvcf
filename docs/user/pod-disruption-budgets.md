# Pod Disruption Budgets

PodDisruptionBudgets (PDBs) limit the number of pods that can be voluntarily
evicted at once during node drains, cluster upgrades, or autoscaler scale-down
events. Enabling PDBs prevents entire stateful tiers from going offline during
maintenance and is recommended for production deployments.

See the [Kubernetes PDB documentation](https://kubernetes.io/docs/tasks/run-application/configure-pdb/)
for a full explanation of how disruption budgets work.

## How it works

When a PDB is active, the Kubernetes eviction API blocks any voluntary
disruption that would reduce the number of running pods below `minAvailable` (or
above `maxUnavailable`). Node drain operations will pause and wait for displaced
pods to reschedule and become ready before proceeding.

PDBs only protect against voluntary disruptions (drains, upgrades,
autoscaler). They do not prevent evictions caused by node failure or
out-of-memory pressure.

## Configuration

Set PDB values in your environment file (e.g.
`deploy/stacks/self-managed/environments/<env>.yaml`).

### Infrastructure components

These are the stateful dependencies that underpin the NVCF control plane. PDBs
are most critical here.

The custom Cassandra chart has PDB disabled by default. NATS and OpenBao use
upstream charts that enable a disruption budget by default.

Cassandra (3-node cluster, namespace `cassandra-system`):

```yaml
cassandra:
  podDisruptionBudget:
    enabled: true
    minAvailable: 2   # keep at least 2 of 3 nodes up during any disruption
```

NATS (3-node JetStream cluster, namespace `nats-system`):

The upstream NATS chart enables a PDB by default. Override to disable or
customise:

```yaml
nats:
  podDisruptionBudget:
    enabled: true
    merge:
      spec:
        minAvailable: 2
```

OpenBao server (3-node HA Raft cluster, namespace `vault-system`):

The upstream OpenBao chart enables an HA disruption budget by default.
Override `maxUnavailable` when needed:

```yaml
openbao:
  server:
    ha:
      disruptionBudget:
        enabled: true
        maxUnavailable: 1
```

The OpenBao injector PDB (`minAvailable: 1`) is always active and can be
adjusted:

```yaml
openbao:
  injector:
    podDisruptionBudget:
      minAvailable: 1
```

### Control-plane services

These services run as Deployments and default to a single replica. Enable PDBs
only when you increase `replicaCount` above 1.

| Environment key | Default replicas |
|---|---|
| `ess.podDisruptionBudget` | 1 |
| `grpcproxy.podDisruptionBudget` | 1 |
| `invocation.podDisruptionBudget` | 1 |
| `rateLimiter.podDisruptionBudget` | 1 |
| `llmApiGateway.podDisruptionBudget` | 3 |
| `llmRequestRouter.podDisruptionBudget` | 3 |
| `adminIssuerProxy.podDisruptionBudget` | 1 |
| `apikeys.podDisruptionBudget` | 1 |
| `natsAuthCalloutService.podDisruptionBudget` | 1 |
| `functionautoscaler.podDisruptionBudget` | 1 |
| `reval.podDisruptionBudget` | 1 |
| `podDisruptionBudget` (nvca-operator) | 1 |

Example for a scaled-up LLM gateway:

```yaml
llmApiGateway:
  replicaCount: 5
  podDisruptionBudget:
    enabled: true
    maxUnavailable: 1
```

### Value reference

Each PDB block accepts the same fields:

| Field | Type | Description |
|---|---|---|
| `enabled` | bool | Set `true` to create the PDB resource. Default: `false` for custom charts. |
| `minAvailable` | int or string | Minimum pods that must remain available. Accepts an integer (`2`) or a percentage (`"50%"`). Mutually exclusive with `maxUnavailable`. |
| `maxUnavailable` | int or string | Maximum pods that may be unavailable at once. Accepts an integer (`1`) or a percentage (`"33%"`). Mutually exclusive with `minAvailable`. |

Set exactly one of `minAvailable` or `maxUnavailable` when `enabled: true`.
The chart will fail at render time if both or neither are set.
