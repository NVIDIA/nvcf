# Function Reference

Detailed reference for managing functions via `nvcf-cli`.

## Creating Functions

### From JSON file

```bash
nvcf-cli function create --input-file function.json
```

JSON file structure:

```json
{
  "name": "my-inference-function",
  "containerImage": "nvcr.io/your-org/your-image:tag",
  "inferenceUrl": "/predict",
  "inferencePort": 8000,
  "health": {
    "protocol": "HTTP",
    "uri": "/health",
    "port": 8000,
    "timeout": "PT30S",
    "expectedStatusCode": 200
  },
  "containerEnvironment": [
    {"key": "MODEL_PATH", "value": "/models"},
    {"key": "BATCH_SIZE", "value": "32"}
  ],
  "secrets": [
    {"name": "API_KEY", "value": "sk-12345"},
    {"name": "DB_PASSWORD", "value": "secret"}
  ]
}
```

### From CLI flags

```bash
nvcf-cli function create \
  --name <name> \
  --image <image> \
  --inference-url <endpoint-path> \
  --inference-port <port> \
  --health-uri <path> \
  --health-port <port> \
  --health-timeout <duration> \
  [--health-protocol <HTTP|gRPC>] \
  [--health-expected-status <code>] \
  [--description "<description>"] \
  [--function-type <DEFAULT|STREAMING>] \
  [--container-env <key=value>] \
  [--container-args "<args>"] \
  [--secrets <KEY1=value1,KEY2=value2>] \
  [--tags <tag1,tag2>] \
  [--api-body-format <format>] \
  [--rate-limit <pattern>] \
  [--rate-limit-exempted <nca-ids>] \
  [--rate-limit-sync] \
  [--models <name:version:uri>] \
  [--resources <name:version:uri>] \
  [--helm-chart <spec>] \
  [--helm-chart-service <service>] \
  [--metrics-telemetry-id <uuid>] \
  [--logs-telemetry-id <uuid>] \
  [--traces-telemetry-id <uuid>]
```

When both `--input-file` and CLI flags are provided, CLI flags override the JSON file values.

### Required Fields

| Field | Flag | JSON Key | Description |
|-------|------|----------|-------------|
| Name | `--name` | `name` | Function name |
| Image | `--image` | `containerImage` | Container image path |
| Inference URL | `--inference-url` | `inferenceUrl` | Endpoint path for inference requests (see note below) |
| Inference Port | `--inference-port` | `inferencePort` | Port the container listens on |

**Note on `inferenceUrl`:** This field is used **only for the health check** — it is the path the platform pings to determine when your function is ready. It is **not** used for invocation routing. HTTP invocations are routed to your container via the NVCF invocation path (`/v2/nvcf/pexec/functions/{id}`), regardless of what `inferenceUrl` is set to. Set it to the path your container responds to with a 200 at startup (e.g., `/health`, `/`, `/v1/health/ready`).

**Note on gRPC functions:** `--inference-url` is required by the API but is not used for gRPC routing either. Set it to a placeholder value such as `/grpc`. gRPC traffic is routed via the dedicated gRPC listener (port 10081) on the gateway, not by HTTP path.

### Health Configuration

Health checks determine when a deployed function instance is ready to serve traffic. When any health field is specified, `--health-timeout` is **required** (ISO 8601 duration, e.g., `PT30S`).

| Flag | JSON Key | Required | Default | Description |
|------|----------|----------|---------|-------------|
| `--health-uri` | `health.uri` | Yes* | None | Health endpoint path |
| `--health-port` | `health.port` | No | Same as inference port | Health endpoint port |
| `--health-timeout` | `health.timeout` | Yes | None | Health check timeout (ISO 8601: `PT10S`, `PT30S`, `PT1M`) |
| `--health-protocol` | `health.protocol` | No | `HTTP` | Health check protocol (`HTTP` or `gRPC`) |
| `--health-expected-status` | `health.expectedStatusCode` | No | `200` | Expected HTTP status code |

*Required for non-Triton containers. Without `--health-uri`, deployments will fail or get stuck in a non-ready state.

### Container Environment Variables

Pass environment variables using `--container-env` (repeatable):

```bash
nvcf-cli function create \
  --name my-function \
  --image my-image:latest \
  --inference-url /predict \
  --inference-port 8000 \
  --container-env MODEL_PATH=/models \
  --container-env BATCH_SIZE=32
```

Or in JSON:

```json
{
  "containerEnvironment": [
    {"key": "MODEL_PATH", "value": "/models"},
    {"key": "BATCH_SIZE", "value": "32"}
  ]
}
```

### Secrets

Pass secrets using `--secrets` (comma-separated `name=value` pairs):

```bash
nvcf-cli function create \
  --name my-function \
  --image my-image:latest \
  --inference-url /predict \
  --inference-port 8000 \
  --secrets API_KEY=secret123,DB_PASSWORD=pass456
```

Secrets are encrypted at rest and masked in logs.

Or in JSON:

```json
{
  "secrets": [
    {"name": "API_KEY", "value": "secret123"},
    {"name": "DB_PASSWORD", "value": "pass456"}
  ]
}
```

### Function Types

| Type | Description |
|------|-------------|
| `DEFAULT` | Standard request-response (default) |
| `STREAMING` | Streaming response |

```bash
nvcf-cli function create --function-type STREAMING ...
```

### Rate Limiting

```bash
nvcf-cli function create --rate-limit "100-M" ...
```

Rate limit patterns: `10-S` (per second), `100-M` (per minute), `1000-H` (per hour), `10000-D` (per day).

Exempt specific NCA IDs from rate limiting:

```bash
nvcf-cli function create --rate-limit "100-M" --rate-limit-exempted <nca-id> ...
```

### Helm Chart Functions

```bash
nvcf-cli function create \
  --name my-helm-function \
  --inference-url /predict \
  --inference-port 8000 \
  --helm-chart <org>/<chart>:<tag> \
  --helm-chart-service <service-name>
```

## Listing Functions

```bash
# List all functions
nvcf-cli function list

# List function IDs only
nvcf-cli function list-ids

# List versions of a specific function
nvcf-cli function list-versions <function-id>
```

## Getting Function Details

```bash
nvcf-cli function get --function-id <id> --version-id <version>

# JSON output
nvcf-cli function get --function-id <id> --version-id <version> --json
```

## Updating Functions

Update function tags:

```bash
nvcf-cli function update \
  --function-id <id> \
  --version-id <version> \
  --tags tag1,tag2,tag3
```

You can also provide updates via `--input-file`.

For updating deployments, use `nvcf-cli function deploy update` instead (see [deployment.md](deployment.md)).

## Deleting Functions

### Function/Version ID Resolution

The `delete` command resolves IDs in this priority order:

1. **Explicit arguments**: `nvcf-cli function delete <function-id> <version-id>`
2. **CLI flags**: `--function-id` and `--version-id`
3. **JSON file**: `--input-file` with `functionId` and `versionId`
4. **Current state**: Automatically uses the function from `nvcf-cli function create` (only if state was persisted successfully)

### Delete function entirely

```bash
# Delete using saved state
nvcf-cli function delete

# Delete specific function
nvcf-cli function delete <function-id> <version-id>

# Delete by flags
nvcf-cli function delete --function-id <id> --version-id <version>
```

Function deletion is permanent and cannot be undone.

### Delete deployment only

Remove the deployment but keep the function definition for redeployment later:

```bash
# Delete deployment only
nvcf-cli function delete --deployment-only

# Graceful shutdown (allow current requests to complete)
nvcf-cli function delete --deployment-only --graceful
```
