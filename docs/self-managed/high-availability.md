# High Availability

This guide explains how to run the self-hosted NVCF control plane in a
high-availability (HA) topology so that it survives the loss of a single node
or availability zone (AZ). It covers the cluster prerequisites you must provide,
how to turn HA on, what each service does under HA, and how to validate the
result.

HA is on by default (`highAvailability.mode: preferred`). Single-node installs
(local, CI, small proofs of concept) should turn it off by setting
`highAvailability.mode: none`.

## Overview

HA is controlled by a single switch in your Helmfile environment values,
`highAvailability.mode`. There is no per-component HA configuration beneath
it: sizing and placement are derived uniformly from the mode for every
in-scope release, in `deploy/stacks/self-managed/global.yaml.gotmpl`.

| `highAvailability.mode` | Behavior |
| --- | --- |
| `none` | Keep existing single-replica chart/env values. Nothing in this guide applies. Set this for local, CI, and single-node installs. |
| `preferred` (default) | HA sizing (multiple replicas, quorum services at 3). Node/AZ spread is a **preference**: if a second node or AZ has no capacity, pods still schedule (co-located) rather than staying `Pending`. |
| `enforced` | Same HA sizing, but node/AZ spread is **required**: a replica that cannot land on a distinct node/AZ stays `Pending` instead of packing onto an occupied one. |

`preferred` is the recommended starting point: it gives you the full HA
topology while degrading gracefully on a constrained cluster. Move to
`enforced` once you have confirmed your node pools have capacity in every AZ
and you want hard placement guarantees.

