# High Availability

This guide explains how to run the self-hosted NVCF control plane in a
high-availability (HA) topology so that it survives the loss of a single node
or availability zone (AZ). It covers the cluster prerequisites you must provide,
how to turn HA on, what each service does under HA, and how to validate the
result.

HA is opt-in. Single-node installs (local, CI, small proofs of concept) should
leave it off and are unaffected by anything in this guide.

## Overview

HA is controlled by a single switch in your Helmfile environment values,
`highAvailability.mode`. The self-managed stack maps that mode onto per-chart
values (replica counts, pod anti-affinity, zone topology spread, pod disruption
budgets, and data-durability settings) in
`deploy/stacks/self-managed/global.yaml.gotmpl`.

| `highAvailability.mode` | Behavior |
| --- | --- |
| `none` (default) | Keep existing single-replica chart/env values. Nothing in this guide applies. Use for local, CI, and single-node installs. |
| `ha-preferred` | HA sizing (multiple replicas, quorum services at 3). Node/AZ spread is a **preference**: if a second node or AZ has no capacity, pods still schedule (co-located) rather than staying `Pending`. |
| `ha-enforced` | Same HA sizing, but node/AZ spread is **required**: a replica that cannot land on a distinct node/AZ stays `Pending` instead of packing onto an occupied one. |

`ha-preferred` is the recommended starting point: it gives you the full HA
topology while degrading gracefully on a constrained cluster. Move to
`ha-enforced` once you have confirmed your node pools have capacity in every AZ
and you want hard placement guarantees.

## Cluster prerequisites

HA depends on infrastructure the **operator** provides. The stack cannot create
nodes or AZs for you — it only schedules against what you label.

### 1. Node count

Both HA modes require **at least 3 schedulable nodes** in the pool(s) that host
control-plane and quorum workloads. The quorum services (Cassandra, NATS,
OpenBao) run 3 replicas that must land on 3 distinct nodes.

### 2. Availability-zone labels

To spread replicas across failure domains, the scheduler uses the standard
Kubernetes well-known label on your nodes:

```
topology.kubernetes.io/zone=<az>
```

You (the operator) must ensure this label is present on every node. Managed
Kubernetes services (EKS, AKS, GKE) apply it automatically. On bare-metal or
custom clusters, set it yourself, for example:

```bash
kubectl label node <node-name> topology.kubernetes.io/zone=az-1
```

If the label is absent, zone topology spread has nothing to spread across. In
`ha-preferred` this silently degrades to node-level spread only; in
`ha-enforced` zone-constrained pods can stay `Pending`. Aim for capacity in
**at least two AZs** (three is better for the quorum services).

### 3. Dedicated node pools (recommended)

For predictable placement and isolation, give the stateful quorum services their
own node pools and label them with `nvcf.nvidia.com/workload`. Configure the
selectors under `global.nodeSelectors` and **set `enabled: true`** — the
selectors ship disabled (`global.nodeSelectors.enabled: false`) and are a no-op
until you turn them on:

```yaml
global:
  nodeSelectors:
    enabled: true                 # required; selectors are ignored when false
    controlplane:
      key: nvcf.nvidia.com/workload
      value: control-plane
    cassandra:
      key: nvcf.nvidia.com/workload
      value: cassandra
    vault:
      key: nvcf.nvidia.com/workload
      value: vault
```

| Pool | Selector value | Hosts |
| --- | --- | --- |
| `controlplane` | `control-plane` | Stateless + hot-path Deployments |
| `cassandra` | `cassandra` | Cassandra StatefulSet |
| `vault` | `vault` | OpenBao StatefulSet |

Each dedicated pool must span the availability zones — that is, have **capacity
in every AZ you want to spread across** (both zones in a 2-AZ cluster, all three
in a 3-AZ cluster). A 3-node Cassandra pool concentrated in one AZ cannot spread
across zones no matter what the stack requests, and with `enabled: false` the
pods fall back to default scheduling regardless of your labels. If you run a
single shared pool instead, set `global.nodeSelectors.enabled: true` with
`global.nodeSelectors.all` and size it to hold every replica on distinct
nodes/AZs.

## Enabling HA

Set the mode in your environment file (for example
`deploy/stacks/self-managed/environments/<env>.yaml`):

```yaml
highAvailability:
  mode: ha-preferred
```

That single line activates all of the defaults documented below. Every knob
under `highAvailability` has a sensible default; override only what you need.
Then apply the stack as usual:

