# Self-hosted CLI

This page provides documentation for the NVCF Self-hosted CLI, a command-line interface for managing NVIDIA Cloud Functions in self-hosted deployments.

## Overview

The NVCF Self-hosted CLI provides:

- **Automatic Token Generation**: Generate admin tokens and API keys via direct API calls
- **Smart State Management**: Persistent workflow context eliminates manual ID copying
- **Multi-Environment Support**: Separate configurations for dev/staging/production
- **gRPC Invocation**: Native support for gRPC function invocation
- **Shell Completion**: Autocompletion for bash, zsh, fish, and PowerShell

<Note>
The CLI is available as a container image from NGC. See [self-hosted-artifact-manifest](./manifest) for the full artifact path.

</Note>

## Prerequisites

- Network access to NVCF API endpoints
- [NGC CLI installed](https://org.ngc.nvidia.com/setup/installers/cli) (for downloading the CLI release from NGC)

## Installation

### Download from NGC

The CLI is available as a resource from NGC. See [download-nvcf-cli](./image-mirroring) for detailed download and extraction instructions.

The downloaded package includes:

- `nvcf-cli` - The CLI binary
- `.nvcf-cli.yaml.template` - Configuration template
- `examples/` - Sample configuration files
- `USAGE-GUIDE.md` - Detailed usage documentation

## Configuration

The CLI uses YAML configuration files. After extracting the CLI, copy the included template:

```bash
cp .nvcf-cli.yaml.template .nvcf-cli.yaml
```

Configuration files are searched in this order:

1. **Explicit path** via `--config` flag (highest priority)
2. **Current directory**: `./.nvcf-cli.yaml`
3. **Home directory**: `~/.nvcf-cli.yaml`

<Tip>
Place your `.nvcf-cli.yaml` in the directory where you run the CLI for project-specific configuration, or in your home directory for global configuration.

</Tip>

### Self-Hosted Configuration

For self-hosted deployments, the CLI must be configured to communicate with your gateway. The gateway uses **hostname-based routing** for HTTP services, which requires proper configuration.

<Note>
For a complete understanding of how the gateway routes traffic, including architecture diagrams, verification commands, and production DNS/HTTPS setup, see [gateway-routing](./gateway-routing).

</Note>

#### Get Your Gateway Address

After deploying the control plane, get your gateway's external address (this assumes you followed Step 1 of [helmfile-installation](./helmfile-installation)):

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway \
  -o jsonpath='{.status.addresses[0].value}')
echo "Gateway Address: $GATEWAY_ADDR"
```

#### Configuring the CLI

Create your configuration file:

```bash
# Copy the template
cp .nvcf-cli.yaml.template .nvcf-cli.yaml
```

**Complete Self-Hosted Configuration:**

```yaml
# ==============================================================================
# API Endpoints - Point to your gateway load balancer
# ==============================================================================

# Main API endpoint (use http:// for non-TLS setups)
base_http_url: "http://<GATEWAY_ADDR>"

# Invocation endpoint (same as base_http_url for self-hosted)
invoke_url: "http://<GATEWAY_ADDR>"

# gRPC endpoint - uses dedicated TCP port (no Host header needed)
base_grpc_url: "<GATEWAY_ADDR>:10081"

# API Keys service endpoint
api_keys_service_url: "http://<GATEWAY_ADDR>"

# ==============================================================================
# Host Header Overrides (Required for Hostname-Based Routing)
# ==============================================================================
#
# Because the gateway routes HTTP requests based on the Host header,
# you must specify the correct Host header for each service.
# These values must match your HTTPRoute hostnames.
#
# Without these, the gateway returns 404 because it can't match the route.

# Host header for API Keys service
api_keys_host: "api-keys.<GATEWAY_ADDR>"

# Host header for NVCF API (function management)
api_host: "api.<GATEWAY_ADDR>"

# Host header for Invocation service
invoke_host: "invocation.<GATEWAY_ADDR>"

# ==============================================================================
# API Keys Service Configuration
# ==============================================================================

api_keys_service_id: "nvidia-cloud-functions-ncp-service-id-aketm"
api_keys_issuer_service: "nvcf-api"
api_keys_owner_id: "svc@nvcf-api.local"

# ==============================================================================
# Account Configuration
# ==============================================================================

client_id: "nvcf-default"

# ==============================================================================
# Debugging (optional - set to true for verbose output)
# ==============================================================================

