# Stargate local validation, 2026-10-14

Baseline: `a8abe14f361fccf485f457929dffdd51f8a44f6d` (merge-base).

Candidate: `eb04cd97744315b4c20990f6c1217501d37406b3`.

Functional validation and repeated revision comparisons are complete. All
measured requests succeeded. No consistent revision-specific latency regression
was established; these measurements do not prove equivalence. Individual tail
results vary, and the auth service has a measured memory cost documented below.
That intentional resource tradeoff requires approval before merge.

## Controls and scope

- Linux amd64 workstation, 24 logical CPUs and 249 GiB RAM. No pre-existing
  Docker containers or k3d clusters were present. No AWS resources were changed.
- Six isolated single-node k3d clusters: two hubs and four MockDCs. Each hub
  has three main routers, three backend routers, and one auth service. Each
  MockDC has two Ready backend Pods on its single node. The eight backends
  retain the local environments' heterogeneous one/two-worker capacities.
- Both revisions use Bazel optimized images, the same frozen candidate chart
  and controller, resource requests/limits, policy, mock settings, Spark 0.3.8
  image, deterministic generated prompts, and cold-cache reset procedure.
  No overlapping campaigns or image builds ran during measurement.
  PowerOfN uses unseeded backend draws; fixed prompts do not replay identical
  routing decisions. Individual results retain that observed variation.
- Revision order: baseline/candidate, candidate/baseline, baseline/candidate.
  Each revision repetition runs both workloads separately under wait-and-widen
  and power-of-n: 12 accepted campaigns and 24 workload measurements.
- Sustained load: 5,760 requests, 48 RPS, 96 workers, 2,048-byte prompts.
  Affinity: 128 sessions x 12 turns, 24 RPS, 48 workers, 3,584-byte stable
  prefixes and 512-byte turns. Both request 256 output tokens, use 30-second
  timeouts and 10-second request/wait bounds, and drain every admission.
- The policy is queue-bounds none with maximum queued requests 4. The model
  identity is changed only to match the isolated fixture. Each arm verifies
  membership, policy, Pod identities, readiness, and empty caches after reset.
- The baseline predates this development stack. Its runtime sources are
  unchanged; candidate deployment/controller/chart files and OCI packaging
  targets supply the harness. The original worker-auth fixture uses its
  original command flags and TCP probes in place of the new HTTP health
  endpoints. Both revisions use the same disposable test credentials.
  [Source overlays](source-overlays.json) record every test-only adapter.
- Router, Pylon, and MockDynamo OCI digests are identical across revisions.
  Auth is the changed runtime image. This comparison measures the new auth
  service within an identical topology; it does not compare one-node and
  two-node topology capacity. The single-node scheduling change is validated
  by the functional placement and traffic checks below.

## Inference results

Values below are medians of three repetitions. Positive latency changes are
worse; positive throughput changes are better. Spark latency quantiles are
histogram bucket lower bounds. Ranges and paired changes are retained in
[comparison.json](comparison.json); they are observed variation, not confidence
intervals. Offered load is fixed, so these results do not establish maximum
capacity or production performance.

| Workload / policy | RPS baseline -> candidate | TTFT p95 ms | TTFT p99 ms | Latency p95 ms | Latency p99 ms |
| --- | --- | --- | --- | --- | --- |
| session-affinity / power-of-n | 23.400 -> 23.510 (+0.47%) | 141.312 -> 137.216 (-2.90%) | 184.320 -> 165.888 (-10.00%) | 1720.320 -> 1654.784 (-3.81%) | 2080.768 -> 1933.312 (-7.09%) |
| session-affinity / wait-and-widen | 23.608 -> 23.608 (-0.00%) | 93.184 -> 95.232 (+2.20%) | 116.736 -> 119.808 (+2.63%) | 1228.800 -> 1228.800 (+0.00%) | 1277.952 -> 1261.568 (-1.28%) |
| sustained-capacity / power-of-n | 47.313 -> 47.253 (-0.13%) | 76.800 -> 76.800 (+0.00%) | 84.992 -> 83.968 (-1.20%) | 1703.936 -> 1638.400 (-3.85%) | 1949.696 -> 1916.928 (-1.68%) |
| sustained-capacity / wait-and-widen | 47.562 -> 47.553 (-0.02%) | 75.776 -> 75.776 (+0.00%) | 78.848 -> 77.824 (-1.30%) | 1327.104 -> 1327.104 (+0.00%) | 1359.872 -> 1359.872 (+0.00%) |

