# nvstor KV tier for vLLM

**Experimental.** A vLLM offloading spec (`nvstor_kv.spec.NvstorSpec`)
whose storage medium is an nvstor KV server (`nvstor-kv serve`, crate
`crates/nvstor-kv`). vLLM offloads KV-cache blocks to the server and
loads them back over RDMA, straight into GPU memory. The spec is loaded
by vLLM's `OffloadingConnector`; vLLM itself is not patched.

## What it does

Per GPU worker, for each KV-cache group:

- **Load**: blocks are fetched by RDMA into free slots of a landing pool
  in GPU memory (through `libnvstor_kv.so`), then one batched
  device-to-device copy (vLLM's `swap_blocks_batch`) moves them into
  vLLM's KV pages on the worker's own CUDA stream.
- **Store**: one batched copy moves the pages into landing slots and a
  pinned host staging row; a background thread then computes each
  block's check over the staging row and PUTs the blocks (the server
  reads them from the landing slots by RDMA).
- **Lookup** (scheduler side): a key seen for the first time answers
  RETRY while a batched HEAD resolves it in the background; afterwards
  the cached answer is used and refreshed once it is older than its TTL.
  Stored blocks are announced as REMOTE KV-cache events when vLLM has
  them enabled.

A load can fail in normal operation (a block evicted between lookup and
load); vLLM then recomputes those blocks.

**Namespaces.** Engines that set the same `nvstor_namespace` share
blocks, but only when their KV identity also matches (model, dtype, KV
groups, layout, parallelism, bytes per block): the namespace sent to the
server is a hash of both. Weight quantization and model revision are
not part of that identity; the launch's namespace must distinguish them.
Without `nvstor_namespace`, an engine shares blocks only with itself.

The package also contains `nvstor_kv.probe.NvstorProbeSpec`, a spec that
stores nothing and records, per GPU worker, the KV-cache layout vLLM
hands it and whether that memory can be registered for RDMA (one JSON
report per worker in `report_dir`).

## Requirements

- vLLM with failed-load support in the offloading connector (main after
  v0.32.0).
- `--prefix-caching-hash-algo sha256`. Keys are sha256 block hashes plus
  the KV-cache group; with any other hash algorithm every block is a
  miss and nothing is stored (an error is logged).
- `"kv_load_failure_policy": "recompute"` in `--kv-transfer-config`.
  Any other value is refused at startup (when vLLM's config is not
  visible to the spec, a warning says what it must be); vLLM's default,
  `"fail"`, would fail a request whenever a load fails.
- Loads stay asynchronous: `sync_load_budget_bytes` must be unset
  (vLLM stops the engine when a synchronous load fails).
- One GPU worker per engine (world size 1). Tensor- or
  pipeline-parallel engines are refused; scale with data parallelism.
- `libnvstor_kv.so` (the C ABI in `crates/nvstor-kv/src/ffi.rs`), on
  the loader path or named by `NVSTOR_KV_LIB`:

  ```bash
  cargo build --release -p nvstor-kv --lib   # target/release/libnvstor_kv.so
  ```

- `NVSTOR_KV_SECRET` (and optionally `NVSTOR_KV_ACCESS_KEY`, default
  `kv`) in the environment of both the server and vLLM; credentials are
  never passed as arguments.
- RDMA NICs usable from the vLLM container and GPU memory that can be
  registered for RDMA (dma-buf). `NvstorProbeSpec` checks the latter.
- Python packages: `numpy`, plus `torch` and vLLM at run time.

## Running

Build and start the server (each selected NIC is a rail, rail *i* on
port + *i*):

```bash
cargo build --release -p nvstor-kv --bin nvstor-kv
export NVSTOR_KV_SECRET=<16+ characters>
nvstor-kv serve --port 9900 --devices netns
```

Launch vLLM with this package on `PYTHONPATH`:

```bash
export NVSTOR_KV_SECRET=<same secret>
export PYTHONPATH=/path/to/integrations/vllm:$PYTHONPATH
vllm serve <model> \
  --prefix-caching-hash-algo sha256 \
  --kv-transfer-config '{"kv_connector": "OffloadingConnector",
    "kv_role": "kv_both",
    "kv_load_failure_policy": "recompute",
    "kv_connector_extra_config": {
      "spec_name": "NvstorSpec",
      "spec_module_path": "nvstor_kv.spec",
      "nvstor_host": "<server address>",
      "nvstor_port": 9900,
      "nvstor_rails": <number of server rails>,
      "nvstor_namespace": "<32 hex digits>"}}'
```

### Settings (`kv_connector_extra_config`)

| Key | Default | Meaning |
| --- | ------- | ------- |
| `nvstor_host`, `nvstor_port`, `nvstor_rails` | required | Server address, port of rail 0, number of server rails. |
| `nvstor_namespace` | engine id | 32 hex digits naming the set of engines that share blocks. |
| `nvstor_devices` | `netns` | Local RDMA devices: `netns` (those of this network namespace), `all`, or a comma-separated list of device names. |
| `nvstor_connections` | 8 | Connections per KV-cache group for loads and stores. |
| `nvstor_lookup_connections` | 4 | Connections for scheduler-side lookups. |
| `nvstor_landing_slots` | 256 | Landing slots per group in GPU memory. |
| `nvstor_part_keys` | 64 | Most keys per transfer; larger jobs run in parts. |
| `nvstor_budget_ms` | 1000 | Time budget of each GET/PUT request, in milliseconds. |
| `nvstor_hit_ttl_s`, `nvstor_miss_ttl_s` | 5.0, 0.5 | How long a cached lookup answer is used before it is refreshed. |
| `nvstor_max_cached_keys` | 1000000 | Bound on cached lookup answers. |
| `nvstor_verify_loads` | false | Re-check every loaded block against the check the server returned. |

## Tests

Neither test needs a GPU, a NIC or vLLM.

```bash
# NvstorSpec against stub vLLM types, a simulated address space and a
# fake server (needs numpy)
python3 -I integrations/vllm/tests/test_spec.py integrations/vllm

# The ctypes binding against a built library
cargo build -p nvstor-kv --lib
python3 -I integrations/vllm/tests/test_client_binding.py integrations/vllm target/debug/libnvstor_kv.so
```
