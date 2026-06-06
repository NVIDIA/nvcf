<coding_guidelines>
# crates/pylon-lib

`pylon-lib` owns pylon backend sidecar behavior. A pylon is the former `stargate-client`: it manages registration streams, reverse tunnels, local upstream forwarding, request observation, metrics, bringup calibration, and active canaries.

## Local Invariants

- The QUIC tunnel requires `x-request-id`, `x-model`, and `x-input-tokens`; missing required headers return HTTP `400` and must not be forwarded upstream.
- Treat `x-request-id` as the canonical globally unique request identity. Do not synthesize replacement local request IDs.
- `x-stargate-retryable`, `x-stargate-retry-reason`, and `x-stargate-retry-after-ms` are response metadata emitted by the pylon, not caller request headers.
- `x-stargate-expected-queue-ms` is internal request metadata emitted by Stargate only. Strip it before forwarding upstream and never trust a caller-supplied value.
- Queue mismatch retry responses are local pylon admission decisions and must use pylon-emitted retry response metadata.
- Request observation assumes streamed responses. Derive output progress from SSE `data:` events, not non-streaming JSON bodies.
- Terminal request-observer transitions are invariants. Calling terminalization from a terminal state should fail loudly.
- Model advertisement is gated by both caller-provided status and bringup lifecycle state.
- Reverse-tunnel connectivity gates advertisement per stargate even when local model calibration has completed.

## Bringup And Canaries

- Bringup calibration runs against the local upstream HTTP server, not the advertised `inference_server_url`.
- When the advertised URL is `quic://...`, callers must also supply the direct local HTTP base URL.
- Calibration runs only after a Stargate assigns this pylon as that Stargate's local cluster calibration owner and the local upstream health check succeeds. The assigned pylon submits its result only to the assigning Stargate through the owner-only RPC; completion never populates backend runtime `last_mean_input_tps` or local admission state. Each Stargate coordinates its local state independently, and calibration or submission work must not block registration heartbeats.
- Active canaries begin only after active advertisement.
- The built-in canary is the deterministic `1+1=` chat request. Completion exactly at `canary_max_generation_threshold` is treated as runaway generation and demotes the model.

## Metrics

- Keep pylon metrics in `src/metrics.rs` using the per-process `PylonMetrics` registry.
- Model gauges should be emitted for request-observation updates and KV-cache poll-only updates.
- Preserve terminal request counters and token histograms for all terminal outcomes.
</coding_guidelines>
