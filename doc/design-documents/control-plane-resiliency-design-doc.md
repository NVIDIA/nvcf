# NVCF Self-Hosted: Control Plane Resiliency

**Design doc for [#1063](https://github.com/NVIDIA/nvcf/issues/1063)**

Note - Throughout, **operator** means the company running NVCF self-hosted as a platform — the customer of this document.

## Problem

NVCF self-hosted today runs **every control-plane component as a single replica**. All state lives in one Cassandra, all work is queued through one NATS, and every GPU cluster registers to one control plane.

That is enough to evaluate the platform. For production it breaks down:

- **One node failure is a full outage** — everything stops until the pod is rescheduled, which takes minutes.
- **In-flight work is lost** — running invocations fail and NVCA agents lose their connection.
- **The singleton services have no failover path** — not the NATS JetStream leader, the OpenBao active node, or the Cassandra coordinator.
- **Every upgrade needs downtime** — a single-replica rollout cannot roll.

## Scope

This document covers the resilience of a single NVCF self-hosted control plane within one site: surviving the loss of any pod or node, and upgrading without downtime.

Running more than one control plane — for site-level failover, capacity pooling, or latency — is out of scope and tracked separately (see [Looking Ahead](#looking-ahead)).

## Goals

- **Survive single node or pod failure without disruption.** No failure inside a site interrupts service or requires an operator to intervene.
- **Make resilience adoptable, not a rebuild.** An existing deployment reaches the resilient topology by changing configuration, not by migrating to a new platform.

## Non-Goals

NVCF provides the control plane. The operator owns everything customer-facing above it, and the infrastructure underneath. Out of scope:

- **Compute plane resilience** — NVCA runs on the GPU cluster. This document covers the control plane's tolerance of NVCA disconnection, not NVCA's own availability.
- **Resilience of customer workload containers or inference pods** — tenants own their function images and the code inside them.
- **Backup and restore of the database or secret store** — a separate workstream. This document covers surviving failures, not recovering from data loss.
- **The operator layer — the app, the traffic layer, and the gateways** — the operator brings their own. NVCF exposes what those layers consume.
- **Any change to how end users invoke functions** — the invocation contract is unchanged.

## Design and Architecture

### Target Outcomes

| Tier | Components | Serving model | Target behavior |
| :--- | :--- | :--- | :--- |
| Tier-1 Stateless | All replica-safe serving services (nvcf-api, admin-token-issuer-proxy, llm-api-gateway, rateLimiter, nats-auth-callout, api-keys, icms, notary, reval, nvct-api and ess) | Active–active | ≥2 Ready replicas spread across nodes/AZs. On single pod/node loss: no sustained outage — surviving replicas keep serving; connections on the lost pod may reset and clients retry.<br /><br />ESS's scheduled crypto jobs (rotation, re-encryption, promotion) are `@ConditionalOnProperty(encryption.*.scheduled.enabled=true)` and default off. They run only in a separate ESS worker deployment that is not part of the self-hosted stack; the in-stack ESS API runs none of them, so it scales safely to 2. |
| Named exceptions (single replica) | grpc-proxy, invocation-service (Envoy-gated) | Single replica for now | Held at 1 with documented limitations; join the convention once Envoy lands (grpc-proxy/IS) |
| Tier-2 Quorum | NATS JetStream, OpenBao (Raft), Cassandra | Quorum (NATS/OpenBao leader-elected; Cassandra masterless) | 3 members on distinct nodes and AZs |
| Ops contract | All of the above | One Helmfile knob (`highAvailability.mode`) | zero-downtime Tier-1 upgrades; testable failure scenarios |

```text
Tier-1 (active-active)
  Client → Service/Gateway → Pod [AvailabilityZone=az-a] (Ready)
                           → Pod [AvailabilityZone=az-b] (Ready)
  az-a pod dies  →  endpoints update  →  az-b already serving

Tier-2 (quorum)
  3 members on 3 nodes → majority (2 of 3) keeps working if 1 dies
  NATS / OpenBao (Raft): leader re-elected on loss
  Cassandra: masterless — availability from replication + LOCAL_QUORUM
```

### Design Principles

1. **Convention over configuration.** The operator states an outcome (`highAvailability.mode`); the stack derives the coupled mechanics (replica counts, PDBs, rollout, quorum sizing, placement). Operators do not assemble an HA profile from mechanism-level toggles.
2. **One public knob.** A single `highAvailability.mode` enum. No per-service or per-mechanism HA sub-trees in the public values. `preferred` = soft placement; `enforced` = hard placement; both derive the same sizing/PDB/rollout.
3. **Cross-cutting scheduling is tunable once.** Affinity and topology spread follow the existing `global.nodeSelectors` pattern (`global.affinity` / `global.topologySpreadConstraints`, class → all → mode convention). Component-shaped values are the final escape hatch.
4. **Coupled invariants are derived, never independent toggles.** Replicas, PDB thresholds, rollout strategy, 3-member quorum, and JetStream RF=3 move together.
5. **AZs are Kubernetes zones.** Nodes are labelled `topology.kubernetes.io/zone`; placement is applied via chart values, never by `kubectl edit`.

### Architecture Overview

#### High Level Topology

One Kubernetes cluster spanning one site, two AZs, multiple nodes in each AZ.

- **Tier-1 (active-active):** replicas spread across nodes/AZs.
- **Tier-2 (quorum):** 3 members on distinct nodes and AZs; NATS/OpenBao (Raft) elect a leader; Cassandra is masterless (availability from replication + LOCAL_QUORUM).
- **≥3 nodes required** for quorum — a majority must survive one loss; 2 nodes cannot.
- Placement strictness (soft vs hard, node and zone) is derived from mode — it is not a separate opt-in.
- **Two-AZ caveat:** a 3-member quorum splits 2+1, so losing the AZ holding the majority breaks quorum. Surviving an arbitrary AZ loss needs ≥3 AZs; surviving whole-site loss needs DR (out of scope). Neither mode claims AZ- or site-loss tolerance.

#### Terminology

| Term | Meaning |
| :--- | :--- |
| Cluster | One Kubernetes control-plane install |
| Node | One physical or virtual server in that cluster |
| Site / room | Where a control plane runs. This document covers one site |
| Availability zone (AZ) | Failure domain inside that cluster (az-a, az-b) |
| Zone label | `topology.kubernetes.io/zone=az-a` or `az-b` |
| Failure domain | Boundary where a shared failure can take out multiple machines (a room/AZ). Replicas are spread across domains |
| Pod / replica | One running copy of a service, scheduled onto a node |
| Active–active | All Ready replicas serve traffic (Tier-1) |
| Quorum | Majority of replicas required for progress (2 of 3) |
| Leader election | Stateful system picks a new active pod after leader loss |
| Replication factor (RF) | Number of data/stream copies across replicas (RF=3 = 3 copies) |
| Anti-affinity | Scheduling rule to keep replicas off the same node |
| Topology spread | Scheduling rule to spread replicas across AZs |
| PodDisruptionBudget (PDB) | Limit on how many pods can be taken down at once during voluntary drains/upgrades |
| RTO / RPO | Recovery time objective / recovery point objective |
| KSM | kube-state-metrics |
| Workload class | Scheduling grouping used by `global.*` fallback (controlplane, cassandra, vault) |

#### Control plane vs compute scope

| Layer | Cluster model | Scope |
| :--- | :--- | :--- |
| Control plane (API, NATS, OpenBao, Cassandra, …) | One self-managed stack | In scope |
| Compute / GPU (NVCA, function pods) | One or more registered clusters | Out of scope |

### Component Design

#### Tier-1 replica-safe serving set

Active-active, no leader election. Under HA the entire replica-safe serving set gets the convention together (2 replicas, PDB, surge rollout, mode-derived hostname anti-affinity and zone spread); a service is either in the convention or a named exception. The set:

*nvcf-api, admin-token-issuer-proxy, llm-api-gateway (LLM addon), rateLimiter, nats-auth-callout, api-keys, icms, notary, reval, nvct-api*

Named exceptions — held at `replicaCount: 1`:

- *grpc-proxy* and *invocation-service* — Envoy-gated (worker-callback host binding / cross-cluster routing)

#### Envoy-gated exception: grpc-proxy and invocation-service

*grpc-proxy* and *invocation-service* are replica-safe but are held at `replicaCount: 1` until Envoy is enabled in the self-hosted stack, and this is recorded here as a named exception with its limitation.

- Both advertise a per-pod worker-callback address — *invocation-service* a per-pod DNS name (`<dashed-pod-ip>.invocation.<ns>.svc`, resolved by CoreDNS to the exact pod even on a plain ClusterIP Service), *grpc-proxy* a raw pod IP (`$POD_IP:10086`). In a single cluster the worker therefore reaches the exact originating pod, so per-pod host binding already works without Envoy.
- Envoy is the prerequisite for the cross-cluster case (a worker in a different cluster cannot reach in-cluster pod IPs / `*.svc.cluster.local`), where a router must deliver each worker tunnel to the correct pod. Scaling these two to ≥2 is gated on Envoy enablement.
- While deferred, hostname anti-affinity/zone spread still render but are no-ops at one replica, and no HA PDB is set (a `minAvailable: 1` PDB on a singleton would block node drains).

**Caveat:** scaling to ≥2 does not make in-flight requests resilient. The callback is host-bound to the pod that accepted the request. If that pod dies after pushing work to NATS but before the worker callback completes, the callback state dies with it, the in-flight request fails and the client retries. A second replica serves new requests but cannot resume the dead pod's in-flight request. Reroutable callbacks (Envoy/router) are required for safe scaling and cross-cluster routing, but surviving an origin-pod death mid-request would additionally require replicated per-request state. In practice this remains a client-retry gap, not something scaling or rerouting alone closes.

#### Tier-2 Built-in quorum

Stateful systems that stay available through a majority. NATS and OpenBao use Raft consensus with an elected leader; Cassandra is masterless, its availability comes from replication + quorum consistency. Run 3 members (odd, so a majority survives one loss), one per node across AZs. Under HA each gets mode-derived hostname anti-affinity and zone spread (soft under `preferred`, hard under `enforced`).

| Component | How we run it | Failover |
| :--- | :--- | :--- |
| NATS JetStream | 3 pods, one per node. RF=3 → 3 copies of each stream message (one per member). Separately, a write commits at Raft quorum = 2 of 3 (majority ack), so the stream keeps accepting writes with 1 member down. | Raft elects a new leader from the surviving majority; clients retry with backoff |
| OpenBao (Raft HA) | 3 server pods (1 active / 2 standby); injector already at 2 | A standby is promoted to active; clients retry with backoff |
| Cassandra | 3 nodes, one pod per node. Keyspaces use NetworkTopologyStrategy with RF = replicaCount; apps read/write at LOCAL_QUORUM. | Lose 1 node = no data loss / no downtime; RF change needs migration |

#### NATS JetStream replication factor

These are two distinct numbers:

- **Replication factor (RF)** = how many copies of each stream message JetStream keeps. RF=3 → 3 copies, one per member. This is the durability/redundancy setting.
- **Quorum** = how many members must acknowledge a write for it to commit. JetStream stream groups use Raft, so quorum is a majority = 2 of 3. This is the consistency/availability threshold; it is not configurable and is not the same as RF.

With RF=3, all 3 members hold a copy and a write commits once any 2 ack, so the stream tolerates one member down.

#### Upgrade Impact

- **Tier-1** — Two different mechanisms cover two different events, they are not interchangeable:
  - Rolling update (a Deployment spec change, e.g. a new image on `helm upgrade`) is governed only by the rollout strategy: `maxUnavailable: 0` with `maxSurge: 1` brings a new pod to Ready before an old one is removed, so the Ready endpoint count never drops. The PDB does not participate in a rolling update.
  - Voluntary eviction (`kubectl drain`, node upgrade/replacement) is governed only by the PodDisruptionBudget: `minAvailable: 1` stops the eviction from removing the last Ready pod. `maxSurge`/`maxUnavailable` do not participate in a drain. With `replicaCount` ≥ 2 both hold ≥1 Ready endpoint through their respective event. The cluster must have capacity for the surge pod during rollouts.
- **Tier-2** — NATS/OpenBao recover via built-in leader election/promotion; Cassandra has no leader — a restarted node rejoins and LOCAL_QUORUM reads/writes continue on the surviving replicas. Clients retry with backoff; evict at most one member at a time. A PDB that blocks `kubectl drain` means the remaining nodes cannot host the required Ready count — add capacity, do not disable the PDB.
- Setting mode to `preferred`/`enforced` is a scale-up needing ≥3 nodes. A Cassandra RF change is a keyspace migration and must be preflighted, not treated as an ordinary `replicaCount` change. Upgrade Tier-1 first; upgrade NATS, OpenBao, and Cassandra independently.

### Configuration

Resilience is configured with a single value in the selected Helmfile environment:

```yaml
highAvailability:
  mode: preferred           # none | preferred | enforced
```

`highAvailability.mode` states the required behaviour and default placement strictness; the mapping layer owns translation to each chart's schema.

#### The enum

| Mode | Sizing / disruption / rollout | Placement | Guarantee |
| :--- | :--- | :--- | :--- |
| `preferred` (default) | HA replicas, PDBs, surge rollout | Soft hostname anti-affinity + soft zone spread (`ScheduleAnyway`) | Pod-loss resilience; node/zone separation best-effort |
| `enforced` | Same HA sizing | Hard hostname anti-affinity + hard zone spread (`DoNotSchedule`) | Single-node-loss resilience when prerequisites are met |
| `none` | Preserve existing chart/env effective values | Preserve ordinary placement | No additional HA guarantee |

#### Derived invariants

Both **preferred** and **enforced** derive the same coupled invariants; only placement hardness differs.

- Replica-safe Deployments: `replicaCount: 2`, PDB `minAvailable: 1`, rollout `RollingUpdate` with `maxSurge: 1` / `maxUnavailable: 0`.
- Quorum services: 3 members, PDB tolerating exactly one voluntary disruption.
- NATS JetStream: stream RF=3; an application invariant, not an operator value.

These are **not** exposed as knobs, so incoherent combinations (e.g. 2 replicas with the PDB disabled, or a 3-member quorum with a PDB allowing 2 disruptions) are unrepresentable.

#### Shared scheduling tuning

Affinity and topology spread are cross-cutting, so advanced tuning follows the existing node-selector pattern — define once by workload class, fall back to all, else the mode convention:

```yaml
highAvailability:
  mode: preferred
# global:
#   affinity:
#     all:          { ... }
#     controlplane: { ... }
#     cassandra:    { ... }
#     vault:        { ... }
#   topologySpreadConstraints:
#     all:          [ ... ]
#     controlplane: [ ... ]
```

How placement is resolved (per service): the template picks the first of these that is set:

1. the value for the service's workload class (e.g. cassandra),
2. else `global.<...>.all`,
3. else the placement derived from mode.

A key has three meanings — omitted, set, or empty are all different:

| | Meaning | Result |
| :--- | :--- | :--- |
| Key omitted | "No opinion" | Placement is derived from mode (the default) |
| a value (`{...}` / `[...]`) | "Use this policy" | your value is used |
| an empty `{}` / `[]` | "turn it off" | no placement for that class — not the same as omitting it |

This mechanism is for placement only — affinity, pod anti-affinity, topology spread, topology keys, weights. Replica counts, PDBs, and rollout are not tunable here; they are always derived from mode. To change one of those for a single release, use the chart-shaped escape hatch.

#### Escape hatches

The final override is chart-shaped and component-scoped, never under `highAvailability`:

```yaml
highAvailability:
  mode: enforced
api:
  replicaCount: 3   # exceptional capacity decision for one release
```

In-scope charts MUST expose the required value hooks.

### Cross-AZ Tier-2 Prerequisites (operator-provided)

Spreading the quorum services across AZs (so a single-AZ loss cannot take a majority) requires the following from the cluster operator — the value layer cannot supply any of these. These are prerequisites for `enforced` stateful placement in particular (hard `DoNotSchedule`); under `preferred` (soft) a short/absent AZ never leaves a peer Pending.

1. **≥3 availability zones** with real schedulable capacity in the relevant pool (a 1-1-1 split; on 2 AZs one zone holds the majority, so an AZ loss still breaks quorum).
2. **Zone labels on every node** (`topology.kubernetes.io/zone=<az>`).
3. **Dedicated node pools per Tier-2 workload** spanning the AZs — labelled `nvcf.nvidia.com/workload=cassandra|vault|control-plane`, with `global.nodeSelectors.enabled: true`.
4. **StorageClass `volumeBindingMode: WaitForFirstConsumer` + zonal-volume capacity per AZ.** Each quorum pod has a zonal PV; delayed binding lets the scheduler place the pod first (honouring spread) and create the PV in that pod's zone. Immediate binding pins the pod to the PV's zone and fights the spread constraint (pods can go Pending).
5. **rack = AZ** for true Cassandra data diversity. NATS/OpenBao need only pod spread.

Once a quorum pod's PV is created in a zone it is pinned there for that StatefulSet ordinal; a pod whose AZ is lost cannot reschedule until the AZ returns (its two peers carry quorum meanwhile).

### Application Requirements

1. Tier-1 services SHALL NOT introduce leader-election standby gating under this design unless classified as singletons separately.
2. Clients of NATS and OpenBao SHALL retry with backoff covering the leader election window.
3. Readiness probes SHALL exclude pods that cannot serve traffic.
4. NVCA reconnect/retry is assumed sufficient to bridge control-plane recovery; validation is in scope, NVCA redesign is not.

### Observability

#### Failover risk metrics and alerts

Goal: warn admins while the control plane is still serving, but HA headroom is gone (for example only one Ready Tier-1 replica left). This is early warning, not post-failure RTO measurement.

| Signal | At-risk condition |
| :--- | :--- |
| Tier-1 Ready count | Ready < configured `replicaCount` |
| Tier-1 AZ spread | All Ready replicas in one AZ |
| Tier-2 replicas count | Ready replicas < 3 |
| Tier-2 placement | Two or more peers on the same node |

#### Alert path

Self-hosted CP already ships VictoriaMetrics and nvcf-otel-collector. Failover-risk alerts reuse that path and add KSM plus alerting.

**NOTE:** kube-state-metrics (KSM) reports Kubernetes object Ready counts. It is not NVCF state-metrics (function / autoscaler).

Already in the CP stack (reused):

- VictoriaMetrics (vmsingle) — stores scraped metrics, serves PromQL.
- nvcf-otel-collector + ServiceMonitors — scrape targets, remote-write to VictoriaMetrics.

To add for failover-risk alerts:

- kube-state-metrics (KSM) — exposes Deployment / StatefulSet Ready-vs-desired counts.
- KSM scrape wiring — a ServiceMonitor (or scrape config) so the OTel collector pulls KSM into vmsingle.
- Alert rules (vmalert / Alertmanager) — fire when Tier-1 Ready < spec or Tier-2 Ready < 3.

The on-demand `nvcf-cli self-hosted check --control-plane` validator is complementary — a point-in-time PASS/FAIL check, not a continuous alert.

### Validation

The stack validates the effective rendered result, not the presence of the top-level flag.

**Preflight / runtime checks:**

- ≥3 schedulable nodes for the quorum services; ≥3 labelled zones when zonal spread is rendered.
- Node selectors/tolerations leave capacity for hostname separation and a Tier-1 surge pod.
- Cassandra/OpenBao storage can reattach after a node failure (delayed-binding StorageClass).
- Effective Ready replicas, PDBs and placement match the rendered specs.

**Runtime failure tests** (on a ≥3-node cluster):

| Test | Pass criteria |
| :--- | :--- |
| HA install, `mode: preferred` / `enforced` | Each in-convention Tier-1 Deployment ≥2 Ready; named exceptions (grpc-proxy, invocation-service) = 1 Ready; Tier-2 = 3 Ready |
| Anti-affinity / zone spread (test under `enforced`) | No two Tier-2 peers on one node; Tier-1 replicas not confined to one AZ (best-effort under `preferred`) |
| Tier-1 pod kill in AZ-A | Invocations continue on the surviving replica (AZ-B) |
| `kubectl drain` | PDB blocks eviction of the last Ready pod; service stays available |
| Node failure | Service recovers and resumes serving; record the RTO (detection + election/promotion + reconnect) |
| Rolling upgrade (`helm upgrade`) | No sustained Tier-1 invoke failure (guaranteed by `maxUnavailable: 0` / `maxSurge: 1`, not the PDB) |
| Cassandra single-pod stop | LOCAL_QUORUM reads/writes succeed on the surviving replicas (masterless; no leader) |
| Escape-hatch weakening | Validator reports the degraded guarantee (e.g. `mode: enforced` but `api` effective `replicaCount: 1`) |

**CLI (`nvcf-cli self-hosted check --control-plane`):**

Confirms the live cluster actually meets the HA guarantee for the intended mode. The operator or CI passes the profile they installed via `--mode` (`preferred` | `enforced` | `none`). The check runs against a live cluster.

The invariants:

- **Minimum replicas per role:** in-convention Tier-1 Deployments have `spec.replicas` ≥ 2 and Ready ≥ 2; the named exceptions (grpc-proxy, invocation-service) are exactly 1; Tier-2 StatefulSets are 3 Ready.
- **PDBs:** present with the correct floor (`minAvailable: 1` stateless; one-disruption for quorum).
- **Effective placement:** hostname anti-affinity present; zone `topologySpreadConstraints` present (and hard under `enforced`); nodes labelled `topology.kubernetes.io/zone`.
- **Degraded-guarantee reporting:** flags any workload whose effective objects fall below what the intended mode requires (e.g. `mode: enforced` but `api` effective `replicaCount: 1`, a missing PDB, or anti-affinity removed by an escape hatch).

Run the check after initial install, after `highAvailability.mode` is enabled or changed, and after any Helm upgrade, node drain, or node replacement.

## Risks and Mitigations

| Risk | Mitigation |
| :--- | :--- |
| Pressure for Tier-1 active–standby | Keep Tier-1 as active–active (no API leader election) |
| Chart lacks a hook → HA value is a silent no-op | Charts-first: add hooks in owning charts, release, then consume; validate effective objects |
| `enforced` zone spread leaves stateful pods Pending | Requires `WaitForFirstConsumer` SC + ≥3 AZs; `preferred` (soft) otherwise; preflight checks |
| Cassandra data not zone-diverse despite pod spread | Document rack = AZ requirement; NATS/OpenBao unaffected |
| Cassandra RF change (keyspace migration) | Explicit migration runbook |
| grpc-proxy / invocation-service scaling needs Envoy | Named exception at `replicaCount: 1`; per-pod binding works in-cluster; Envoy for cross-cluster |
| In-flight request tied to its originating pod | Client-retry gap; scaling adds availability for new requests, not in-flight survivability |
| NATS JetStream write availability | RF=3 (odd); losing a majority pauses writes; prefer ≥3 AZs for zone spread |
| Two-AZ topology only | Document residual AZ-loss risk; neither mode claims AZ-loss tolerance |

## Limitations

What this design does not do, stated so an operator can plan around it.

- **Losing an entire AZ is not guaranteed to be survivable.** With only two AZs, three quorum members split 2+1, so the majority may sit in the AZ that failed. Surviving loss of the whole site (both AZs) needs a second site / DR — out of scope.
- **Under `preferred`, replicas can end up packed onto one node.** Anti-affinity and zone spread are soft in that mode. A capacity preflight covers this; `enforced` makes both hard where the node pool supports it.
- **The gRPC worker callback path is single-replica.** grpc-proxy and invocation-service are not covered by the Tier-1 active-active model.

## Looking Ahead

This design makes a single control plane resilient, so no failure within its site interrupts service. Beyond it, there is still only one control plane, and it sits at one site:

- **Single point of failure** — lose it and every connected GPU cluster stops receiving work, however healthy those GPUs are.
- **Stranded capacity** — a GPU cluster is bound to one control plane, so capacity elsewhere cannot absorb load or be pooled.
- **No latency story** — an operator serving a large geography cannot place control planes closer to their users.

Letting a deployment run more than one control plane over shared state, so losing any one of them does not stop the platform, is tracked in [#41](https://github.com/NVIDIA/nvcf/issues/41), with its infrastructure dependencies in [#44](https://github.com/NVIDIA/nvcf/issues/44).
