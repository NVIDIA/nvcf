# worker-utils

NVCF worker service that runs alongside the user's inference container. It polls
NVCF for invocation requests, forwards and streams them to the user container,
streams the responses back, reports progress, and handles request cancellation
and spot-instance termination.

## Build

The binary is built with Bazel:

```bash
bazel build //src/compute-plane-services/worker-utils/cmd:cmd
```

## Test

```bash
# Run unit tests via Bazel
bazel test //src/compute-plane-services/worker-utils/...

# Or with the Go toolchain, from this directory
go test ./...
```