```bash
helmfile -e <env> apply
```

An invalid mode fails the render fast with a clear error, so a typo cannot
silently disable HA.

## What HA changes, by tier

### Stateless control-plane services

Active-active Deployments with no leader election: `nvcf-api`,
`invocation-service`, `grpc-proxy`, `admin-token-issuer-proxy`, and
`llm-api-gateway` (when the LLM addon is enabled).

Under HA each of these gets:

- **2 replicas** (`highAvailability.stateless.replicaCount`).
- **Hostname pod anti-affinity** so the two replicas never share a node.
- **Zone topology spread** (`topology.kubernetes.io/zone`, `maxSkew: 1`) so they
  land in different AZs when zones are labelled.
- **A PodDisruptionBudget** (`minAvailable: 1`) so voluntary disruptions
  (drains, upgrades) never take the last replica.
- **A surge rolling-update strategy** (`maxSurge: 1`, `maxUnavailable: 0`) so a
  new pod is Ready before an old one is removed.

### Hot-path helper services

`rateLimiter` and `nats-auth-callout` sit on the request/auth hot path, so under
HA they are raised to **2 replicas** (`highAvailability.hotPath.replicaCount`)
with their own PDB (`minAvailable: 1`). They reuse the stateless hostname
anti-affinity and zone spread.

### Tier-2 quorum services (data durability)

`Cassandra`, `NATS`, and `OpenBao` run as **3-replica quorum StatefulSets** with:

- **Hostname pod anti-affinity** so the 3 peers land on 3 distinct nodes
  (`highAvailability.tier2.podAntiAffinity`). Following the mode, this is
  preferred (soft) under `ha-preferred` and required (hard) under `ha-enforced`.
  OpenBao's upstream chart ships a hard anti-affinity that the stack disables for
  single-node installs and re-enables (soft/hard by mode) under HA.
- **PodDisruptionBudgets** sized to preserve quorum (`minAvailable: 2`).

#### Zone spread for the quorum pods (opt-in)

By default the quorum peers are only guaranteed distinct **nodes**, not distinct
**zones** — three nodes can all be in one AZ, so a single-AZ loss could still
break quorum. To spread the three peers across `topology.kubernetes.io/zone`,
enable:

```yaml
highAvailability:
  tier2:
    topologySpread:
      enabled: true      # off by default
      maxSkew: 1
      strict: false      # ScheduleAnyway (soft). Set true only on >= 3 AZs.
```

This is **opt-in** because, unlike the stateless tier, it depends on
infrastructure you must provide:

- **StorageClass `volumeBindingMode: WaitForFirstConsumer`.** Each quorum pod has
  a zonal PersistentVolume, and a zonal disk can only attach to a node in its own
  AZ. With `WaitForFirstConsumer`, the scheduler places the pod first (honoring
  the spread constraint) and the PV is then created in that pod's zone. With
  `Immediate` binding the PV's zone is chosen up front and the pod is pinned to
  it, which fights the spread constraint and can leave pods `Pending`. The stack
  cannot set this for you — it is a property of the StorageClass you supply.
- **Capacity in at least 3 AZs.** A 3-member quorum only survives an AZ loss if
  no single AZ holds a majority. With only 2 AZs one zone inevitably holds 2 of
  3 members, and losing that zone breaks quorum. On a 2-AZ cluster leave zone
  spread off (or keep `strict: false`).

`whenUnsatisfiable` defaults to `ScheduleAnyway` (soft) so a temporarily short
AZ never leaves a peer `Pending`. Only set `strict: true` — which makes it
`DoNotSchedule` (hard) — on clusters you know have capacity in ≥3 AZs.

**Cassandra needs one more thing: rack = AZ.** Spreading the *pods* across zones
does not by itself make the *data* zone-diverse. `NetworkTopologyStrategy`
replicates by **rack**, and Cassandra's rack is assigned by the image entrypoint,
not by the pod's Kubernetes zone. Unless each pod's Cassandra rack is set to its
AZ, RF=3 can still place all three data replicas in one rack. Map rack to AZ on
the Cassandra nodes to get true cross-AZ data placement; NATS and OpenBao (Raft)
replicate per member and need only the pod spread.

Once a quorum pod's PV is created in a zone it is pinned there for the life of
that StatefulSet ordinal — steady-state placement stays spread, but a pod whose
AZ is lost cannot reschedule elsewhere until the AZ returns (its two peers carry
quorum in the meantime).