`preferred` guarantees continuity after a single **pod** failure but provides
only best-effort node/zone separation — under capacity pressure, replicas can
still end up co-located. `enforced` guarantees continuity after a single pod
**or node** failure when the prerequisites below are met. Neither mode alone
claims arbitrary availability-zone or site-loss tolerance; see
[Zone spread for the quorum services](#zone-spread-for-the-quorum-services)
and [Recovery objectives](#recovery-objectives-and-failure-behavior) for what
it takes to survive an AZ loss specifically.

## Cluster prerequisites

HA depends on infrastructure the **operator** provides. The stack cannot create
nodes or AZs for you — it only schedules against what you label.

### 1. Node count

Both HA modes require **at least 3 schedulable nodes** in the pool(s) that host
control-plane and quorum workloads for the *intended* spread to actually
schedule — the quorum services (Cassandra, NATS, OpenBao) run 3 replicas.
Under `preferred`, fewer nodes degrade to co-located (but still Ready) pods
rather than blocking the install; under `enforced`, insufficient nodes leave
replicas `Pending`.

### 2. Availability-zone labels

To spread replicas across failure domains, the scheduler uses the standard
Kubernetes well-known label on your nodes:

```text
topology.kubernetes.io/zone=<az>
```

You (the operator) must ensure this label is present on every node. Managed
Kubernetes services (EKS, AKS, GKE) apply it automatically. On bare-metal or
custom clusters, set it yourself, for example:

```bash
kubectl label node <node-name> topology.kubernetes.io/zone=az-1
```

If the label is absent, zone topology spread has nothing to spread across. In
`preferred` this silently degrades to node-level spread only; in
`enforced` zone-constrained pods can stay `Pending`. Aim for capacity in
**at least two AZs** (three is better for the quorum services — see
[Zone spread for the quorum services](#zone-spread-for-the-quorum-services)).

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
| `controlplane` | `control-plane` | All Tier-1 Deployments (api, invocation, grpcproxy, adminIssuerProxy, rateLimiter, natsAuthCalloutService, llmApiGateway, nats) |
| `cassandra` | `cassandra` | Cassandra StatefulSet |
| `vault` | `vault` | OpenBao StatefulSet |

These same three classes (`controlplane`/`cassandra`/`vault`) are also the
classes used by `global.affinity` and `global.topologySpreadConstraints`
below, so node-pool selection and scheduling-policy tuning stay consistent.

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
  mode: preferred
```

That single line activates all of the defaults documented below. Then apply
the stack as usual:

```bash
helmfile -e <env> apply
```

An invalid mode fails the render fast with a clear error, so a typo cannot
silently disable HA.

## What HA changes, by tier

### Replica-safe Deployments

Active-active Deployments with no leader election: `api`, `rateLimiter`,
`natsAuthCalloutService`, `adminIssuerProxy`, `llmApiGateway` (when the LLM
addon is enabled), and the control-plane services `nvctApi`, `notary`, `sis`,
`apiKeys`, and `reval` all follow the same convention. (`nvctApi` and `sis` run
scheduled jobs but coordinate them with a distributed lock, so they scale
safely.)

> **Note — `invocation-service` and `grpc-proxy` are deferred.** These two are
> stateless too, but their multi-replica scaling is intentionally **held at a
> single replica for now**, pending Envoy support in the self-hosted stack.
> Worker callbacks are host-bound to the specific pod that accepted the request
> (per-pod pod-IP / DNS addressing), which is safe in a single cluster; the
> Envoy dependency is for the cross-cluster case. Until then they keep hostname
> anti-affinity and zone spread (no-ops at one replica) and get **no HA PDB**
> (a `minAvailable: 1` PDB on a singleton would block node drains).
>
> **Note — `ess` (Encrypted Secret Store) stays single-replica.** ESS runs
> scheduled cryptographic jobs (key rotation, re-encryption, key promotion) that
> are not yet coordinated across pods (no distributed lock), so multiple replicas
> would fire them concurrently. It is intentionally excluded from HA sizing and
> stays at one replica until that coordination is added.

Under HA each of these gets:

- **2 replicas.**
- **Hostname pod anti-affinity** so the two replicas never share a node.
- **Zone topology spread** (`topology.kubernetes.io/zone`, `maxSkew: 1`) so they
  land in different AZs when zones are labelled.
- **A PodDisruptionBudget** (`minAvailable: 1`) so voluntary disruptions
  (drains, upgrades) never take the last replica.
- **A surge rolling-update strategy** (`maxSurge: 1`, `maxUnavailable: 0`) so a
  new pod is Ready before an old one is removed.

Affinity and topology spread are shared scheduling policy, so they can be
tuned once for every release of a class instead of per-component. See
[Tuning scheduling policy](#tuning-scheduling-policy-globalaffinity--globaltopologyspreadconstraints)
below.

### Quorum services (data durability)

`Cassandra`, `NATS`, and `OpenBao` run as **3-replica quorum StatefulSets** with:

- **Hostname pod anti-affinity** so the 3 peers land on 3 distinct nodes.
  Following the mode, this is preferred (soft) under `preferred` and required
  (hard) under `enforced` — the same convention as replica-safe Deployments.
  OpenBao's upstream chart ships a hard anti-affinity that the stack disables
  for single-node installs and re-enables (soft/hard by mode) under HA.
- **Zone topology spread** across `topology.kubernetes.io/zone`, same
  soft/hard-by-mode convention. See the next section for what it takes for
  this to actually protect against an AZ loss.
- **PodDisruptionBudgets** sized to tolerate exactly one voluntary disruption
  (`minAvailable: 2` of 3, or OpenBao's equivalent `maxUnavailable: 1`).

#### Zone spread for the quorum services

Zone topology spread for the quorum peers is part of the same mode
convention as everything else — it is **not** a separate toggle. That said,
whether it actually protects you from an AZ loss depends on infrastructure
you must provide:

- **StorageClass `volumeBindingMode: WaitForFirstConsumer`.** Each quorum pod
  has a zonal PersistentVolume, and a zonal disk can only attach to a node in
  its own AZ. With `WaitForFirstConsumer`, the scheduler places the pod first
  (honoring the spread constraint) and the PV is then created in that pod's
  zone. With `Immediate` binding the PV's zone is chosen up front and the pod
  is pinned to it, which fights the spread constraint and can leave pods
  `Pending`. The stack cannot set this for you — it is a property of the
  StorageClass you supply.
- **Capacity in at least 3 AZs.** A 3-member quorum only survives an AZ loss
  if no single AZ holds a majority. With only 2 AZs one zone inevitably holds
  2 of 3 members, and losing that zone breaks quorum.

If you do not have 3-AZ capacity or a `WaitForFirstConsumer` StorageClass,
zone spread for the quorum tier will not reliably schedule or will not
protect you from an AZ loss even though it renders. Switching from `enforced`
to `preferred` alone does **not** fix this — `preferred`'s soft
(`ScheduleAnyway`) constraint will let pods co-locate in that case rather than
failing loudly, which hides the gap rather than closing it. To disable
generated zone spread for the quorum tier explicitly instead of relying on a
mode change, use the `global.topologySpreadConstraints` escape hatch:

```yaml
global:
  topologySpreadConstraints:
    cassandra: []
    vault: []
```

(NATS shares the `controlplane` class with the stateless tier; scope an
override to just NATS via a component-shaped value instead if you need to
disable spread for NATS specifically without affecting the rest of
`controlplane`.)

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
replica factor (RF) to **3** (`highAvailability.nats.jetstream.replicaFactor`)
on the two services that create streams — `nvcf-api` (via `NVCF_NATS_REPLICAS`)
and `invocation-service` (via `NATS_PROPERTIES__REPLICAS`). JetStream streams
use Raft quorum: RF=3 tolerates the loss of one replica, matching the
3-member NATS cluster. **RF=2 is not sufficient** — a 2-member Raft group
loses quorum the moment either replica is unavailable, so it provides no
resilience benefit over RF=1.

#### Cassandra replication and consistency

The Cassandra keyspaces are created with `NetworkTopologyStrategy` and a
replication factor of 3 under HA, and the control-plane services read/write
at `LOCAL_QUORUM`. This is the correct configuration for both single-DC and
multi-AZ deployments:

- **Single datacenter:** RF=3 with `LOCAL_QUORUM` tolerates the loss of one
  replica for reads and writes.
- **Multi-AZ:** because replicas are placed with `NetworkTopologyStrategy`,
  labelling nodes by rack/AZ makes Cassandra distribute the 3 replicas across
  AZs automatically; `LOCAL_QUORUM` then keeps the cluster available through the
  loss of a single AZ.

No stack change is required to select the strategy — it is
`NetworkTopologyStrategy` in all cases. To get true cross-AZ placement, ensure
the Cassandra nodes carry AZ labels (see the prerequisites above).

## Tuning scheduling policy (`global.affinity` / `global.topologySpreadConstraints`)

Affinity and zone topology spread are shared scheduling policy, so you can
tune them once per workload class instead of overriding every component
individually. Both resolve in the same order:

```text
class-specific global value  ->  global.<key>.all  ->  the highAvailability.mode-derived convention
```

The available classes match `global.nodeSelectors`: `controlplane`,
`cassandra`, `vault`. These values are optional — absence means "use the
mode-derived convention" — and an explicit override replaces the generated
policy for that class entirely, it does not merge with it:

```yaml
highAvailability:
  mode: preferred

global:
  affinity:
    cassandra:
      podAntiAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchLabels:
                app.kubernetes.io/instance: cassandra
            topologyKey: kubernetes.io/hostname
  topologySpreadConstraints:
    all: []   # disable generated zone spread everywhere except explicit class overrides
```

An explicit `{}` (for `affinity`) or `[]` (for `topologySpreadConstraints`)
suppresses the generated policy for that class — this is different from
omitting the key, which falls back to `all` and then to the convention.

Component-shaped values (for example `api.affinity`, `cassandra.affinity`) in
your environment file remain the final, single-release escape hatch and take
precedence over both `global.affinity` and the generated convention.

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

Confirm the replica-safe Deployments scaled and spread:

```bash
kubectl -n nvcf get deploy nvcf-api admin-token-issuer-proxy -o wide
kubectl -n nvcf get pods -o wide -l app.kubernetes.io/instance=nvcf-api
# invocation-service and grpc-proxy stay at 1 replica for now (deferred until Envoy);
# ess also stays at 1 (excluded from HA sizing, see note above)
kubectl -n nvcf get deploy invocation-service grpc-proxy -o wide
kubectl -n ess get deploy ess-api -o wide
```

Confirm the JetStream RF took effect (streams report `Replicas: 3`):

```bash
kubectl -n nats-system exec -it nats-0 -- nats stream ls
kubectl -n nats-system exec -it nats-0 -- nats stream info <stream-name>
```

Confirm Cassandra keyspace replication:

```bash
kubectl -n cassandra-system exec -it cassandra-0 -- \
  cqlsh -e "SELECT keyspace_name, replication FROM system_schema.keyspaces;"
```

If any pod is stuck `Pending` under `enforced`, it usually means a node pool
lacks capacity in a second node/AZ. Add capacity, or drop to `preferred` to
let it schedule while you rebalance — but see
[Zone spread for the quorum services](#zone-spread-for-the-quorum-services)
for why that alone does not restore the AZ-loss guarantee if the underlying
capacity gap remains.

## Recovery objectives and failure behavior

With HA enabled and capacity in at least two AZs:

- **Single node loss:** Multi-replica replica-safe Deployments keep
  serving from their surviving replica; the scheduler recreates the lost pod on
  another node (and the PDB prevents drains from removing the last one).
  `invocation-service` and `grpc-proxy` (single replica until Envoy) are briefly
  unavailable while the scheduler restarts the pod on another node. Quorum
  services (Cassandra RF=3/`LOCAL_QUORUM`, NATS RF=3, OpenBao 3-node Raft)
  retain quorum with 2 of 3 members and continue serving reads and writes.
- **Single AZ loss:** Only if zone spread actually took effect — see
  [Zone spread for the quorum services](#zone-spread-for-the-quorum-services)
  for its prerequisites (3-AZ capacity, `WaitForFirstConsumer` storage). With
  those met, the control plane stays available on the surviving AZ(s) and
  recovery time is dominated by pod reschedule/restart time rather than any
  manual failover. Without them, an AZ loss can take a majority of a quorum
  service's members and pause writes even with HA enabled.
- **Two simultaneous quorum-member losses:** A 3-member quorum service loses
  quorum and pauses writes until a member returns. This is why three AZs (or at
  least three nodes across two AZs, with the third member able to reschedule) is
  the durable target.

HA reduces recovery to automatic rescheduling within surviving failure domains;
it does not replace backups. Continue to back up Cassandra and OpenBao per the
[Control Plane Operations](./control-plane-operations.md) runbooks.

## Upgrading an existing install

HA ships in a specific self-managed stack release. On a running install, adopt it
in this order:

1. **Upgrade to the release that includes HA.** Check out the stack release that
   carries these changes and use its `deploy/stacks/self-managed/` directory. Your
   `environments/<environment-name>.yaml` and `secrets/` carry over unchanged.
2. **Make the new artifacts available.** If you pull directly from NGC
   (`nvcr.io` / `helm.ngc.nvidia.com`), no action is needed. If you mirror to a
   private registry, re-mirror the stack's charts and images **before** syncing —
   otherwise pods fail with `ImagePullBackOff`. See
   [Image Mirroring](/nvcf/overview/image-mirroring) and the [Artifact Manifest](/nvcf/overview/manifest).
3. **Choose the mode.** HA is on by default (`preferred`), so an environment
   file that does not set `highAvailability.mode` **activates HA on upgrade**.
   Confirm the [cluster prerequisites](#cluster-prerequisites) first — `preferred`
   needs ≥3 schedulable nodes; `enforced` also needs ≥3 AZs with capacity, AZ
   labels, and `WaitForFirstConsumer` storage. If the cluster is not ready (or it
   is a single-node install), set `highAvailability.mode: none` to keep the
   current single-replica behavior.
4. **Apply.** `HELMFILE_ENV=<environment-name> helmfile sync`. Replica-safe
   Deployments roll with `maxUnavailable: 0` / `maxSurge: 1` (needs headroom for
   one surge pod); quorum services roll one member at a time and keep quorum.
5. **Verify.** Confirm the [Validation](#validation) checks — replica counts
   (2 or 3), PodDisruptionBudgets, and zone spread.

> Because `preferred` is the default, upgrading with an environment file that
> does not pin `highAvailability.mode` will scale services up (2/3 replicas,
> PDBs, placement) on the first `sync`. Set `mode: none` beforehand if you are
> not ready for HA.

## Related

- [Control Plane Operations](./control-plane-operations.md) — service reference,
  key rotation, and upgrade runbooks.
- [Infrastructure Sizing](/nvcf/overview/infrastructure-sizing) — node pool sizing
  guidance.
- [Helmfile Installation](./helmfile-installation.md) — how environment values
  and `global.yaml.gotmpl` are applied.