debug: false
```

**Example: AWS ELB Configuration**

For a typical AWS EKS deployment with an ELB load balancer:

```yaml
# Gateway load balancer address
base_http_url: "http://a1b2c3d4e5f6.us-west-2.elb.amazonaws.com"
invoke_url: "http://a1b2c3d4e5f6.us-west-2.elb.amazonaws.com"
base_grpc_url: "a1b2c3d4e5f6.us-west-2.elb.amazonaws.com:10081"
api_keys_service_url: "http://a1b2c3d4e5f6.us-west-2.elb.amazonaws.com"

# Host headers matching HTTPRoute configuration
api_keys_host: "api-keys.a1b2c3d4e5f6.us-west-2.elb.amazonaws.com"
api_host: "api.a1b2c3d4e5f6.us-west-2.elb.amazonaws.com"
invoke_host: "invocation.a1b2c3d4e5f6.us-west-2.elb.amazonaws.com"

# API Keys service
api_keys_service_id: "nvidia-cloud-functions-ncp-service-id-aketm"
api_keys_issuer_service: "nvcf-api"
api_keys_owner_id: "svc@nvcf-api.local"

client_id: "nvcf-default"
debug: true  # Enable for initial testing
```

#### Verifying Your Configuration

After configuring the CLI, verify connectivity:

```bash
# Test token generation (uses api_keys_host)
./nvcf-cli init

# Expected output:
# [INFO] Starting fresh session...
# [INFO] Generating admin token from API Keys service...
# [DEBUG] Using Host header override: api-keys.<your-gateway>
# [SUCCESS] Admin token generated and saved
```

If you see a 404 error, verify:

1. The `api_keys_host` value matches your HTTPRoute hostname
2. The gateway load balancer is accessible
3. The API Keys service is running: `kubectl get pods -n api-keys`

<Note>
**Why Host Headers?** The Envoy Gateway uses hostname-based routing to direct traffic to different backend services through a single load balancer. Without the correct `Host` header, the gateway cannot match the request to a route and returns 404.

</Note>

<Tip>
**gRPC doesn't need Host headers** because it uses a dedicated TCP listener on port 10081. The gateway routes all traffic on that port directly to the gRPC service without hostname matching.

</Tip>

#### Production Setup: DNS and HTTPS

The Host header configuration above is designed for **testing and development**. For **production deployments**, configure proper DNS and TLS to eliminate the need for Host header overrides.

With proper DNS and HTTPS configured:

- DNS records resolve service hostnames directly to your Gateway's load balancer
- TLS certificates secure all traffic
- The CLI uses simple URLs without Host header overrides
- Browsers and other clients can access services directly

```yaml
# Simple URLs using your domain - no Host header overrides needed!
base_http_url: "https://api.nvcf.example.com"
invoke_url: "https://invocation.nvcf.example.com"
base_grpc_url: "grpc.nvcf.example.com:443"
api_keys_service_url: "https://api-keys.nvcf.example.com"

# No host header overrides required - DNS handles routing
# api_keys_host: ""  # Not needed
```

<Note>
For complete instructions on setting up DNS records and TLS certificates, see [production-dns-https](./gateway-routing) in the Gateway Routing guide.

</Note>

#### Multi-Environment Setup

Use the `--config` flag to manage multiple environments with separate configuration files:

```bash
# Development
./nvcf-cli --config dev.yaml init
./nvcf-cli --config dev.yaml function create --input-file function.json

# Production
./nvcf-cli --config prod.yaml init
./nvcf-cli --config prod.yaml function list
```

Each configuration maintains separate state files (e.g., `~/.nvcf-cli.dev.state` for `dev.yaml`).

#### Debug Mode

Enable debug mode for detailed logging by adding to your configuration file:

```yaml
debug: true
```

Or use the `--debug` flag or `NVCF_DEBUG=true` environment variable per-command.

## Quick Start

```bash
# 1. Initialize - generate admin token
./nvcf-cli init

# 2. Generate API key for invocations
./nvcf-cli api-key generate

# 3. Create a function, update example file with your image
./nvcf-cli function create --input-file examples/create-function.json

# 4. Deploy the function (uses saved context automatically)
./nvcf-cli function deploy create

# 5. Invoke the function
./nvcf-cli function invoke --request-body '{"message": "hello world"}'

