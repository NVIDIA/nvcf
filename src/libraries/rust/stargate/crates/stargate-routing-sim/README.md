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
- Engine: the MockDynamo timing model. Each request has a slot, a FIFO wait
  queue, independent per-request prefill at a fixed rate, per-key LRU KV
  cache, and a sampled decode rate.
- Network: per-region round-trip times between Stargates and backends.
- Clients: open-loop Poisson arrivals over sessions, a client timeout that
  cancels engine work, and a TTFT SLO for goodput.

## Usage

```sh
cargo run --release -p stargate-routing-sim -- \
  --config crates/stargate-routing-sim/configs/stargate-dev-parity.json \
  --output /tmp/routing-sim.json
```

Each config runs every combination of seed, rate, and policy. All policies see
the same arrival sequence for a seed. Policy `load_balancer` objects use the
same JSON shape as Stargate per-model load-balancer config.

## Fidelity limits

- Engine prefill is independent per request, as in MockDynamo. Real engines
  share prefill compute, so cache hits matter more on real hardware.
- Load balancers use an unseeded thread RNG internally, so runs with the same
  seed are not bit-for-bit reproducible. Use several seeds.
- One backend per routed cluster. Shared-engine cluster aggregation and
  sibling-backend retries are not modeled.
- `max_input_work_seconds` admission, priorities, and registration churn are
  not modeled.
- Results are relative comparisons until the engine model is calibrated
  against real-engine measurements.
