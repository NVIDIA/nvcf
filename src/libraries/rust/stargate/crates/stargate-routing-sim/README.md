# stargate-routing-sim

Discrete-event simulator for comparing Stargate load-balancer policies and
configurations. It calls the production `LoadBalancer` implementations from the
`stargate` crate in virtual time. It models the surrounding system:

- Stargate: one load-balancer instance per Stargate, stale per-backend stats,
  routing reservations, the routing wait and retry loop, and queue-mismatch
  reroutes. Reservations use Stargate's own settlement code. Each one counts
  from routing, and its soft TTL is the Stargate-to-backend RTT, bounded by
  `stargate.routing_reservation_ttl_min_ms` (default `1`) and
  `stargate.routing_reservation_ttl_max_ms` (default `1000`). It ends when
  that Stargate receives an update from the reserved backend at or after the
  soft expiry, or when a queue-mismatch rejection releases it.
- Session-aware routing: each growing-session turn carries the cluster that
  served the session's last successful turn as its last-cluster hint, as the
  gateway sends `x-stargate-last-cluster-id`. First turns carry none. Policies
  with `last_cluster_affinity` classify each request once and keep that
  classification across its routing waits and reroutes.
- Pylon: live request phases, queue estimates, queue-mismatch admission, and
  Pylon's own OpenAI fallback window from `pylon-lib`, which sets
  `last_mean_input_tps` and `max_input_tps`. The model is a deployment where
  every chat request expects usage (`--force-chat-completions-include-usage`)
  and MockDynamo reports cached prompt tokens in it. A request enters the
  window at first output and waits for usage, which arrives at completion
  and counts uncached prompt tokens. A waiting request holds newer requests
  out of the rate, and cache hits that leave fewer than 256 uncached tokens
  are left out. A request cancelled after first output never reports usage,
  so it counts total prompt tokens. The maximum rises only from windows whose
  requests all reported usage. The `initial` value stands in for startup
  calibration and sets both values until the window replaces the mean.
- Engine: the `mock-engine` crate that MockDynamo also runs. Each backend is a
  Dynamo deployment with `num_gpu_workers` workers that batch prefill and
  decode in shared steps, keep their own KV caches, and receive requests by
  perfect KV routing.
- Network: per-region round-trip times between Stargates and backends.
- Clients: fixed sessions with open-loop Poisson arrivals, or growing
  conversations whose sessions start with Poisson arrivals and whose later
  turns follow the previous response after a think time. Failed growing turns
  retry with backoff. A client timeout cancels engine work, and a TTFT SLO
  defines goodput.

## Usage

Simulate every seed, rate, and policy in a config. Run from
`src/libraries/rust/stargate`:

```sh
cargo run --release -p stargate-routing-sim -- simulate \
  --config crates/stargate-routing-sim/configs/stargate-dev-parity.json \
  --output /tmp/routing-sim.json
```

All policies see the same arrival sequence for a seed. Policy `load_balancer`
objects use the same JSON shape as Stargate per-model load-balancer config.

## Driving a deployed fleet

`drive` sends the same workload to a deployed Stargate in real time, so a
fleet run and a simulation of the same config and seed see identical
sessions. Requests carry `x-input-tokens`, `x-output-tokens`, and
`x-cache-affinity-key` instead of prompt text; MockDynamo honors all three.
Run one driver per region with a shared start time and the same config,
seed, and fleet-wide `--rate-rps`. Each driver sends the requests planned for
its region's Stargates. Driver hosts need synchronized clocks, because the
start time is wall-clock Unix milliseconds:

```sh
START_MS=$(( ($(date +%s) + 60) * 1000 ))
stargate-routing-sim drive --config bench.json --region usw2 \
  --endpoint http://router:8000 --rate-rps 300 --seed 1 \
  --routing-method pulsar-wait-and-widen --run-label pulsar-300-s1 \
  --start-at-unix-ms "$START_MS" --records usw2.jsonl
```

Like the gateway, the driver sends each growing-session turn the
`x-stargate-cluster-id` of the session's last successful response as
`x-stargate-last-cluster-id`. A success is a 2xx response whose stream
reached `[DONE]`; failed attempts keep the earlier value.

Each in-flight request holds a connection, so raise the driver's open-file
limit (`ulimit -n`) for high rates. Running out of descriptors is recorded as
`driver-nofile`, not as a fleet failure. A request succeeds only when its
stream reaches `[DONE]`.

Then summarize all regions' records with the same measurement window and
TTFT SLO as the simulator. The optional `--backend-gpus` file is a JSON object
that maps backend cluster IDs to GPU worker counts, for example
`{"usw2-backend-0": 1}`:

```sh
stargate-routing-sim summarize-fleet --config bench.json \
  --records usw2.jsonl ue1.jsonl ew1.jsonl an1.jsonl as2.jsonl \
  --backend-gpus backend-gpus.json --output summary.json
```

Fleet and simulator summaries both report `hinted_requests` and
`hinted_on_last_cluster_fraction`, the share of successful hinted requests
served by their hinted cluster. Simulator summaries also count
`session_states` for policies with `last_cluster_affinity`.

## Fidelity limits

- Engine step costs are estimates until calibrated against a real engine.
- Load balancers use an unseeded thread RNG internally and read a small amount
  of real-clock jitter through the request receive time, so runs with the same
  seed are not bit-for-bit reproducible. Use several seeds.
- Offered requests and SLO attainment count every attempt, including
  growing-session retries. A retry's TTFT starts at the retry.
- Arrivals stop at the end of the measurement window. Requests that arrived in
  the window run to completion.
- A request succeeds when the engine completes it, even if the client timeout
  fires during the final one-way trip back to the Stargate.
- One backend per routed cluster. Shared-engine cluster aggregation and
  sibling-backend retries are not modeled.
- Fixed sessions send no last-cluster hint, so a policy with
  `last_cluster_affinity` classifies all their requests as `new`. Growing
  sessions remember a cluster only after a turn succeeds. The gateway contract
  stores the cluster when 2xx response headers arrive, so a turn that fails
  after its headers would update the gateway's hint but not the simulator's.
- Request heads leave Stargate as soon as a backend is selected, so a
  reservation's soft expiry starts at routing. Tunnel stream opening delays
  are not modeled.
- The fallback window sees each request's usage once, at completion, and every
  request's usage reports cached tokens. Pylon's `--min-input-tokens` is its
  default of `1`. Calibration traffic is not simulated.
- `max_input_work_seconds` admission, `consider_kv_free_tokens`, priorities,
  and registration churn are not modeled. Simulated Pylons publish no KV-cache
  stats.
- Without `pylon.stats_update_coalesce_ms`, Pylons publish only on the
  heartbeat. Production Pylons also publish on request state changes.
- Retries for upstream errors other than queue-mismatch rejections are not
  modeled.
- Results are relative comparisons until the engine model is calibrated
  against real-engine measurements.
