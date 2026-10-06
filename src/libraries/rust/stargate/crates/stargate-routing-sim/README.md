# stargate-routing-sim

Discrete-event simulator for comparing Stargate load-balancer policies and
configurations. It calls the production `LoadBalancer` implementations from the
`stargate` crate in virtual time. It models the surrounding system:

- Stargate: one load-balancer instance per Stargate, stale per-backend stats,
  optimistic reservations cleared by heartbeats, the routing wait and retry
  loop, and queue-mismatch reroutes.
- Pylon: live request phases, queue estimates, queue-mismatch admission, and
  the fallback input-throughput window that sets `last_mean_input_tps` and
  `max_input_tps`.
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

Run from `src/libraries/rust/stargate`:

```sh
cargo run --release -p stargate-routing-sim -- \
  --config crates/stargate-routing-sim/configs/stargate-dev-parity.json \
  --output /tmp/routing-sim.json
```

Each config runs every combination of seed, rate, and policy. All policies see
the same arrival sequence for a seed. Policy `load_balancer` objects use the
same JSON shape as Stargate per-model load-balancer config.

## Fidelity limits

- Engine step costs are estimates until calibrated against a real engine.
- Load balancers use an unseeded thread RNG internally, so runs with the same
  seed are not bit-for-bit reproducible. Use several seeds.
- One backend per routed cluster. Shared-engine cluster aggregation and
  sibling-backend retries are not modeled.
- `max_input_work_seconds` admission, `consider_kv_free_tokens`, priorities,
  and registration churn are not modeled. Simulated Pylons publish no KV-cache
  stats.
- Without `pylon.stats_update_coalesce_ms`, Pylons publish only on the
  heartbeat. Production Pylons also publish on request state changes.
- Retries for upstream errors other than queue-mismatch rejections are not
  modeled.
- Results are relative comparisons until the engine model is calibrated
  against real-engine measurements.
