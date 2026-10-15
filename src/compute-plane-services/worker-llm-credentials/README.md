# worker-llm-credentials

Sidecar that maintains a fresh NVCF worker credential token on disk for the
LLM inference container. It connects to NVCF over gRPC using the worker token,
runs a background refresher that periodically fetches a new token, and writes
it atomically to the configured token path (owner-only `0600` permissions). The
process runs until its context is cancelled.

## Build

The binary is built with Bazel:

```bash
bazel build //src/compute-plane-services/worker-llm-credentials/cmd:cmd
```

## Test

```bash
# Run unit tests via Bazel
bazel test //src/compute-plane-services/worker-llm-credentials/...

# Or with the Go toolchain, from this directory
go test ./...
```