### Individual repetitions

| Workload / policy | Revision / repetition | Successes | RPS | TTFT p95 / p99 ms | Latency p95 / p99 ms | Cache hit % |
| --- | --- | --- | --- | --- | --- | --- |
| session-affinity / power-of-n | baseline / 1 | 1536 | 23.400 | 147.456 / 184.320 | 1720.320 / 2080.768 | 50.46 |
| session-affinity / power-of-n | candidate / 1 | 1536 | 23.452 | 137.216 / 163.840 | 1622.016 / 1835.008 | 52.41 |
| session-affinity / power-of-n | baseline / 2 | 1536 | 23.093 | 141.312 / 188.416 | 1736.704 / 2097.152 | 52.41 |
| session-affinity / power-of-n | candidate / 2 | 1536 | 23.510 | 141.312 / 165.888 | 1654.784 / 1933.312 | 52.67 |
| session-affinity / power-of-n | baseline / 3 | 1536 | 23.612 | 139.264 / 167.936 | 1490.944 / 1671.168 | 52.28 |
| session-affinity / power-of-n | candidate / 3 | 1536 | 23.593 | 137.216 / 167.936 | 1703.936 / 2080.768 | 51.95 |
| session-affinity / wait-and-widen | baseline / 1 | 1536 | 23.603 | 92.160 / 115.712 | 1228.800 / 1277.952 | 83.92 |
| session-affinity / wait-and-widen | candidate / 1 | 1536 | 23.620 | 97.280 / 123.904 | 1228.800 / 1261.568 | 83.72 |
| session-affinity / wait-and-widen | baseline / 2 | 1536 | 23.608 | 94.208 / 116.736 | 1228.800 / 1294.336 | 84.11 |
| session-affinity / wait-and-widen | candidate / 2 | 1536 | 23.608 | 93.184 / 119.808 | 1228.800 / 1261.568 | 84.18 |
| session-affinity / wait-and-widen | baseline / 3 | 1536 | 23.610 | 93.184 / 124.928 | 1228.800 / 1277.952 | 84.11 |
| session-affinity / wait-and-widen | candidate / 3 | 1536 | 23.603 | 95.232 / 112.640 | 1228.800 / 1277.952 | 84.05 |
| sustained-capacity / power-of-n | baseline / 1 | 5760 | 47.171 | 76.800 / 86.016 | 1654.784 / 1949.696 | 0.00 |
| sustained-capacity / power-of-n | candidate / 1 | 5760 | 47.253 | 76.800 / 83.968 | 1622.016 / 1818.624 | 0.00 |
| sustained-capacity / power-of-n | baseline / 2 | 5760 | 47.313 | 76.800 / 83.968 | 1703.936 / 1900.544 | 0.00 |
| sustained-capacity / power-of-n | candidate / 2 | 5760 | 47.196 | 76.800 / 84.992 | 1671.168 / 1916.928 | 0.00 |
| sustained-capacity / power-of-n | baseline / 3 | 5760 | 47.349 | 76.800 / 84.992 | 1703.936 / 1949.696 | 0.00 |
| sustained-capacity / power-of-n | candidate / 3 | 5760 | 47.255 | 76.800 / 83.968 | 1638.400 / 1933.312 | 0.00 |
| sustained-capacity / wait-and-widen | baseline / 1 | 5760 | 47.564 | 75.776 / 78.848 | 1343.488 / 1359.872 | 0.00 |
| sustained-capacity / wait-and-widen | candidate / 1 | 5760 | 47.553 | 75.776 / 78.848 | 1327.104 / 1359.872 | 0.00 |
| sustained-capacity / wait-and-widen | baseline / 2 | 5760 | 47.562 | 75.776 / 78.848 | 1327.104 / 1359.872 | 0.00 |
| sustained-capacity / wait-and-widen | candidate / 2 | 5760 | 47.565 | 75.776 / 77.824 | 1327.104 / 1359.872 | 0.00 |
| sustained-capacity / wait-and-widen | baseline / 3 | 5760 | 47.552 | 75.776 / 80.896 | 1327.104 / 1359.872 | 0.00 |
| sustained-capacity / wait-and-widen | candidate / 3 | 5760 | 47.547 | 75.776 / 77.824 | 1327.104 / 1359.872 | 0.00 |