# 6. Clean up
./nvcf-cli function deploy remove
./nvcf-cli function delete
```

<Note>
For immediate testing, you can use `load_tester_supreme` from `nvcf-onprem` (see [self-hosted-artifact-manifest](./manifest)), which supports the `{"message": "hello world"}` request body above. For more function samples, see the [nv-cloud-function-helpers](https://github.com/NVIDIA/nv-cloud-function-helpers) repository and [function-creation](./function-creation) for function creation documentation.

</Note>

## Authentication

The CLI supports two types of authentication tokens:

- **Admin Token (NVCF_TOKEN)**: For function management (create, deploy, update, delete)
- **API Key (NVCF_API_KEY)**: For user operations (invoke, list, queue status)

### Generate Admin Token

```bash
# Generate fresh admin token (clears existing state)
./nvcf-cli init

# With debug output
./nvcf-cli init --debug

# Example output:
# [INFO] Starting fresh session...
# [INFO] Generating admin token from API Keys service...
# [SUCCESS] Admin token generated and saved
# Token: <admin-token>
# Expires: 2025-11-19 06:08:15
```

### Refresh Admin Token

Refresh your token while preserving function context:

```bash
# Refresh token (keeps current function state)
./nvcf-cli refresh

# Example output:
# [SUCCESS] Admin token refreshed
# Function ID: func-abc123  (preserved)
```

### Generate API Key

```bash
# Generate with defaults (24h expiration)
./nvcf-cli api-key generate

# Custom expiration and description
./nvcf-cli api-key generate --expires-in 48h --description "Production key"

# Generate with custom scopes
./nvcf-cli api-key generate --scopes invoke_function,list_functions

# Generate and validate
./nvcf-cli api-key generate --validate
```

Available scopes for API keys (all included by default):

| Scope | Description |
| --- | --- |
| `invoke_function` | Execute deployed functions |
| `list_functions` | View available functions |
| `list_functions_details` | View detailed function metadata |
| `queue_details` | Monitor function execution queues |
| `manage_registries` | Manage registry credentials |

## Command Reference

### General Commands

| Command | Description |
| --- | --- |
| `init` | Generate admin token and start fresh session |
| `refresh` | Refresh admin token while preserving function context |
| `status` | Display CLI state and configuration (use `--show-tokens` for full token output) |
| `version` | Show CLI version information |
| `completion` | Generate shell autocompletion scripts (supports `bash`, `zsh`, `fish`, `powershell`) |

### API Key Commands

| Command | Description |
| --- | --- |
| `api-key generate` | Generate a new API key for function operations |
| `api-key list` | List all API keys |
| `api-key show` | Show the current saved API key |
| `api-key delete` | Delete a specific API key (supports `--force`) |
| `api-key revoke` | Revoke an API key (same as delete, supports `--force`) |
| `api-key clear` | Clear saved API key from state (supports `--force`) |
| `api-key clear-all` | Delete all API keys for an owner (supports `--force`) |

### Function Management Commands

**Create Function**

```bash
# Create from JSON file
./nvcf-cli function create --input-file examples/create-function.json

# Create with CLI flags
./nvcf-cli function create \
  --name "my-function" \
  --image "nvcr.io/your-org/your-image:tag" \
  --inference-url "/predict" \
  --inference-port 8000 \
  --health-uri "/health" \
  --health-port 8000

# Create with additional options
./nvcf-cli function create \
  --name "my-function" \
  --image "nvcr.io/your-org/your-image:tag" \
  --inference-url "/predict" \
  --inference-port 8000 \
  --function-type STREAMING \
  --container-env "KEY1=value1" \
  --secrets "API_KEY=secret123" \
  --tags "production,v2" \
  --rate-limit "100-S"
```

All `function create` flags:

| Flag | Description |
| --- | --- |
| `--name` | Function name (required) |
| `--image` | Container image (required) |
| `--inference-url` | Inference endpoint URL (required) |
| `--inference-port` | Inference endpoint port (required) |
| `--input-file` | JSON file with function configuration |
| `--description` | Function description |
| `--function-type` | `DEFAULT` or `STREAMING` (default: `DEFAULT`) |
| `--api-body-format` | API body format (default: `CUSTOM`) |
| `--health-uri` | Health check endpoint URI |
| `--health-port` | Health check endpoint port |
| `--health-protocol` | Health protocol (`HTTP` or `gRPC`) |
| `--health-timeout` | Health check timeout (ISO 8601 duration, e.g., `PT30S`) |
| `--health-expected-status` | Expected health check status code (default: 200) |
| `--container-args` | Arguments for container launch |
| `--container-env` | Environment variables in `key=value` format (repeatable) |
| `--secrets` | Secrets in `name=value` format (repeatable) |
| `--tags` | Comma-separated tags |
| `--models` | Model artifacts in `name:version:uri` format (repeatable) |
| `--resources` | Resource artifacts in `name:version:uri` format (repeatable) |
| `--helm-chart` | Helm chart specification |
| `--helm-chart-service` | Helm chart service name |
| `--rate-limit` | Rate limit pattern (e.g., `100-S`, `50-M`, `10-H`, `5-D`) |
| `--rate-limit-exempted` | NCA IDs exempted from rate limiting (repeatable) |
| `--rate-limit-sync` | Enable synchronous rate limit checking |

Example function JSON:

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
  }
}
```

