# Capture and restore history

What captures, restores and pod starts did, kept beyond the agents' life,
so a cluster's performance can be followed over weeks and compared across
agent releases. Three records, each answering its own question:

| Record | Kept by | Answers |
|---|---|---|
| Prometheus series on each agent's `/metrics` | the cluster's Prometheus | trends: how long restores take, by phase; how often captures fail and why; how many pods start cold |
| One summary log line per capture, restore and pod start | the cluster's log store | one function's history: its captures and restores, with times |
| Kubernetes events on the pods; a capture's last restores in its group record | the API server | live debugging with `kubectl` alone |

## Metrics

Every label takes a bounded set of values. There is no namespace, pod,
function or checkpoint label: those are in the summary lines.

| Series | Type | Labels |
|---|---|---|
| `nvsnap_pod_ready_seconds` | histogram | `method`, `gpu`, `ranks` |
| `nvsnap_restore_phase_seconds` | histogram | `method`, `phase`, `gpu`, `ranks` |
| `nvsnap_restore_outcomes_total` | counter | `method`, `outcome`, `reason`, `gpu`, `ranks` |
| `nvsnap_restore_fetches_total` | counter | `source`, `result` |
| `nvsnap_restore_fetch_seconds` | histogram | `source` |
| `nvsnap_capture_phase_seconds` | histogram | `method`, `phase`, `gpu`, `ranks` |
| `nvsnap_capture_outcomes_total` | counter | `method`, `outcome`, `reason`, `gpu`, `ranks` |
| `nvsnap_capture_bytes` | histogram | `method`, `part`, `gpu` |
| `nvsnap_admissions_total` | counter | `decision`, `reason` |
| `nvsnap_webhook_requests_total` | counter | `code` |
| `nvsnap_webhook_request_seconds` | histogram | |
| `nvsnap_build_info` | gauge | `version` |

Labels:

- `method`: `criu` (restored from a CRIU capture), `cachedir` (seeded from a
  cachedir capture, the fallback), `cold`.
- `gpu`: the node's GPU product (`nvidia.com/gpu.product`).
- `ranks`: the instance's GPU ranks, bucketed `1`, `2-4`, `5-8`, `9-16`,
  `17+`; companions (etcd, NATS) are not ranks.
- `reason`: the class of a failure or refusal: `mptcp`, `rdma`, `gpushare`,
  `fetch`, `timeout`, `criu`, `other`.
- `source`: where a checkpoint fetched to a node came from: `rails`, `peer`,
  `fsstore`, `volume` (`unknown` for a fetch that found no source). A
  checkpoint already on the node is no fetch.

What is measured:

- **Pod start** (`pod_ready_seconds`): a GPU pod's creation to Ready, once per
  pod, by the agent of its node. Only pods the webhook admitted count, and
  only those that became Ready while the agent watched (an agent restart
  does not count running pods again).
- **Restore phases**: per rank, on its node: `criu`, `gpu` (GPU state reload
  and resume), `rank` (the node's whole restore). Per instance: `group`
  (placeholders running to every rank restored) and `ready` (restored to the
  pod Ready).
- **Restore outcomes**: `restored`, `failed` (ran and failed), `not-run`
  (could not run, for example the checkpoint could not be fetched; its pods
  are replaced to try again).
- **Capture phases**: per rank: `dump` (freeze, CRIU, GPU state, snapshot),
  `snapshot` (replay mounts and the rootfs diff, while CRIU holds the tree
  stopped), `rank`. Per instance: `group` (the dump of every member) and
  `verify` (capture start to its restored pods serving). Cachedir: `rank`.
- **Capture outcomes**: `captured` (its restore verifies it), `verified`,
  `unverified` (its restored pods never served), `failed`, `refused` (the
  preflight refuses the configuration), `gpushare-failed` (its pods never
  started under the gpushare library).
- **Capture size** by `part`: `gpu` (GPU state), `rootfs` (rootfs diff),
  `memory` (the rest: process images), `cachedir`.
- **Admissions**: the webhook's decision for a GPU rank of a captured
  configuration: `restore`, `fallback` (reason: the configuration's state,
  `failed`, `restore-failed`, `gpushare-failed`) or `cold` (reason:
  `size-mismatch`, `not-restorable-as-group`, `checkpoint-blocked`,
  `own-instance`, `prepare-failed`, `model-unavailable`). A pod of a
  configuration with no capture is not counted. The agents add `missed`:
  a rank of an instance the webhook never mutated (reason `not-admitted`:
  the API server admitted it unchanged, as its failure policy lets it when
  the webhook cannot be reached; `opted-out`: the pod carries
  `nvsnap.io/inject=false`). Such an instance cannot be captured while that
  rank runs; it is captured once the rank's replacement is admitted.
- **Webhook requests**: what each agent's webhook answered, by HTTP status,
  and its time. Compared with the API server's own
  `apiserver_admission_webhook_request_total` and
  `apiserver_admission_webhook_fail_open_count` for `rootfs-restore.nvsnap.io`,
  a gap is calls that never reached an agent.
- **Agent bundle** (`build_info`): 1 for the bundle each agent serves; join
  on it, or compare time ranges around a rollout.

Example queries:

```promql
# p95 pod start by method, last 7 days
histogram_quantile(0.95, sum by (le, method) (rate(nvsnap_pod_ready_seconds_bucket[7d])))

# where a CRIU restore's time goes, p50, for 8-rank instances
histogram_quantile(0.5, sum by (le, phase) (rate(nvsnap_restore_phase_seconds_bucket{method="criu", ranks="5-8"}[1d])))

# why captures did not happen
sum by (outcome, reason) (increase(nvsnap_capture_outcomes_total{outcome=~"failed|refused|gpushare-failed|unverified"}[7d]))
```

## Summary lines

Each agent logs one line at the end of each capture, restore and pod start:
`msg="nvsnap capture summary"`, `"nvsnap restore summary"`,
`"nvsnap pod ready summary"`. Fields: `pod` or `criuGroup`, `method`,
`outcome`, `reason`, `error`, `ranks`, `gpu`, `seconds` and, where they
apply, `bytes`, `restoredToReadySeconds`, `fallbackReason`, `bundle`. Search
the log store by namespace or pod for one function's history.

## Events and the group record

- Events on the pods: `NvsnapCaptured`, `NvsnapCaptureFailed`,
  `NvsnapCaptureRefused` (on the captured instance's first rank),
  `NvsnapRestored`, `NvsnapRestoreFailed` (on the restore's first rank),
  `NvsnapStarted` (on each GPU pod, with its time to Ready and how it
  started), `NvsnapNotAdmitted` (on a rank the webhook never mutated, once).
- A capture's group record (`nvsnap-criu-group-<key>` ConfigMap) keeps its
  last 10 restores under `restores`, newest last, as `RFC3339=seconds`.

## Install

The agent serves `/metrics` on its API port (8081, `http-api`), without
authentication. To have the cluster's Prometheus keep it:

```yaml
agent:
  metrics:
    serviceMonitor:
      enabled: true
      namespace: ""     # one whose monitors the cluster's Prometheus reads ("" is the release's)
      labels: {}        # what the cluster's Prometheus selects monitors by
      interval: 30s
    dashboard:
      enabled: true     # a Grafana dashboard ConfigMap (grafana_dashboard: "1")
      labels: {}
```

The ServiceMonitor needs the Prometheus Operator CRDs. The dashboard is
`deploy/helm/nvsnap/files/grafana-dashboard-nvsnap-history.json`; import
it by hand into a Grafana without the dashboard sidecar.