All 87,552 measured inference requests in the primary comparison completed
successfully. Failures, timeouts, retries, and cancellations were zero.
Fixed-count runs reconcile
requested counts with successes plus failures, so window-end in-flight
requests are not omitted. Raw Spark numerical reports are retained in
[raw-reports.json](raw-reports.json); private client configuration and
generated prompt copies are omitted.

WaW affinity's primary median TTFT increases by 2.048 ms at p95 and 3.072 ms
at p99. Paired p99 differences are +8.192, +3.072, and -12.288 ms. The separate
reversed-order follow-up instead lowers candidate p95/p99 by 1.024/7.168 ms.
The initial increase does not repeat consistently.

PowerOfN affinity is also variable. Its primary end-to-end p99 differences are
-245.760, -163.840, and +409.600 ms. The focused follow-up is worse by 147.456 ms.
The improved primary median does not erase those worse individual results.
Unseeded backend choices provide a source of variation, but aggregate cache-hit
counts do not prove the cause of a particular tail result.

The investigation checked matching prompt hashes, deployment controls, and
calibration matrices. Per-backend input TPS varies by less than 0.2% across the
primary WaW affinity runs, so calibration alone does not explain the early tail
differences. Auth runs at registration/tunnel setup, rather than per inference
request; router/Pylon/MockDynamo images are identical across revisions.

The [proxy-log observations](runtime-observations.json) use the actual
stargate-k8s-router messages. Six persistent backend-router logs cover all
28 measured windows. They contain 3,596 registration-forward and 2,684 QUIC-relay
events overall, confirming the selected messages are observable. No such new
connections or selected proxy errors appear inside measured windows. There are
92 registration-stream errors outside those windows. Historical main-router
auth/NACK logs were not retained through Pod resets, so this is proxy evidence,
not a direct count of every auth RPC or error.

## Authentication results

The identical on-wire gRPC probe validates missing-service-auth and unknown
worker rejection, warms up with 500 valid RPCs, then sends 20,000 valid RPCs
at 1,000 RPS with at most 64 in flight and a two-second timeout. It checks
the returned routing identity and drains every request. Both server versions
are reached through the same loopback kubectl port-forward method. Reported
latency includes that client/network path and is not server CPU time.

| Metric | Baseline -> candidate median |
| --- | --- |
| successfulRps | 999.978 -> 999.981 (+0.00%) |
| p95Ms | 1.631 -> 1.626 (-0.28%) |
| p99Ms | 1.964 -> 1.967 (+0.15%) |
| meanMs | 0.802 -> 0.797 (-0.62%) |

| Revision / repetition | Successes | RPS | p95 ms | p99 ms | Mean ms |
| --- | --- | --- | --- | --- | --- |
| baseline / 1 | 20000 | 999.972 | 1.662 | 2.023 | 0.815 |
| candidate / 1 | 20000 | 999.650 | 1.632 | 1.967 | 0.802 |
| baseline / 2 | 20000 | 999.981 | 1.617 | 1.947 | 0.779 |
| candidate / 2 | 20000 | 1000.024 | 1.573 | 1.930 | 0.765 |
| baseline / 3 | 20000 | 999.978 | 1.631 | 1.964 | 0.802 |
| candidate / 3 | 20000 | 999.981 | 1.626 | 1.974 | 0.797 |