**Deploy Function**

The `function deploy` command group manages deployments with the following subcommands:

| Command | Description |
| --- | --- |
| `function deploy create` | Create a new deployment for a function |
| `function deploy update` | Update an existing deployment |
| `function deploy get` | Get deployment details (supports `--json` for raw output) |
| `function deploy remove` | Remove a function deployment |

```bash
# Deploy using saved context (from create)
./nvcf-cli function deploy create

# Deploy with explicit IDs and configuration
./nvcf-cli function deploy create \
  --function-id <function-id> \
  --version-id <version-id> \
  --instance-type "NCP.GPU.A10G_1x" \
  --gpu "A10G" \
  --min-instances 1 \
  --max-instances 1

# Deploy from JSON file
./nvcf-cli function deploy create --input-file examples/deploy-function.json

# View deployment details
./nvcf-cli function deploy get \
  --function-id <function-id> \
  --version-id <version-id>

# Update an existing deployment
./nvcf-cli function deploy update \
  --function-id <function-id> \
  --version-id <version-id> \
  --gpu "A10G" \
  --instance-type "NCP.GPU.A10G_1x" \
  --min-instances 2 \
  --max-instances 4

# Remove a deployment
./nvcf-cli function deploy remove \
  --function-id <function-id> \
  --version-id <version-id>
```

Key `function deploy create` flags:

| Flag | Description |
| --- | --- |
| `--function-id` | Function ID (uses state if not specified) |
| `--version-id` | Version ID (uses state if not specified) |
| `--gpu` | GPU name (default: `H100`) |
| `--instance-type` | Instance type (default: `NCP.GPU.H100_1x`) |
| `--min-instances` | Minimum instances (default: 1) |
| `--max-instances` | Maximum instances (default: 1) |
| `--max-request-concurrency` | Max request concurrency (1-1024) |
| `--input-file` | JSON file with deployment configuration |
| `--backend` | Backend/CSP for the GPU instance |
| `--regions` | Allowed deployment regions (repeatable) |
| `--clusters` | Specific clusters (repeatable) |
| `--availability-zones` | Availability zones (repeatable) |
| `--timeout` | Deployment timeout in seconds (default: 900) |
| `--storage` | Available storage (e.g., `80G`) |
| `--system-memory` | Amount of RAM |
| `--gpu-memory` | Amount of GPU memory |

Example deployment JSON:

```json
{
  "deploymentSpecifications": [
    {
      "gpu": "A10G",
      "instanceType": "NCP.GPU.A10G_1x",
      "minInstances": 1,
      "maxInstances": 1
    }
  ]
}
```

**List and Get Functions**

```bash
# List all functions
./nvcf-cli function list

# List function IDs only
./nvcf-cli function list-ids

# List versions of a specific function
./nvcf-cli function list-versions <function-id>

# Get details of a specific function version
./nvcf-cli function get \
  --function-id <function-id> \
  --version-id <version-id>

# Get details as raw JSON
./nvcf-cli function get \
  --function-id <function-id> \
  --version-id <version-id> \
  --json
```

**Update Function**

```bash
# Update function tags
./nvcf-cli function update \
  --function-id <function-id> \
  --version-id <version-id> \
  --tags "production,v2"

# Update from JSON file
./nvcf-cli function update \
  --function-id <function-id> \
  --version-id <version-id> \
  --input-file metadata-update.json
```

**Invoke Function**

