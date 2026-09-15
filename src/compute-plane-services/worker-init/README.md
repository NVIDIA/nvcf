# worker-init

Init component that prepares the NVCF worker environment before the task
container starts. It downloads the model and resource artifacts from NGC,
bootstraps the ESS (secret) agent configuration, and emits initialization
metrics.

## Build

The binary and container image are built with Bazel:

```bash
# Build the worker-init binary
bazel build //src/compute-plane-services/worker-init/cmd:worker-init

# Build the container image
bazel build //src/compute-plane-services/worker-init/cmd:image
```

## Test

```bash
# Run unit tests via Bazel
bazel test //src/compute-plane-services/worker-init/...

# Or with the Go toolchain, from this directory
go test ./...
```
