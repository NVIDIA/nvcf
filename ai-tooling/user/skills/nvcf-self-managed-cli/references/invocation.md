# Invocation Reference

Detailed reference for invoking functions via `nvcf-cli`.

## Prerequisites

- Function must be deployed and healthy
- API key must be generated (`nvcf-cli api-key generate`) or admin token available (`nvcf-cli init`)

## REST Invocation (Default)

### Using saved function context

After `function create`, the CLI saves the function/version IDs. Invoke directly:

```bash
nvcf-cli function invoke --request-body '{"input": "Hello, World!"}'
```

### With explicit IDs

```bash
nvcf-cli function invoke \
  --function-id <function-id> \
  --version-id <version-id> \
  --request-body '{"input": "Hello!"}'
```

### From JSON file

```bash
nvcf-cli function invoke --input-file invoke-config.json
```

### All Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--function-id` | Yes (or from state) | From state | Function ID |
| `--version-id` | Yes (or from state) | From state | Version ID |
| `--request-body` | Yes | None | JSON request body |
| `--input-file` | No | None | JSON config file (overrides flags) |
| `--timeout` | No | `60` | Request timeout in seconds |
| `--poll-duration` | No | `5` | Initial polling duration in seconds |
| `--poll-rate` | No | `3` | Polling rate in seconds |
| `--input-asset-references` | No | None | Input asset references |

## gRPC Invocation

gRPC uses a dedicated TCP listener on port 10081 in self-hosted deployments. Unlike HTTP, gRPC does not require host header configuration because the gateway routes all traffic on that port directly to the gRPC service.

**Self-hosted deployments require `--grpc-plaintext`** because the gateway does not use TLS on the gRPC port. Without it, invocations fail with a TLS handshake error.

You must also specify `--grpc-service` and `--grpc-method` matching your function's proto definition. The defaults (`nvidia.nvcf.v1.InferenceService/Predict`) will not work unless your function implements that exact interface.

### Discovering service and method names

Use `grpcurl` with gRPC reflection to discover the available services and methods:

```bash
grpcurl -plaintext \
  -H "function-id: <function-id>" \
  -H "function-version-id: <version-id>" \
  -H "authorization: Bearer $NVCF_API_KEY" \
  <GATEWAY_ADDR>:10081 list
```

Then describe a service to see its methods:

```bash
grpcurl -plaintext \
  -H "function-id: <function-id>" \
  -H "function-version-id: <version-id>" \
  -H "authorization: Bearer $NVCF_API_KEY" \
  <GATEWAY_ADDR>:10081 describe <ServiceName>
```

### Invoking via nvcf-cli

```bash
nvcf-cli function invoke --grpc --grpc-plaintext \
  --grpc-service <ServiceName> \
  --grpc-method <MethodName> \
  --function-id <function-id> \
  --version-id <version-id> \
  --request-body '{"message": "hello"}'
```

### Invoking via grpcurl

```bash
grpcurl -plaintext \
  -H "function-id: <function-id>" \
  -H "function-version-id: <version-id>" \
  -H "authorization: Bearer $NVCF_API_KEY" \
  -d '{"message": "hello"}' \
  <GATEWAY_ADDR>:10081 <ServiceName>/<MethodName>
```

### gRPC-Specific Flags

| Flag | Description |
|------|-------------|
| `--grpc` | Enable gRPC invocation (native Go client with JSON encoding) |
| `--grpc-service` | gRPC service name (required for non-default services) |
| `--grpc-method` | gRPC method name (required for non-default methods) |
| `--grpc-plaintext` | Use plaintext (insecure) gRPC connection (required for self-hosted) |

## Polling Behavior

For functions that take longer than the initial timeout, the CLI automatically polls for results:

1. Sends the invocation request
2. If the function doesn't respond within `--poll-duration` seconds, begins polling
3. Polls every `--poll-rate` seconds until the response is available or `--timeout` is reached

## Direct HTTP Invocation (curl)

For direct HTTP invocation outside the CLI, use the NVCF API key. The invocation service routes requests to functions via hostname-based routing, where the Host header includes the function ID.

For self-hosted deployments:

```bash
curl --request POST \
  --url "http://<GATEWAY_ADDR>/<inference-url>" \
  --header "Host: <function-id>.invocation.<GATEWAY_ADDR>" \
  --header "Authorization: Bearer $NVCF_API_KEY" \
  --header "Content-Type: application/json" \
  --data '{"input": "Hello!"}'
```

- `<GATEWAY_ADDR>` -- your Envoy Gateway address (e.g., the ELB hostname)
- `<function-id>` -- the function UUID returned by `function create`
- `<inference-url>` -- the inference endpoint path configured on the function (e.g., `/echo`, `/predict`)

## Queue Monitoring

### Queue Status

Get queue details for a function:

```bash
nvcf-cli function queue status <function-id> <version-id>
```

### Queue Position

Get the position of a specific request in the execution queue:

```bash
nvcf-cli function queue position <request-id>
```

## Authentication for Invocation

The `invoke` command prefers `NVCF_API_KEY` but falls back to the admin token (`NVCF_TOKEN`) if no API key is available.

For direct HTTP invocation via curl, you must have `NVCF_API_KEY` set. Verify before attempting:

```bash
nvcf-cli api-key show
```

If no API key exists:

```bash
nvcf-cli api-key generate --validate
```
