<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->
# NVCF CLI Usage Guide

Complete guide for using the NVIDIA Cloud Functions CLI with direct HTTPS API calls.

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Authentication Setup](#authentication-setup)
3. [API Keys Service Endpoints](#api-keys-service-endpoints)
   - [Endpoint Overview](#endpoint-overview)
   - [/v1/admin/keys - JWT Token Generation](#v1adminkeys---jwt-token-generation)
   - [/v1/keys - API Key Generation](#v1keys---api-key-generation)
   - [Workflow: Which Endpoint to Use?](#workflow-which-endpoint-to-use)
4. [Configuration](#configuration)
   - [Smart Configuration Integration](#smart-configuration-integration)
5. [Registry Credential Management](#registry-credential-management)
6. [Function Lifecycle](#function-lifecycle)
7. [Cloud Tasks (NVCT)](#cloud-tasks-nvct)
8. [Additional Operations](#additional-operations)
9. [Troubleshooting](#troubleshooting)

## Prerequisites

### Required Tools
- Network access to NVCF API endpoints
- Configuration file (`.nvcf-cli.yaml`) - optional but recommended

### Simple Setup

```bash
# 1. Copy the configuration template
cp .nvcf-cli.yaml.template .nvcf-cli.yaml

# 2. Edit for your environment (production or staging)
vi .nvcf-cli.yaml

# 3. That's it! You're ready to use the CLI
```

## Authentication Setup

### 1. Generate Admin Token

Admin tokens are required for privileged operations like function registration and registry management.

#### Generate Token via API
```bash
# Generate admin token automatically (saves to state file)
./nvcf-cli init --debug

# The init command automatically:
# - Calls API Keys service endpoint (/v1/admin/keys)
# - Generates JWT admin token with all required scopes
# - Saves token to ~/.nvcf-cli-state.json with expiration
# - Token includes scopes: register_function, list_functions, deploy_function,
#   update_function, delete_function, manage_telemetries, manage_registry_credentials

# Example output:
# [INFO] Starting fresh session...
# [INFO] Generating admin token from API Keys service...
# [SUCCESS] Admin token generated and saved
# Token: eyJhbGciOiJFUzI1NiIs...
# Expires: 2025-11-19 06:08:15
```

#### Token for Different Environments
```bash
# Production (default)
./nvcf-cli init

# Staging
API_KEYS_SERVICE_URL=https://api-keys.shqa.stg.nvcf.nvidia.com ./nvcf-cli init

# Using config file
./nvcf-cli --config staging.yaml init
```

### 2. Generate API Keys

API keys are used for general user operations.

#### Available Scopes

| Scope | Purpose | Operations Enabled |
|-------|---------|-------------------|
| `invoke_function` | Function invocation | Execute deployed functions, submit inference requests |
| `list_functions` | Function listing | View functions you have access to |
| `list_functions_details` | Detailed function info | View comprehensive function metadata |
| `queue_details` | Queue monitoring | Monitor function execution queues, check request status |
| `register_function` | Function registration | Create new functions (requires JWT token) |
| `deploy_function` | Function deployment | Deploy functions to clusters (requires JWT token) |
| `delete_function` | Function deletion | Delete functions (requires JWT token) |
| `update_function` | Function updates | Modify function metadata (requires JWT token) |
| `manage_registry_credentials` | Registry management | Manage container registry credentials (requires JWT token) |
| `manage_telemetries` | Telemetry management | Configure telemetry settings (requires JWT token) |
| `authorize_clients` | Authorization management | Manage function access authorizations (requires JWT token) |

#### Default Scopes

When no `--scopes` flag is provided, API keys are generated with these **default scopes**:
- `invoke_function`
- `list_functions`
- `queue_details`
- `list_functions_details`

These default scopes are sufficient for most read-only user operations (invoking functions, monitoring queues, and viewing function information). For write operations, you need to use JWT tokens (NVCF_TOKEN).

#### Custom Scopes Use Cases

**Minimal Access (invoke-only):**
```bash
--scopes "invoke_function"
```
For applications that only need to invoke functions.

**Read-Only Access:**
```bash
--scopes "list_functions,list_functions_details,queue_details"
```
For monitoring and discovery without invocation privileges.

**Extended User Access:**
```bash
--scopes "invoke_function,list_functions,queue_details,list_functions_details"
```
Default scopes for extended access.

#### Important Notes on Custom Scopes

**Scope Limitations**: API keys are intended for read-only operations. Write operations (create, deploy, update, delete functions) require JWT tokens (NVCF_TOKEN) with appropriate scopes.

**Principle of Least Privilege**: Always use the minimum scopes necessary for your use case. The default read-only scopes are sufficient for most applications.

**Default Recommendation**: For most applications, the default scopes are sufficient and provide a good balance of functionality and security. Use JWT tokens for function management operations.

#### Flag Reference

| Flag | Description | Example |
|------|-------------|---------|
| `--expires-in` | Duration until key expires | `24h`, `7d`, `168h` |
| `--description` | Human-readable description | `"Development API key"` |
| `--scopes` | Comma-separated list of scopes | `invoke_function,list_functions` |
| `--validate` | Test the key after generation | `--validate` |

#### CLI Usage
```bash
# Generate API key with default scopes (requires admin token in config)
./nvcf-cli api-key generate \
  --expires-in 7d \
  --description "NVCF CLI Key"

# Generate API key with custom scopes
./nvcf-cli api-key generate \
  --expires-in 7d \
  --description "Custom scoped key" \
  --scopes "invoke_function,list_functions"

# Generate API key with extended scopes
./nvcf-cli api-key generate \
  --expires-in 7d \
  --description "Extended permissions key" \
  --scopes "invoke_function,list_functions,queue_details,list_functions_details,register_function"

# List API keys
./nvcf-cli api-key list

# Show current API key details
./nvcf-cli api-key show

# Delete specific API key
./nvcf-cli api-key delete --key-id <KEY_ID>

# Clear current API key from state
./nvcf-cli api-key clear

# Bulk delete all API keys (use with caution)
./nvcf-cli api-key clear-all
```

## API Keys Service Endpoints

For customers building their own tools, the NVCF platform provides two distinct API Keys Service endpoints for token generation. These endpoints are **not part of the NVCF OpenAPI specification** and are hosted separately at the API Keys service.

### Endpoint Overview

| Endpoint | Purpose | Token Type | Use Case |
|----------|---------|------------|----------|
| `/v1/admin/keys` | Generate JWT admin tokens | JWT (Bearer token) | Function management operations (create, deploy, delete, update, registry) |
| `/v1/keys` | Generate API keys | API Key | User operations (invoke, list) |

### `/v1/admin/keys` - JWT Token Generation

**Base URL:** `https://api-keys.{environment}.nvcf.nvidia.com`

**Purpose:** Generate JWT tokens for administrative and function management operations.

**HTTP Method:** `POST`

**Request:**
```bash
curl -X POST https://api-keys.shqa.stg.nvcf.nvidia.com/v1/admin/keys \
  -H "Content-Type: application/json" \
  -d '{}'
```

**Response:**
```json
{
  "id": "6059b36c-b977-0d6a-b128-7c09458f2a57",
  "value": "eyJhbGciOiJFUzI1NiIsImtpZCI6IkFSTF9vdG1BSV9FeWEyVWUzRno0ek9lcm9hUSIsInR5cCI6IkpXVCJ9...",
  "status": "ACTIVE",
  "owner_type": "SYSTEM",
  "owner_id": "admin-issuer-proxy",
  "issuer_service_id": "nvidia-cloud-functions-ncp-service-id-aketm",
  "audience_service_ids": ["nvidia-cloud-functions-ncp-service-id-aketm"],
  "description": "Admin token",
  "created_at": "2025-11-18T18:59:51Z",
  "expires_at": "2025-11-19T06:59:51Z",
  "authorizations": {
    "policies": [{
      "aud": "nvidia-cloud-functions-ncp-service-id-aketm",
      "auds": ["nvidia-cloud-functions-ncp-service-id-aketm"],
      "product": "nv-cloud-functions",
      "resources": [
        {"id": "*", "type": "account-functions"},
        {"id": "*", "type": "authorized-functions"}
      ],
      "scopes": [
        "register_function",
        "list_functions",
        "list_functions_details",
        "deploy_function",
        "update_function",
        "update_secrets",
        "delete_function",
        "manage_telemetries",
        "manage_registry_credentials"
      ]
    }]
  }
}
```

**Token Scopes:**
- `register_function` - Create new functions
- `deploy_function` - Deploy and undeploy functions
- `update_function` - Update function metadata
- `delete_function` - Delete functions
- `manage_registry_credentials` - Manage Docker registry credentials
- `list_functions` - List all functions
- `list_functions_details` - Get detailed function information
- `update_secrets` - Manage function secrets
- `manage_telemetries` - Manage telemetry settings

**Usage:** The JWT token (`value` field) should be used as a Bearer token in the `Authorization` header for NVCF API operations:
```bash
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6IkFSTF9vdG1BSV9FeWEyVWUzRno0ek9lcm9hUSIsInR5cCI6IkpXVCJ9...
```

### `/v1/keys` - API Key Generation

**Base URL:** `https://api-keys.{environment}.nvcf.nvidia.com`

**Purpose:** Generate API keys for user operations (function invocation and listing).

**HTTP Method:** `POST`

**Authentication Required:** Must provide a valid JWT token from `/v1/admin/keys` in the Authorization header.

**Request:**
```bash
curl -X POST https://api-keys.shqa.stg.nvcf.nvidia.com/v1/keys \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <JWT_TOKEN>" \
  -H "Key-Issuer-Service: nvcf-api" \
  -H "Key-Issuer-Id: nvidia-cloud-functions-ncp-service-id-aketm" \
  -H "Key-Owner-Id: svc@nvcf-api.local" \
  -d '{
    "audience_service_ids": ["nvidia-cloud-functions-ncp-service-id-aketm"],
    "authorizations": {
      "policies": [{
        "aud": "nvidia-cloud-functions-ncp-service-id-aketm",
        "auds": ["nvidia-cloud-functions-ncp-service-id-aketm"],
        "product": "nv-cloud-functions",
        "resources": [
          {"id": "*", "type": "account-functions"},
          {"id": "*", "type": "authorized-functions"}
        ],
        "scopes": [
          "invoke_function",
          "list_functions",
          "queue_details",
          "list_functions_details"
        ]
      }]
    },
    "description": "API key for function invocation",
    "expires_at": "2025-11-20T22:15:29.237Z"
  }'
```

**Response:**
```json
{
  "id": "fb63fee3-0321-4a67-b77a-102c2b5aea49",
  "value": "nvapi-nvcf-stg-121e78JIqKdjzxiJvGHtYhYuNAj8EshCuzxSQLYX-AUMbBgIQO_SepmViUvuBGuk",
  "status": "ACTIVE",
  "owner_type": "USER",
  "owner_id": "svc@nvcf-api.local",
  "issuer_service_id": "nvidia-cloud-functions-ncp-service-id-aketm",
  "audience_service_ids": ["nvidia-cloud-functions-ncp-service-id-aketm"],
  "description": "API key for function invocation",
  "created_at": "2025-11-19T22:15:29.533Z",
  "expires_at": "2025-11-20T22:15:29.237Z",
  "authorizations": {
    "policies": [{
      "aud": "nvidia-cloud-functions-ncp-service-id-aketm",
      "auds": ["nvidia-cloud-functions-ncp-service-id-aketm"],
      "product": "nv-cloud-functions",
      "resources": [
        {"id": "*", "type": "account-functions"},
        {"id": "*", "type": "authorized-functions"}
      ],
      "scopes": [
        "invoke_function",
        "list_functions",
        "queue_details",
        "list_functions_details"
      ]
    }]
  }
}
```

**API Key Scopes:**
- `invoke_function` - Invoke deployed functions
- `list_functions` - List functions
- `list_functions_details` - Get detailed function information
- `queue_details` - Query function queue status

**Usage:** The API key (`value` field) should be used directly as a Bearer token in the `Authorization` header for NVCF API operations:
```bash
Authorization: Bearer nvapi-nvcf-stg-121e78JIqKdjzxiJvGHtYhYuNAj8EshCuzxSQLYX-AUMbBgIQO_SepmViUvuBGuk
```

### Workflow: Which Endpoint to Use?

**For Function Management Operations:**
1. Call `/v1/admin/keys` to generate a JWT token
2. Use the JWT token for:
   - Creating functions (`POST /v2/nvcf/functions`)
   - Deploying functions (`POST /v2/nvcf/deployments/functions/{id}/versions/{vid}`)
   - Updating functions (`PUT /v2/nvcf/functions/{id}/versions/{vid}`)
   - Deleting functions (`DELETE /v2/nvcf/functions/{id}/versions/{vid}`)
   - Managing registry credentials (`POST /v2/nvcf/registry-credentials`)

**For User Operations:**
1. Call `/v1/admin/keys` to get a JWT token
2. Call `/v1/keys` with the JWT token to generate an API key
3. Use the API key for:
   - Invoking functions (`POST http://<function-id>.invocation.<gateway-addr>/<inference-path>`)
   - Listing functions (`GET /v2/nvcf/functions`)

### Environment-Specific URLs

| Environment | API Keys Service Base URL |
|-------------|---------------------------|
| Production | `https://api-keys.nvcf.nvidia.com` |
| Staging | `https://api-keys.shqa.stg.nvcf.nvidia.com` |

### Important Notes

1. **JWT tokens are short-lived** (typically 12 hours) and should be refreshed regularly
2. **API keys can have custom expiration** (default 24 hours in the CLI)
3. **Both tokens support the same NVCF API endpoints**, but JWT has more scopes
4. **API Keys Service endpoints are separate** from NVCF API endpoints
5. **The CLI handles this automatically** - `init` calls `/v1/admin/keys` and `api-key generate` calls `/v1/keys`

## Configuration

> **Important**: The CLI automatically manages tokens in a state file (`~/.nvcf-cli.state`). Fresh tokens generated by `init` and `api-key generate` take precedence over static config file values. See [Configuration Priority](#configuration-priority) for details.

### Sample Configuration File (`~/.nvcf-cli.yaml`)

```yaml
# NVIDIA Cloud Functions CLI Configuration

# ================================
# Authentication Configuration
# ================================

# JWT Token for admin operations (from token generate command)
NVCF_TOKEN: "eyJhbGciOiJFUzI1NiIsImtpZCI6IjNPSVdkUE11eW84eFJQeUV4blJ0RlBFVldXUSIsInR5cCI6IkpXVCJ9..."

# API Key for general operations (from api-key generate command)
NVCF_API_KEY: "nvapi-nvcf-stg-EIMjEiBR3mRbCBWRTx3EWkLpnXhxVVr2W2P8467WV8I9-dtxUYF4jHlvzxSM1PGi"

# ================================
# API Endpoints
# ================================

# For production/staging environments
NVCF_BASE_HTTP_URL: "https://api.nvcf.nvidia.com"
NVCF_BASE_GRPC_URL: "grpc.nvcf.nvidia.com:443"
NVCF_INVOKE_URL: "https://invocation.nvcf.nvidia.com"

# Client ID (account identifier)
NVCF_CLIENT_ID: "nvcf-default"

# ================================
# Debug and Configuration
# ================================

# Enable debugging for troubleshooting
debug: true

# Demo mode for testing
demo: false
```

### Smart Configuration Integration

The NVCF CLI uses a **unified configuration system** that seamlessly integrates two files:
- **`.nvcf-cli.yaml`**: User configuration and manual overrides
- **`.nvcf-cli.state`**: Runtime state and auto-generated tokens

#### Configuration Priority Chain

The CLI loads tokens and configuration using this priority order:

```
Priority 1: Environment variables (NVCF_TOKEN, NVCF_API_KEY) - Highest
Priority 2: State file (.nvcf-cli.state) - Auto-generated, fresh tokens
Priority 3: Config file (.nvcf-cli.yaml) - Static configuration
Priority 4: Defaults - Lowest
```

**Why this order?**
- **Environment variables** allow immediate overrides without file changes
- **State file** contains freshly generated tokens from `init` and `api-key generate`
- **Config file** provides static configuration and fallback values
- **Defaults** ensure the CLI works even without configuration

#### How It Works

When you run any CLI command, the system:

1. **Loads both files** (state file is loaded first for fallback values)
2. **Validates token expiration** (expired state tokens are ignored)
3. **Applies priority chain** (higher priority sources override lower ones)
4. **Shows token sources** in debug mode

#### Debug Output Examples

Enable debug mode to see exactly where tokens are loaded from:

```bash
./nvcf-cli list functions --debug
```

**Example outputs:**

```
# Both tokens from config file
DEBUG: NVCF_API_KEY loaded from: config_file
DEBUG: NVCF_TOKEN loaded from: config_file

# API key from config, admin token from state
DEBUG: NVCF_API_KEY loaded from: config_file
DEBUG: NVCF_TOKEN loaded from: state
DEBUG: State function token expires: 2025-10-15 14:16:21

# Environment variable override
DEBUG: NVCF_API_KEY loaded from: config_file
DEBUG: NVCF_TOKEN loaded from: environment

# State file fallback with expiration info
DEBUG: NVCF_TOKEN loaded from: state
DEBUG: State function token expires: 2025-10-15 14:03:08
```

#### User Workflows

**Fresh Setup (Recommended)**
```bash
# 1. User gets CLI and runs init
#  Token generated and saved to state file
# DEBUG: Successfully generated admin token, expires: 2025-10-15 14:16:21

# 2. User runs commands - tokens work automatically
./nvcf-cli list functions --debug
#  DEBUG: NVCF_TOKEN loaded from: state
#  Uses token from state file seamlessly
```

**Advanced User Configuration**
```bash
# User has custom tokens in config file
echo "NVCF_TOKEN: my-custom-admin-token" >> ~/.nvcf-cli.yaml
echo "NVCF_API_KEY: my-custom-api-key" >> ~/.nvcf-cli.yaml

# User runs command
./nvcf-cli list functions --debug
#  DEBUG: NVCF_API_KEY loaded from: config_file
#  DEBUG: NVCF_TOKEN loaded from: config_file
```

**Temporary Override for Testing**
```bash
# User needs to test with different token temporarily
export NVCF_TOKEN="test-admin-token"
./nvcf-cli list functions --debug
#  DEBUG: NVCF_TOKEN loaded from: environment
#  DEBUG: NVCF_API_KEY loaded from: config_file
```

**Token Expiration Handling**
```bash
# State token expires, CLI automatically falls back
./nvcf-cli list functions --debug
#   DEBUG: NVCF_TOKEN loaded from: config_file
# (State token was expired and ignored)

# Generate fresh token
#  Fresh token generated and saved to state
```

#### Configuration File Roles

| File | Purpose | Contents | When Updated |
|------|---------|----------|--------------|
| **`.nvcf-cli.yaml`** | User configuration | Manual token overrides, endpoints, settings | User edits manually |
| **`.nvcf-cli.state`** | Runtime state | Auto-generated tokens, function context, expiration times | Updated by CLI commands (`init`, `create`, etc.) |

#### Benefits

 **Seamless `init` Workflow**: Generated tokens work immediately without manual copying
 **Clear Priority System**: Debug output shows exactly where tokens come from
 **Expiration Validation**: Expired state tokens are automatically ignored
 **Zero Breaking Changes**: Existing workflows continue working unchanged
 **Flexible Override**: Users can override any token source with higher priority sources

#### Best Practices

1. **Use `init` for fresh setups**: Let the CLI manage tokens automatically
2. **Override in config file for permanent changes**: Add custom tokens to `.nvcf-cli.yaml`
3. **Use environment variables for temporary testing**: Override without modifying files
4. **Enable debug mode when troubleshooting**: See exactly which token sources are used
5. **Don't manually edit `.nvcf-cli.state`**: This file is managed by the CLI

### Multi-Environment Configuration Support

The NVCF CLI supports multiple configuration environments, allowing you to maintain separate configurations for development, staging, and production environments.

#### Using the --config Flag

You can specify different configuration files using the `--config` flag:

```bash
# Use development configuration
./nvcf-cli --config dev.yaml init
./nvcf-cli --config dev.yaml create --input-file function.json

# Use production configuration
./nvcf-cli --config prod.yaml list functions
./nvcf-cli --config prod.yaml deploy --input-file deploy.json

# Use staging configuration
./nvcf-cli --config staging.yaml status
```

#### Automatic State File Separation

Each configuration context automatically maintains its own state file:

```bash
# Default config uses: ~/.nvcf-cli.state
./nvcf-cli status

# Dev config uses: ~/.nvcf-cli.dev.state
./nvcf-cli --config dev.yaml status

# Prod config uses: ~/.nvcf-cli.prod.state
./nvcf-cli --config prod.yaml status
```

#### Example Configuration Setup

**1. Create environment-specific config files:**

```bash
# Development configuration (dev.yaml)
cat > dev.yaml << 'EOF'
# Development Environment
nvcf_base_http_url: "http://dev-api.nvcf.example.com"
nvcf_client_id: "dev-account"
debug: true
EOF

# Production configuration (prod.yaml)
cat > prod.yaml << 'EOF'
# Production Environment
nvcf_base_http_url: "https://api.nvcf.nvidia.com"
nvcf_client_id: "prod-account"
debug: false
EOF
```

**2. Initialize each environment:**

```bash
# Set up development environment

# Set up production environment
```

**3. Work with different environments:**

```bash
# Development workflow
./nvcf-cli --config dev.yaml create --input-file test-function.json
./nvcf-cli --config dev.yaml deploy --input-file dev-deploy.json
./nvcf-cli --config dev.yaml invoke --input-file test-request.json

# Production workflow
./nvcf-cli --config prod.yaml list functions
./nvcf-cli --config prod.yaml deploy --input-file prod-deploy.json
```

#### Environment Isolation Benefits

- **Separate State**: Each environment maintains its own tokens, function context, and state
- **Isolated Operations**: Commands in dev environment don't affect production
- **Different Credentials**: Each environment can use different tokens and API keys
- **Environment-Specific Settings**: Different endpoints, debug levels, and configurations
- **Safe Testing**: Experiment in dev without risking production resources

#### Status Command Shows Current Environment

The status command displays which configuration file is being used:

```bash
# Default environment
./nvcf-cli status
# Shows: Config File: (default ~/.nvcf-cli.yaml)

# Development environment
./nvcf-cli --config dev.yaml status
# Shows: Config File: dev.yaml

# Production environment
./nvcf-cli --config prod.yaml status
# Shows: Config File: prod.yaml
```

#### Configuration Best Practices

1. **Use descriptive config names**: `dev.yaml`, `prod.yaml`, `staging.yaml`
2. **Keep sensitive data out of config files**: Use environment variables for secrets
3. **Version control config templates**: Commit `.yaml.template` files, not actual configs
4. **Document environment differences**: Note different endpoints, accounts, or settings
5. **Test config changes in dev first**: Validate configurations before using in production

## Registry Credential Management

Registry credentials allow NVCF to pull container images from private registries.

### 3. Add Registry Credentials

#### CLI Usage
```bash
# Add registry credentials (requires admin token)
./nvcf-cli registry-credential add \
  --name "dockerhub-creds" \
  --registry "docker.io" \
  --username "myusername" \
  --password "mypassword"
```

#### Method C: Using curl directly
```bash
# Add registry credentials
curl -X POST https://api.nvcf.nvidia.com/v2/nvcf/accounts/nvcf-default/registry-credentials \
  -H "Authorization: Bearer $NVCF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "dockerhub-creds",
    "registry": "docker.io",
    "username": "myusername",
    "password": "mypassword"
  }'
```

### List Registry Credentials

#### CLI Usage
```bash
./nvcf-cli registry-credential list
```

### List Recognized Registries

#### CLI Usage
```bash
./nvcf-cli registry-credential list-recognized
```

## Function Lifecycle

### 4. Create a Function

#### CLI Usage
```bash
# Create function (requires admin token)
./nvcf-cli create --file examples/create-function.json

# Create function with secrets using command-line flags
./nvcf-cli function create \
  --name my-function \
  --image nvcr.io/nvidia/pytorch:23.12-py3 \
  --inference-port 8000 \
  --inference-url /predict \
  --health-port 8000 \
  --health-uri /health \
  --secrets API_KEY=sk-1234567890abcdef,DATABASE_PASSWORD=mypassword

# Create function with multiple secrets
./nvcf-cli function create \
  --file examples/create-function.json \
  --secrets DB_HOST=db.example.com,DB_USER=admin,DB_PASS=secret123

# Create an LLM function model with routing metadata
./nvcf-cli function create \
  --name my-llm-function \
  --image nvcr.io/example/openai-compatible:latest \
  --inference-port 8000 \
  --inference-url / \
  --function-type LLM \
  --llm-model "name=dummy-model,uris=/v1/chat/completions|/v1/responses|/v1/embeddings,routingMethod=round_robin,tokenRateLimit=1000-S"
```

**Secrets Format:**
- Use `name=value` format: `--secrets KEY1=value1,KEY2=value2`
- Comma-separated for multiple secrets
- Values can be strings, and will be passed to your function container
- Secret values are encrypted at rest and masked in logs

#### Sample Function JSON (`examples/create-function.json`)
```json
{
  "name": "sample-pytorch-function",
  "image": "nvcr.io/nvidia/pytorch:23.12-py3",
  "inferenceUrl": "/predict",
  "inferencePort": 8000,
  "health": {
    "protocol": "HTTP",
    "port": 8000,
    "uri": "/health",
    "expectedStatusCode": 200
  },
  "models": [{
    "name": "pytorch-model",
    "uri": "s3://my-models/pytorch-model.pkl",
    "version": "1.0"
  }],
  "resources": {
    "gpu": 1,
    "memory": "8Gi",
    "cpu": "2"
  },
  "environment": {
    "MODEL_PATH": "/opt/model",
    "BATCH_SIZE": "32"
  }
}
```

#### LLM Function JSON
```json
{
  "name": "sample-llm-function",
  "containerImage": "nvcr.io/example/openai-compatible:latest",
  "inferenceUrl": "/",
  "inferencePort": 8000,
  "functionType": "LLM",
  "models": [
    {
      "name": "dummy-model",
      "llmConfig": {
        "uris": ["/v1/chat/completions", "/v1/responses", "/v1/embeddings"],
        "routingMethod": "round_robin",
        "tokenRateLimit": "1000-S"
      }
    }
  ]
}
```

For LLM models, `llmConfig.routingMethod` accepts the API/auth spellings
`round_robin`, `power_of_two`, `groq_multiregion`, `pulsar`, or `random`. The
CLI validates these values before sending the create request.
Supported LLM paths are `/v1/chat/completions`, `/v1/responses`, and `/v1/embeddings`.

### 5. List Functions

#### CLI Usage
```bash
# List functions (API key sufficient for user's functions, admin token for all)
./nvcf-cli list functions

./nvcf-cli list functions --details
```

### 6. Deploy a Function

#### CLI Usage
```bash
# Deploy function (requires admin token)
./nvcf-cli deploy --file examples/deploy-function.json
```

#### Sample Deployment JSON (`examples/deploy-function.json`)
```json
{
  "functionId": "550e8400-e29b-41d4-a716-446655440000",
  "versionId": "660e8400-e29b-41d4-a716-446655440001",
  "deploymentSpecifications": [{
    "gpu": "A100",
    "instanceType": "GCP.G2.LARGE",
    "maxInstances": 5,
    "minInstances": 1,
    "maxRequestConcurrency": 16,
    "configuration": {
      "scaling": {
        "targetUtilization": 70,
        "scaleUpTime": "30s",
        "scaleDownTime": "300s"
      }
    }
  }]
}
```

### 7. Manage Function Secrets

Functions often need access to sensitive configuration like API keys, database passwords, or credentials. The NVCF CLI provides commands to manage secrets for your deployed functions.

#### View Current Function Secrets

Display the secrets configured for the current function in state:

```bash
# Show secrets for current function (from create/deploy operations)
./nvcf-cli secrets show

# Show secrets
```

**Sample Output:**
```
Secrets configured for function 550e8400-e29b-41d4-a716-446655440000 version 660e8400-e29b-41d4-a716-446655440001:

1. API_KEY
2. DATABASE_PASSWORD
3. MODEL_CONFIG

Note: Secret values are not displayed for security reasons
To update secrets, use: nvcf-cli secrets update <name=value> [name=value...]
```

#### Update Function Secrets

Add or update secrets for the current function using NAME=VALUE pairs:

```bash
# Update a single secret
./nvcf-cli secrets update API_KEY=secret123

# Update multiple secrets at once
./nvcf-cli secrets update API_KEY=secret123 DB_PASSWORD=mypass ENV=production

# Update complex JSON values
./nvcf-cli secrets update CONFIG='{"timeout":30,"retries":3}'

# Update secrets
```

#### Prerequisites for Secret Management

- **Current Function Context**: Function must be created first (`nvcf-cli create`)
- **Admin Token**: Required for secret updates (`NVCF_TOKEN` from `nvcf-cli init`)
- **Function State**: The CLI uses the current function from state (Function ID and Version ID)

#### Important Notes

- **Security**: Secret values are masked in logs and never displayed in full
- **Admin Only**: Secret updates require admin privileges (not available with API keys alone)
- **State-Based**: Commands work with the function currently saved in CLI state
- **Immediate Effect**: Updated secrets are available to the function immediately
- **Cross-Account**: Uses admin token to work across NVIDIA Cloud Accounts

#### Error Cases

```bash
# No function in state
Error: no function found in state - run 'nvcf-cli create' first to set function context

# Missing admin token
Error: function secrets update requires admin token (NVCF_TOKEN) - run 'nvcf-cli init' first

# Invalid secret format
Error: invalid secret format 'invalid' - expected NAME=VALUE
```

### 8. Invoke a Function

#### CLI Usage
```bash
# HTTP invocation (API key required)
./nvcf-cli invoke --file examples/invoke-function.json

# gRPC invocation
./nvcf-cli invoke --grpc --file examples/invoke-function.json
```

Direct HTTP invocation routes through the function-specific invocation host, not function routing headers:

```bash
curl --request POST \
  --url "https://${FUNCTION_ID}.invocation.${INVOCATION_DOMAIN}/echo" \
  --header "Authorization: Bearer ${API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{"message": "hello"}'
```

LLM functions use the OpenAI-compatible LLM invocation route:

```bash
curl -sS -X POST "https://llm.invocation.${INVOCATION_DOMAIN}/v1/chat/completions" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"${FUNCTION_ID}/${MODEL_NAME}\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}"
```

Use the OpenAI `model` value `${FUNCTION_ID}/${MODEL_NAME}` for LLM invocation requests.

```bash
curl -sS -X POST "https://llm.invocation.${INVOCATION_DOMAIN}/v1/embeddings" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"${FUNCTION_ID}/${MODEL_NAME}\",\"input\":\"NVCF embeddings check\"}"
```

For LLM Gateway endpoint behavior, routing, and session stickiness details, see [LLM Gateway](../../../docs/user/llm-gateway.md).

#### Sample Invocation JSON (`examples/invoke-function.json`)
```json
{
  "functionId": "550e8400-e29b-41d4-a716-446655440000",
  "versionId": "660e8400-e29b-41d4-a716-446655440001",
  "input": {
    "messages": [
      {
        "role": "user",
        "content": "What is machine learning?"
      }
    ],
    "parameters": {
      "temperature": 0.7,
      "max_tokens": 100
    }
  }
}
```

## Additional Operations

## JSON Output Guidance

Use the global `--json` flag for automation. When set, the CLI writes a single JSON object to stdout and sends human-friendly logs to stderr.

### Commands with `--json` support (high value)

- **List commands**: `function list`, `function versions list`, `registry-credential list`, `registry-credential list-recognized`, `api-key list`
- **Get/detail commands**: `function get`, `function deploy get`, `registry-credential get`, `api-key show`, `status`

### Commands with `--json` support (optional)

- `function invoke`
- `registry-credential add`, `registry-credential update`, `registry-credential delete`
- `api-key generate`, `api-key delete`, `api-key revoke`, `api-key clear`, `api-key clear-all`

### Commands that generally don’t need `--json`

These are interactive or local-only commands where JSON adds little value:

- `help` / `version` / `completion`
- Commands that primarily print examples or guidance

### Suggested JSON shape

When `--json` is set:
- Emit a single JSON object to stdout
- Include `result` or `data` for the primary payload
- Include `warnings` or `errors` arrays if relevant
- Avoid mixed human text + JSON in the same output (logs go to stderr when `--json` is set)

### Get Queue Details
```bash
# Using

# Using direct API calls
./nvcf-cli queue --function-id "550e8400-e29b-41d4-a716-446655440000"
```

### Update Function
```bash
./nvcf-cli function update --input-file examples/update-metadata.json
./nvcf-cli function update \
  --function-id "550e8400-e29b-41d4-a716-446655440000" \
  --version-id "01234567-89ab-cdef-0123-456789abcdef" \
  --llm-model-update "name=dummy-model,routingMethod=round_robin,tokenRateLimit=1000-S"
```

### Delete Function or Deployment

The delete command offers flexible ways to specify which function to delete, with automatic fallback to the current function in state.

#### Flexible Function/Version ID Resolution

The delete command resolves Function ID and Version ID in the following priority order:

1. **Explicit arguments**: `delete <function-id> <version-id>`
2. **CLI flags**: `--function-id` and `--version-id`
3. **JSON file**: `--input-file` with `functionId` and `versionId`
4. **Current state**: Uses function from `nvcf-cli create` (automatic)

#### Examples

```bash
# Method 1: Delete current function from state (easiest)
./nvcf-cli delete

# Method 2: Delete specific function by arguments
./nvcf-cli delete func-123 ver-456

# Method 3: Delete specific function by flags
./nvcf-cli delete --function-id func-123 --version-id ver-456

# Method 4: Delete using JSON file
./nvcf-cli delete --input-file examples/delete-function.json

# Delete only deployment (keep function)
./nvcf-cli delete --deployment-only

# Graceful deployment deletion (let current tasks complete)
./nvcf-cli delete --deployment-only --graceful
```

#### Smart State Management

When you delete the current function (the one saved in CLI state), the CLI automatically:

- **Detects Match**: Compares the function being deleted with the current state
- **Clears State**: Removes the function context from state if it matches
- **Updates Status**: The next `nvcf-cli status` will show "No function selected"
- **Preserves Other State**: Tokens and other settings remain unchanged

#### Benefits of State-Based Delete

- **Zero Configuration**: Just run `nvcf-cli delete` after creating/deploying a function
- **No Copy/Paste**: No need to remember or look up Function IDs
- **Safe Defaults**: Only deletes what you're currently working with
- **Flexible Override**: You can still specify different functions when needed

#### Error Cases

```bash
# No function specified and no state
Error: no function specified and no current function in state - provide function ID and version ID, or run 'nvcf-cli create' first

# Missing admin token
Error: NVCF_TOKEN is required for delete operations (set in environment variable or config file)
```

## Troubleshooting

### Common Issues

#### 1. Authentication Errors
```bash
# Check what tokens are loaded and their sources
# Look for: "DEBUG: NVCF_TOKEN loaded from: state/config_file/environment"

# Check API key validity

# Regenerate admin token if expired
```

#### 2. Connection Issues
```bash
# Test connectivity (direct API calls)
curl -H "Authorization: Bearer $NVCF_API_KEY" https://api.nvcf.nvidia.com/v2/nvcf/functions

# Test connectivity
```

#### 3. Registry Issues
```bash
# Test registry credentials

# Add debug logging
```

#### 4. Debug Mode
Enable debug mode for detailed logging:
```bash
# Add --debug flag to any command

# Or set in config file
echo "debug: true" >> ~/.nvcf-cli.yaml
```

### Configuration Priority

The CLI uses the following priority order for configuration values:

#### Authentication Tokens and Configuration Values
1. **Environment variables** (`NVCF_TOKEN`, `NVCF_API_KEY`, etc.) - **Highest priority**
   - Explicit user override for any specific run
   - Example: `NVCF_TOKEN=xyz ./nvcf-cli list functions`

2. **State file** (`~/.nvcf-cli.state`) - **Dynamic values**
   - Auto-generated by `init` and `api-key generate` commands
   - Contains fresh tokens with expiration tracking
   - Automatically updated when tokens are regenerated
   - Takes precedence over static config file

3. **Configuration file** (`~/.nvcf-cli.yaml`) - **Static configuration**
   - User-managed persistent configuration
   - Fallback when no fresher values exist
   - Good for endpoints, defaults, and manual token configuration

4. **Default values** - **Lowest priority**
   - Built-in fallback values
   - Used when no other source provides a value

#### Command-Line Flags

#### How It Works

**Example workflow:**
```bash
# 1. Set static config (lowest priority for tokens)
echo "NVCF_TOKEN: old-token-123" > ~/.nvcf-cli.yaml

# 2. Generate fresh admin token (saved to state file)
# Creates ~/.nvcf-cli.state with fresh token

# 3. CLI automatically uses state file token (higher priority than config file)
./nvcf-cli list functions
# Uses token from state file, not config file

# 4. Environment variable overrides everything
NVCF_TOKEN=special-token ./nvcf-cli list functions
# Uses special-token from environment
```

#### Debug Output

Enable `--debug` to see which source is being used:
```bash
./nvcf-cli list functions --debug
# Output:
# DEBUG: NVCF_TOKEN loaded from: state
# DEBUG: State function token expires: 2025-10-16 12:08:16
# DEBUG: NVCF_API_KEY loaded from: config_file
```

**Token source indicators:**
- `environment` - From environment variable (highest priority)
- `state` - From state file (auto-generated, fresh tokens)
- `config_file` - From ~/.nvcf-cli.yaml (static configuration)
- `none` - No token found

#### Best Practices

1. **Use `init` for admin tokens**: Run `./nvcf-cli init` to generate fresh admin tokens that are automatically managed in the state file
2. **Use `api-key generate` for API keys**: Generated keys are saved to state file with expiration tracking
3. **Keep config file minimal**: Only put static configuration (endpoints, preferences) in `~/.nvcf-cli.yaml`
4. **Let state file manage tokens**: The CLI will automatically use the freshest available tokens
5. **Use env vars for overrides**: When you need to temporarily use a different token, use environment variables

### Mode Selection

### Token vs API Key Usage

- **Admin Token (`NVCF_TOKEN`)**: Required for:
  - Function registration/creation
  - Registry credential management
  - Admin operations (list all functions, manage deployments)

- **API Key (`NVCF_API_KEY`)**: Sufficient for:
  - Function invocation
  - Listing user's own functions
  - Queue operations
  - General user operations
  - **User-generated**: Use `./nvcf-cli api-key generate` to create with custom scopes and expiration

#### Recommended Workflow

1. **Start with `init`**: Generate admin token automatically
   ```bash
   #  Admin token saved to state file, ready to use
   ```

2. **Generate API key for applications**: Create scoped API keys for specific use cases
   ```bash
   #  API key created with appropriate scopes
   ```

3. **Use debug mode**: See which token source is being used
   ```bash
   ./nvcf-cli list functions --debug
   #  Shows: "DEBUG: NVCF_TOKEN loaded from: state"
   ```

Choose the authentication method based on the operations you need to perform. The smart integration ensures tokens are used automatically once generated.

## Cloud Tasks (NVCT)

NVCT exposes a separate API surface for GPU-backed batch jobs. The CLI wraps
it under `nvcf-cli task ...`, reusing the same authentication, state, config,
and `--json` output conventions as `nvcf-cli function`.

### NVCT Authentication

Task commands use an NVCT-scoped API key. `nvcf-cli api-key generate` mints
both the function and task keys by default:

```bash
nvcf-cli api-key generate
```

The task key is saved as `NVCF_NVCT_API_KEY` and used automatically for all
`nvcf-cli task` subcommands. Use `--for task` if you only want the task key.

### NVCT Endpoint Configuration

NVCT uses a dedicated base URL. Configure it via any of:

```yaml
# ~/.nvcf-cli.yaml
base_nvct_url: https://api.nvct.nvidia.com         # production (default)
# base_nvct_url: https://stg.api.nvct.nvidia.com   # staging
```

```bash
# One-shot override via env var
export NVCF_BASE_NVCT_URL=https://stg.api.nvct.nvidia.com
nvcf-cli task list
```

### Subcommands at a Glance

| Subcommand | Endpoint |
| --- | --- |
| `task create` | `POST /v1/nvct/tasks` |
| `task list` | `GET /v1/nvct/tasks` |
| `task bulk` | `POST /v1/nvct/tasks/bulk` |
| `task get [taskId]` | `GET /v1/nvct/tasks/{taskId}` |
| `task delete [taskId]` | `DELETE /v1/nvct/tasks/{taskId}` |
| `task cancel [taskId]` | `POST /v1/nvct/tasks/{taskId}/cancel` |
| `task events [taskId]` | `GET /v1/nvct/tasks/{taskId}/events` |
| `task results [taskId]` | `GET /v1/nvct/tasks/{taskId}/results` |
| `task update-secrets [taskId]` | `PUT /v1/nvct/secrets/tasks/{taskId}` |

### Create a Task

```bash
# JSON spec (recommended)
nvcf-cli task create --input-file examples/create-task.json

# Inline flags
nvcf-cli task create \
  --name my-job \
  --gpu H100 --instance-type GPU.H100_1x --backend GFN \
  --image nvcr.io/0651155215864979/ncp-dev/example-trainer:0.1.0 \
  --container-args "--epochs 10" \
  --container-env LOG_LEVEL=INFO \
  --tags example,demo \
  --max-runtime PT4H \
  --result-strategy NONE
```

`task create` saves the resulting task ID to the state file, so subsequent
`get`/`events`/`results`/`cancel`/`delete`/`update-secrets` commands can omit
the positional task ID and reuse the saved context.

### Required vs Optional Fields

`name`, `gpuSpecification.gpu`, and `gpuSpecification.instanceType` are
mandatory. When `resultHandlingStrategy` is set to `UPLOAD`, you must also
supply:

- `resultsLocation` in the form `<org>/[<team>/]<model>` (an NGC Model Registry
  path), and
- an `NGC_API_KEY` secret with write privileges to that location.

See [`examples/create-task-with-results.json`](./examples/create-task-with-results.json)
for a fully populated example.

### Listing and Filtering

```bash
nvcf-cli task list                              # all tasks (server default page)
nvcf-cli task list --status RUNNING --limit 50  # filter by status
nvcf-cli task list --cursor "$CURSOR"           # next page via cursor
```

Status enum: `QUEUED`, `LAUNCHED`, `RUNNING`, `ERRORED`, `CANCELED`,
`EXCEEDED_MAX_RUNTIME_DURATION`, `EXCEEDED_MAX_QUEUED_DURATION`, `COMPLETED`.

For an explicit set of IDs, use the bulk endpoint (returns `id`, `name`,
`status` only):

```bash
nvcf-cli task bulk --task-ids id1,id2,id3
nvcf-cli task bulk --input-file examples/bulk-tasks.json
```

### Watching Events and Results

```bash
nvcf-cli task events --limit 100       # latest events for current task
nvcf-cli task results                  # results emitted (UPLOAD strategy)
nvcf-cli --json task events > events.json   # automation-friendly output
```

### Rotating Secrets

```bash
# CLI flag style
nvcf-cli task update-secrets --secrets NGC_API_KEY=nvapi-... HF_TOKEN=hf_...

# JSON file style (lets you pass non-string values)
nvcf-cli task update-secrets --input-file examples/update-task-secrets.json
```

The PUT merges the supplied secrets into the existing set: provided secrets are added or updated by name, and existing secrets not included in the request are preserved.

### Cancel and Delete

```bash
nvcf-cli task cancel    # POST /cancel - runs to graceful shutdown
nvcf-cli task delete    # DELETE - permanent
```

`task delete` clears the saved task from state when it matches.

