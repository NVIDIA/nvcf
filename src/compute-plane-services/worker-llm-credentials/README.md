# worker-llm-credentials

Sidecar that maintains fresh NVCF credentials on disk for explicit LLM
functions. It connects to NVCF over gRPC using the worker token and always
refreshes the worker credential used by the LLM router client. When
`ESS_ASSERTION_TOKEN_PATH` is set, it also refreshes the ESS assertion used by
the ESS Agent. The process runs until its context is cancelled.

Each refresher fetches its credential immediately, schedules later refreshes
from the returned expiration, and atomically replaces the configured file. The
shared files use `0644` permissions because the consuming infrastructure
sidecars run as different non-root users. Customer workload containers do not
mount these credential volumes.

## Configuration

- `WORKER_TOKEN_PATH` sets the worker credential file. It defaults to
  `/var/run/llm/worker-token`.
- `ESS_ASSERTION_TOKEN_PATH` optionally sets the ESS assertion file. When it is
  unset, the sidecar does not request or write an ESS assertion.

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