```bash
# Invoke using saved context
./nvcf-cli function invoke --request-body '{"input": "Hello, World!"}'

# gRPC invocation
./nvcf-cli function invoke --grpc --request-body '{"input": "test"}'

# gRPC with custom service and method
./nvcf-cli function invoke --grpc \
  --grpc-service "MyService" \
  --grpc-method "Predict" \
  --request-body '{"input": "test"}'

# Invoke with explicit IDs and timeout
./nvcf-cli function invoke \
  --function-id <function-id> \
  --version-id <version-id> \
  --request-body '{"input": "Hello!"}' \
  --timeout 120
```

Additional `function invoke` flags:

| Flag | Description |
| --- | --- |
| `--grpc` | Use gRPC invocation |
| `--grpc-service` | gRPC service name |
| `--grpc-method` | gRPC method name |
| `--grpc-plaintext` | Use plaintext (insecure) gRPC |
| `--timeout` | Request timeout in seconds (default: 60) |
| `--poll-duration` | Initial polling duration in seconds (default: 5) |
| `--poll-rate` | Polling rate in seconds (default: 3) |
| `--input-file` | JSON file with invocation configuration |
| `--input-asset-references` | Input asset references (repeatable) |

**Queue Management**

```bash
# Get queue status for a function
./nvcf-cli function queue status <function-id> <version-id>

# Get position for a specific request
./nvcf-cli function queue position <request-id>
```

**Delete Function**

```bash
# Delete current function from state
./nvcf-cli function delete

# Delete specific function
./nvcf-cli function delete --function-id <func-id> --version-id <ver-id>

# Delete deployment only (keep function definition)
./nvcf-cli function delete --deployment-only --graceful
```

### Registry Commands

Manage container registry credentials for function images and Helm charts. For comprehensive setup instructions including IAM configuration for AWS ECR, see [third-party-registries-self-hosted](./third-party-registries).

| Command | Description |
| --- | --- |
| `registry add` | Add a new registry credential |
| `registry list` | List all registry credentials |
| `registry get` | Get details of a specific credential |
| `registry update` | Update an existing credential |
| `registry delete` | Delete a registry credential |
| `registry list-recognized` | List all recognized registries |

```bash
# Add registry credentials using base64 secret
./nvcf-cli registry add \
  --hostname "nvcr.io" \
  --secret "<BASE64_ENCODED_USERNAME:PASSWORD>" \
  --artifact-type CONTAINER \
  --description "NGC Container Registry"

# Add registry credentials using username/password
./nvcf-cli registry add \
  --hostname "nvcr.io" \
  --username "<USERNAME>" \
  --password "<PASSWORD>" \
  --artifact-type CONTAINER

# List registry credentials (with optional filters)
./nvcf-cli registry list
./nvcf-cli registry list --artifact-type CONTAINER
./nvcf-cli registry list --provisioned-by USER

# Get details for a specific credential
./nvcf-cli registry get <credential-id>

# Update a credential
./nvcf-cli registry update <credential-id> \
  --username "<NEW_USERNAME>" \
  --password "<NEW_PASSWORD>"

# Delete registry credentials
./nvcf-cli registry delete <credential-id>
./nvcf-cli registry delete <credential-id> --force

# List recognized registries
./nvcf-cli registry list-recognized
```

## Troubleshooting

### Authentication Errors

**401 Unauthorized on function creation:**

```bash
# Regenerate admin token
./nvcf-cli init --debug

# Verify token is being used
./nvcf-cli function create --debug --input-file function.json
# Look for: "Using FUNCTION TOKEN for POST"
```

**403 Forbidden on invocation:**

```bash
# Regenerate API key
./nvcf-cli api-key generate --validate

# Verify API key is being used
./nvcf-cli function invoke --debug --request-body '{"input": "test"}'
# Look for: "Using API KEY for POST"
```

### Token Expiration

```bash
# Check token status
./nvcf-cli status

# Refresh admin token
./nvcf-cli refresh

# Regenerate API key
./nvcf-cli api-key generate
```

### Connection Issues

```bash
# Enable debug mode to see request details
./nvcf-cli function list --debug

# Verify CLI state and configuration
./nvcf-cli status
```

### Token Usage Summary

| Operation | Required Token | Notes |
| --- | --- | --- |
| `function create` | `NVCF_TOKEN` | Admin token required |
| `function deploy create` | `NVCF_TOKEN` | Falls back to API key |
| `function delete` | `NVCF_TOKEN` | **No fallback** - admin only |
| `function invoke` | `NVCF_API_KEY` | Falls back to admin token |
| `function list` | `NVCF_API_KEY` | Falls back to admin token |

For additional troubleshooting, see [self-hosted-troubleshooting](./troubleshooting).
