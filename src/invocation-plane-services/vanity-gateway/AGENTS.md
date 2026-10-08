# Vanity gateway

This service is maintained natively in this repository. Follow root guidance.
Use the existing OpenTelemetry HTTP instrumentation and zap logging.

From this directory, run native tests and vet:

```sh
go test ./...
go test -race ./middleware ./gateway
go vet ./...
```

From the repository root, run scoped Bazel tests:

```sh
bazel test //src/invocation-plane-services/vanity-gateway/middleware:middleware_test //src/invocation-plane-services/vanity-gateway/gateway:gateway_test --test_output=streamed
```

Keep new Go files and test dependencies in the adjacent `BUILD.bazel` files.
Regression tests for proxy timing should use gated transports and readers.
Keep shadow replay contexts separate from mutable primary telemetry.