All 120,000 measured auth RPCs in the primary comparison succeeded, and every
negative-auth check passed. [auth-reports.json](auth-reports.json) preserves
every measured latency, including follow-up preparation probes.

Auth p99's primary median increases by 0.003007 ms, with paired changes of
-2.758%, -0.844%, and +0.505%. Mean and p95 decrease. These results do not show
every latency metric improving.

## Resources

Kubelet CPU and working-set memory were sampled every five seconds and
summed across containers of each role. The table uses the median of the
six campaign medians per revision. Inference windows are approximated
from raw-report completion time and duration; auth probe samples include
warmup and kubelet reporting lag. Individual sample values, ranges, counts,
and phases are retained in comparison.json. These coarse samples cannot
resolve a sub-millicore CPU difference.

| Role | CPU cores baseline -> candidate | Working set MiB baseline -> candidate |
| --- | --- | --- |
| auth | 0.0001 -> 0.0001 | 1.8184 -> 2.4336 |
| backend-router | 0.6501 -> 0.6428 | 38.0430 -> 37.4414 |
| helm-nvcf-llm-request-router | 0.5003 -> 0.4966 | 26.4189 -> 26.5088 |
| mock-dynamo | 0.1156 -> 0.1151 | 18.5146 -> 18.4199 |
| pylon | 0.9057 -> 0.8724 | 56.2500 -> 56.2158 |
| spark | 0.1681 -> 0.1646 | 117.2129 -> 114.5977 |