Beyond placement, HA also raises the data-durability settings:

#### NATS JetStream replica factor

Streams default to a single replica. Under HA the stack sets the JetStream
replica factor (RF) to **2** (`highAvailability.nats.jetstream.replicaFactor`)
on the two services that create streams — `nvcf-api` (via `NVCF_NATS_REPLICAS`)
and `invocation-service` (via `NATS_PROPERTIES__REPLICAS`). With RF=2 the
worker/result streams are replicated across the NATS cluster and survive the
loss of the node hosting the leader. RF must be `<= highAvailability.nats.replicas`.

#### Cassandra replication and consistency

The Cassandra keyspaces are created with `NetworkTopologyStrategy` and a
replication factor equal to `highAvailability.cassandra.replicaCount` (3 under
HA), and the control-plane services read/write at `LOCAL_QUORUM`. This is the
correct configuration for both single-DC and multi-AZ deployments:

- **Single datacenter:** RF=3 with `LOCAL_QUORUM` tolerates the loss of one
  replica for reads and writes.
- **Multi-AZ:** because replicas are placed with `NetworkTopologyStrategy`,
  labelling nodes by rack/AZ makes Cassandra distribute the 3 replicas across
  AZs automatically; `LOCAL_QUORUM` then keeps the cluster available through the
  loss of a single AZ.

No stack change is required to select the strategy — it is
`NetworkTopologyStrategy` in all cases. To get true cross-AZ placement, ensure
the Cassandra nodes carry AZ labels (see the prerequisites above).

## Validation

After applying HA, confirm replicas are spread as expected.

Check that quorum peers landed on distinct nodes and AZs:

```bash
# Nodes and their AZ labels
kubectl get nodes -L topology.kubernetes.io/zone

# Cassandra / NATS / OpenBao pods with their nodes
kubectl -n cassandra-system get pods -o wide
kubectl -n nats-system get pods -o wide
kubectl -n vault-system get pods -o wide
```

Confirm the stateless Deployments scaled and spread:

```bash
kubectl -n nvcf get deploy nvcf-api invocation-service -o wide
kubectl -n nvcf get pods -o wide -l app.kubernetes.io/instance=nvcf-api
```

Confirm the JetStream RF took effect (streams report `Replicas: 2`):

```bash
kubectl -n nats-system exec -it nats-0 -- nats stream ls
kubectl -n nats-system exec -it nats-0 -- nats stream info <stream-name>
```

Confirm Cassandra keyspace replication:

```bash
kubectl -n cassandra-system exec -it cassandra-0 -- \
  cqlsh -e "SELECT keyspace_name, replication FROM system_schema.keyspaces;"
```

If any pod is stuck `Pending` under `ha-enforced`, it usually means a node pool
lacks capacity in a second node/AZ. Add capacity, or drop to `ha-preferred` to
let it schedule while you rebalance.

## Recovery objectives and failure behavior

With HA enabled and capacity in at least two AZs:

- **Single node loss:** Stateless and hot-path Deployments keep serving from
  their surviving replica; the scheduler recreates the lost pod on another node
  (and the PDB prevents drains from removing the last one). Quorum services
  (Cassandra RF=3/`LOCAL_QUORUM`, NATS RF=2, OpenBao 3-node Raft) retain quorum
  with 2 of 3 members and continue serving reads and writes.
- **Single AZ loss:** With replicas spread across AZs, the control plane stays
  available on the surviving AZ(s). Recovery time is dominated by pod
  reschedule/restart time on the healthy AZ rather than any manual failover.
- **Two simultaneous quorum-member losses:** A 3-member quorum service loses
  quorum and pauses writes until a member returns. This is why three AZs (or at
  least three nodes across two AZs, with the third member able to reschedule) is
  the durable target.

HA reduces recovery to automatic rescheduling within surviving failure domains;
it does not replace backups. Continue to back up Cassandra and OpenBao per the
[Control Plane Operations](./control-plane-operations.md) runbooks.

## Related

- [Control Plane Operations](./control-plane-operations.md) — service reference,
  key rotation, and upgrade runbooks.
- [Infrastructure Sizing](./infrastructure-sizing.md) — node pool sizing
  guidance.
- [Helmfile Installation](./helmfile-installation.md) — how environment values
  and `global.yaml.gotmpl` are applied.
