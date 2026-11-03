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

## Runtime identity

The image declares OCI user `1000:1000`. NVCA's generated pod and container
security contexts determine the effective init-container identity and volume
group. A custom `INIT_CONTAINER` image must support the configured identity and
writable paths.

The transport trust bundle installer is packaged in the same image. It writes
the merged CA bundle to an ephemeral volume before worker-init starts. NVCA's
container security context takes precedence over the image default. In
particular, `runAsUser: 0` with `runAsNonRoot: false` runs the installer as root.