Auth working set increases by 0.6152 MiB across the two Pods, about 0.3076 MiB
per Pod on average. The candidate adds an HTTP health/metrics service absent
from the legacy fixture. This is a measured resource cost, not zero overhead.
The unchanged 64 MiB request and 256 MiB limit per Pod provide allocation
context. The [performance gate](../../../../../src/libraries/rust/stargate/AGENTS.md#performance-regression-gate)
requires explicit approval of this intentional tradeoff before merge.

Other roles do not show a comparable consistent candidate-specific resource
increase. Persistent backend-router and Spark memory changes over chronological
runs limit attribution of their footprint to a revision. The retained samples
support these comparisons, not a precise peak-memory or per-RPC CPU claim.

## Functional validation

- All six clusters have exactly one node. Both backend Pods in every
  MockDC are Ready on that same node with the candidate chart and images.
- Both hubs discover all eight backends. Streaming probes complete 192/192
  requests across both hubs and both policies, validate SSE content and
  the final `[DONE]`, and observe every backend through each hub.
- The current controller smoke completes 32/32 requests per policy, with
  zero failures/retries; acceptance and offline rendering pass.
- Scoped native Bazel Pylon and Stargate suites pass. Stack Python tests
  pass all 50 cases, and the full shared-chart test target passes.
- Existing interrupt/reconcile/resume and AWS validation is recorded in
  the Pull Request at its earlier tested revision; it is not presented
  as a new AWS run for this candidate.

Scoped commands run from the repository root:

```sh
bazel test \
  //src/libraries/rust/stargate/crates/pylon-lib:pylon-lib_test \
  //src/libraries/rust/stargate/crates/stargate:stargate_test --test_output=errors
python3 -m unittest discover -s deploy/stacks/stargate-dev/tests
make -C deploy/helm/llm-request-router test
```

## Reproduction and artifacts

Use the local setup in the [stack guide](../../README.md), apply the
test-only isolation/packaging adapters, and set `build --compilation_mode=opt`
in each revision's untracked `user.bazelrc`. Build and publish its images with
`python3 deploy/stacks/stargate-dev/scripts/local.py images`.
Keep the candidate controller and
Spark digest frozen. After each revision deployment, verify both hubs,
then run each policy exclusively from the benchmark directory:

```sh
stargate-dev-bench --suite-file suite.yaml run --suite performance \
  --algorithm wait-and-widen --region local-a --peer-region local-b \
  --spark-pod <isolated-client-pod> --expected-config policy.json \
  --output <unique-campaign-directory>
stargate-dev-bench render --output <unique-campaign-directory>
```

Repeat with `--algorithm power-of-n` and the documented revision order.
Use the retained [suite](suite.yaml), [policy](policy.json),
[image/resource controls](controls.json), [integration receipt](integration.json),
and [source/build provenance](provenance.json). Source archive, controller,
workload, policy, Spark image, and auth probe hashes are recorded.

Full private campaign directories retain the original Spark reports,
acceptance receipts, launch exit/ownership proofs, Pod/cache controls,
resource samples, and logs. Portable numerical reports above omit private
endpoints, credentials, and host paths. Scheduled rate ramps, full canonical
suites, real engines, AWS observability, and production capacity remain
outside this validation.

## Focused WaW affinity follow-up

One additional candidate/baseline pair was fixed in advance after the
observed affinity tail variation. It runs the unchanged 1,536-request
session-affinity recipe without the preceding sustained-capacity arm.
The same controller, Spark image, policy, resources, cache reset,
calibration, auth warmup/probe, and acceptance checks are retained.
These measurements are separate from the three primary repetitions;
both results are retained regardless of direction.

| Revision | Successes | RPS | TTFT p95 / p99 ms | Latency p95 / p99 ms | Cache hit % |
| --- | --- | --- | --- | --- | --- |
| candidate | 1536 | 23.608 | 90.112 / 110.592 | 1228.800 / 1277.952 | 84.05 |
| baseline | 1536 | 23.617 | 91.136 / 117.760 | 1228.800 / 1277.952 | 83.98 |

Both follow-up arms complete all requests with zero failures or retries.
Two additional 20,000-RPC auth probes preserve the original preparation
and pass all valid/invalid checks. The [WaW follow-up reports](affinity-followup.json)
and [affinity-only suite](affinity-suite.yaml) retain their inputs/results.

This pair does not reproduce the early WaW TTFT increase. End-to-end p95/p99
are equal. It supplies additional evidence about variation and is not pooled
into the three primary repetition medians.

## Focused PowerOfN affinity follow-up

One additional candidate/baseline pair was fixed in advance after the
observed affinity tail variation. It runs the unchanged 1,536-request
session-affinity recipe without the preceding sustained-capacity arm.
The same controller, Spark image, policy, resources, cache reset,
calibration, auth warmup/probe, and acceptance checks are retained.
These measurements are separate from the three primary repetitions;
both results are retained regardless of direction.

| Revision | Successes | RPS | TTFT p95 / p99 ms | Latency p95 / p99 ms | Cache hit % |
| --- | --- | --- | --- | --- | --- |
| candidate | 1536 | 23.357 | 145.408 / 204.800 | 1687.552 / 1966.080 | 52.47 |
| baseline | 1536 | 23.333 | 139.264 / 167.936 | 1556.480 / 1818.624 | 52.73 |

Both follow-up arms complete all requests with zero failures or retries.
Two additional 20,000-RPC auth probes preserve the original preparation
and pass all valid/invalid checks. The [PowerOfN follow-up reports](powerofn-followup.json)
and [affinity-only suite](affinity-suite.yaml) retain their inputs/results.

Candidate TTFT p99 is worse by 36.864 ms (+21.95%), and end-to-end p99 by
147.456 ms (+8.11%). This result is retained alongside the primary repetitions
with changes in both directions. It does not establish a general algorithm
advantage, a causal auth regression, or production capacity. No favorable-only
selection or statistical-equivalence claim is made.
