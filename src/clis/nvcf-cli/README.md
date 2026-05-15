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
# NVIDIA Cloud Functions CLI (Go)

**Enhanced with automatic token generation, state management, and multi-environment support!**

## Key Features

- **Automatic Token Generation**: Generate admin tokens and API keys via direct API calls (no kubectl needed!)
- **Smart State Management**: Eliminates manual ID copying with persistent workflow context  
- **Multi-Environment Support**: Separate configurations for dev/staging/production
- **Enhanced User Experience**: Colored output, status dashboard, and helpful guidance
- **Advanced gRPC Support**: Native gRPC invocation with `--grpc` flag
- **Comprehensive Authentication**: Multi-token support with automatic scope management
- **Simple Architecture**: Everything works via direct HTTPS - no cluster access required

**[Jump to Token Generation Guide](#automatic-token-generation-)**

---

## Prerequisites

- Bazel (managed via Bazelisk; see `BAZEL.md` at the repo root for setup)
- Git
- Valid NVIDIA Cloud Functions credentials

## Installation

### Option 1: Build from Source

1. Clone the repository:
```bash
git clone <repository-url>
cd nvcf
```

2. Build the binary (host platform):
```bash
bazel build //src/clis/nvcf-cli:nvcf-cli
```

The binary is at `bazel-bin/src/clis/nvcf-cli/nvcf-cli_/nvcf-cli`.

3. (Optional) Install globally by copying it onto your `PATH`:
```bash
install -m 0755 \
  bazel-bin/src/clis/nvcf-cli/nvcf-cli_/nvcf-cli \
  /usr/local/bin/nvcf-cli
```

### Option 2: Download Pre-built Binary

Pre-built binaries for multiple platforms are available in the [releases section](../../releases).

## Release Process

Releases are driven by Git tags and the GitLab CI pipeline.

1. Create and push a version tag (for example `v0.0.21`):
```bash
git tag v0.0.21
git push origin v0.0.21
```

2. GitLab CI runs the release pipeline, builds binaries, and prepares archives.

3. After QA approval, manually trigger the `ngc-push-all-platforms` job in GitLab to push to `nvcf-onprem/nvcf-ncp-staging`.

Notes:
- The release pipeline only runs on tags.
- The NGC push to staging is manual and gated by QA approval.

## Configuration


### **Configuration File**

The CLI looks for configuration files in this order:
1. **Explicit path** via `--config` flag (highest priority)
2. **Current directory**: `./.nvcf-cli.yaml`
3. **Home directory**: `~/.nvcf-cli.yaml`

Use the provided template to get started:
```bash
# Copy template and customize
cp .nvcf-cli.yaml.template .nvcf-cli.yaml
# Edit with your settings
vi .nvcf-cli.yaml
```

Example configuration (`.nvcf-cli.yaml`):
```yaml
# API Endpoints
base_http_url: https://api.nvcf.nvidia.com
invoke_url: https://api.nvcf.nvidia.com
grpc_url: grpc.nvcf.nvidia.com:443

# Authentication (can also use environment variables)
# api_key: nvapi-your-api-key
# token: your-jwt-token

# Settings
default_timeout: 300
debug: false

# For staging environment
# base_http_url: https://api.shqa.stg.nvcf.nvidia.com
# invoke_url: https://invocation.shqa.stg.nvcf.nvidia.com
# grpc_url: grpc.shqa.stg.nvcf.nvidia.com:443
```

See `.nvcf-cli.yaml.template` for comprehensive configuration options and documentation.

### **Configuration Priority**

Values are read in the following order (highest to lowest priority):

1. **Command-line flags** (highest priority)
2. **Environment variables**
3. **Config file in current directory** (`./.nvcf-cli.yaml`)
4. **Config file in home directory** (`~/.nvcf-cli.yaml`)
5. **Default values** (lowest priority)

## Usage

### **HTTP Debugging**

Enable comprehensive HTTP request/response logging for troubleshooting:

```bash
# Method 1: Command line flag
./nvcf-cli create --debug --name test --image nginx --inference-url http://test --inference-port 8000

# Method 2: Environment variable
export NVCF_DEBUG=true

# Method 3: Configuration file
echo 'debug: true' >> ~/.nvcf-cli.yaml
```

**Debug Output Example:**
```
DEBUG: HTTP debugging enabled with multi-token support
DEBUG: API key available: true
DEBUG: Function token available: true
DEBUG: Using FUNCTION TOKEN for POST /v2/nvcf/accounts/nvcf-default/functions

DEBUG: HTTP Request
---
Method: POST
URL: https://api.nvcf.nvidia.com/v2/nvcf/accounts/nvcf-default/functions
Headers:
  Content-Type: application/json
  Accept: application/json
  Authorization: [REDACTED]
Request Body:
{
  "name": "test",
  "containerImage": "nginx",
  "inferenceUrl": "http://test",
  "inferencePort": 8000
}
---
DEBUG: HTTP Response
---
Status: 200 OK
Headers:
  Content-Type: application/json
Response Body:
{
  "function": {
    "id": "func-123",
    "versionId": "ver-456"
  }
}
---
```

### **JSON Configuration Files**

All commands support JSON configuration files with CLI override capability:


**Usage:**
```bash
# Use JSON file only
./nvcf-cli create --input-file examples/create-function.json

# Combine JSON file with CLI overrides
./nvcf-cli deploy --input-file examples/deploy-function.json --timeout 1200

# CLI flags override JSON file values
./nvcf-cli invoke --input-file examples/invoke-function.json --timeout 30
```

---

## **Authentication & Token Management**

The NVCF CLI supports advanced authentication management with automatic token generation and comprehensive state management.

### **Authentication Methods**

The CLI supports multiple authentication methods:

1. **Manual Token Configuration** (traditional method)
2. **Automatic Token Generation** (recommended for cluster environments)
3. **Multi-Environment Support** (development, staging, production)

### **Token Types**

- **Admin Token (`NVCF_TOKEN`)**: For function management operations (create, deploy, delete, update)
- **API Key (`NVCF_API_KEY`)**: For function operations (list, invoke, queue details)

---

## Automatic Token Generation

### **Prerequisites for Token Generation**

Token generation is simple - no cluster access needed!

Requirements:
- Network access to the API Keys service endpoint
- Configuration file (`.nvcf-cli.yaml`) with the API Keys service URL (optional)

### **Generate Admin Token**

Generate a fresh admin token via direct API call:

```bash
# Generate initial admin token (clears existing state)
nvcf-cli init

# With custom API Keys service URL
API_KEYS_SERVICE_URL=https://api-keys.shqa.stg.nvcf.nvidia.com nvcf-cli init

# With debug output
nvcf-cli --debug init

# Example output:
# [INFO] Starting fresh session...
# [INFO] Generating admin token from API Keys service...
# [SUCCESS] Admin token generated and saved
# Token: eyJhbGciOiJFUzI1NiIs...
# Expires: 2025-11-19 06:08:15
```

**What `init` does:**
- Calls the API Keys service endpoint (`/v1/admin/keys`)
- Generates JWT admin token for NVCF API operations
- **Clears any existing function state** (fresh start)
- Saves token with expiration tracking to `~/.nvcf-cli-state.json`

### **Refresh Admin Token**

Refresh your admin token while preserving function context:

```bash
# Refresh token (keeps current function state)
nvcf-cli refresh

# Example output:
# [INFO] Refreshing admin token (keeping function state)...
# [SUCCESS] Admin token refreshed
# New Token: eyJhbGciOiJFUzI1NiIs...
# Function ID: func-abc123
# Version ID: ver-def456
```

**What `refresh` does:**
- Generates new admin token via API call
- **Preserves current function context** (ID, version, name)
- Updates token expiration tracking
- Maintains workflow continuity

### **Generate API Key**

Generate API keys for function invocation and listing:

```bash
# Generate API key with defaults (24h expiration)
nvcf-cli api-key generate

# Custom expiration and description
nvcf-cli api-key generate --expires-in 48h --description "Production API key"

# Generate and validate the key
nvcf-cli api-key generate --validate

# Example output:
# [INFO] Generating API key...
# [INFO] Description: Generated by nvcf-cli
# [INFO] Expires: 2025-11-19 15:30:00 PDT
# [SUCCESS] API key generated successfully!
# API Key: nvapi-nvcf-stg-DsIa_igIRtkMlquoB7AzGi3lPHxm2pvuB9yK4tnzHLUaEM4...
# Expires: 2025-11-19 15:30:00 PDT
```

**API Key Features:**
- Automatic scope configuration (invoke_function, list_functions, queue_details)
- Configurable expiration (default 24h)
- Optional validation after generation
- Automatic state management and persistence

---

## **Multi-Environment Configuration**

### **Environment-Specific Configs**

Use different configurations for different environments:

```bash
# Development environment
nvcf-cli --config dev.yaml init
nvcf-cli --config dev.yaml create --input-file function.json

# Production environment  
nvcf-cli --config prod.yaml list functions
nvcf-cli --config prod.yaml invoke --request-body '{"input": "test"}'

# Staging environment
nvcf-cli --config staging.yaml init
nvcf-cli --config staging.yaml function list
```

### **Example Configuration Files**

**Development (`dev.yaml`):**
```yaml
# Enable debug logging for development
debug: true

# API Endpoints for development environment
base_http_url: https://api-dev.nvcf.nvidia.com
invoke_url: https://invocation-dev.nvcf.nvidia.com
grpc_url: grpc-dev.nvcf.nvidia.com:443

# Development API Keys service configuration
api_keys_service_url: https://api-keys-dev.nvcf.nvidia.com
```

**Production (`prod.yaml`):**
```yaml
# Disable debug in production
debug: false

# Direct API endpoints for production
NVCF_BASE_HTTP_URL: "https://api.nvcf.nvidia.com"
NVCF_BASE_GRPC_URL: "grpc.nvcf.nvidia.com:443"
NVCF_INVOKE_URL: "https://invocation.nvcf.nvidia.com"

# Set your production credentials
NVCF_API_KEY: "nvapi-your-production-key-here"
```

### **Separate State Management**

Each configuration maintains separate state:

```bash
# Different configs = different state files
~/.nvcf-cli.state          # Default config
~/.nvcf-cli.dev.state      # Dev config (--config dev.yaml)
~/.nvcf-cli.prod.state     # Prod config (--config prod.yaml)
```

---

## **State Management & Workflow**

### **Persistent State**

The CLI automatically manages state to eliminate manual ID copying:

```bash
# Create function (automatically saves context)
nvcf-cli create --input-file function.json
# Function ID: func-abc123 (saved automatically)
# Version ID: ver-def456 (saved automatically)

# Deploy using saved context (no IDs needed!)
nvcf-cli deploy

# Invoke using saved context
nvcf-cli invoke --request-body '{"input": "test"}'

# Check current state
nvcf-cli status
```

### **Status Command**

Get comprehensive CLI state information:

```bash
nvcf-cli status

# Example output:
# [INFO] NVCF CLI Status
# ==================================================
# 
# Configuration:
#    Config File: (default ~/.nvcf-cli.yaml)
#    API Endpoint: https://api.nvcf.nvidia.com
#    API Keys Service: https://api-keys.nvcf.nvidia.com
# 
# Authentication:
#    Admin Token: eyJhbGci***...***dH__uqEbEg [Valid]
#    Token Expires: 2025-10-14 12:48:06 PDT
#    API Key: nvapi-nvcf***...***hDNqjzaeSQ [Valid]
#    API Key Expires: 2025-10-14 13:00:26 PDT
# 
# Current Function:
#    Function ID: func-abc123
#    Version ID: ver-def456
#    Name: my-test-function
#    Status: Ready for operations
# 
# Quick Actions:
#    • nvcf-cli deploy            (deploy current function)
#    • nvcf-cli invoke            (invoke current function)
#    • nvcf-cli undeploy          (undeploy current function)
```

### **Complete Workflow Example**

```bash
# 1. Initialize with fresh admin token
nvcf-cli init

# 2. Generate API key for invocations
nvcf-cli api-key generate --description "Demo key"

# 3. Create function (context automatically saved)
nvcf-cli create --input-file examples/create-function.json

# 4. Deploy function (uses saved context automatically)
nvcf-cli deploy

# 5. Invoke function (uses saved context and API key automatically)
nvcf-cli invoke --request-body '{"message": "hello world"}'

# 6. Check everything is working
nvcf-cli status

# 7. Clean up when done
nvcf-cli undeploy  # New undeploy command!
nvcf-cli delete    # Uses saved context
```

---

## **Command Reference**

### **Authentication & Management Commands**

#### **Initialize CLI (Generate Admin Token)**

Generate a fresh admin token and start a new session:

```bash
# Initialize - generate admin token
nvcf-cli init

# Output example:
# [INFO] Starting fresh session...
# [INFO] Generating admin token from API Keys service...
# [SUCCESS] Admin token generated and saved
```

**Key Features:**
- Clears existing function state for fresh start
- Calls API Keys service directly (`/v1/admin/keys`)
- Saves token with expiration tracking to `~/.nvcf-cli-state.json`
- No kubectl or cluster access needed

#### **Refresh Admin Token**

Refresh your admin token while keeping function context:

```bash
# Refresh token (preserves current function)
nvcf-cli refresh

# Output example:
# [INFO] Refreshing admin token (keeping function state)...
# [SUCCESS] Admin token refreshed
# Function ID: func-abc123  (preserved)
```

**Key Features:**
- Generates new admin token
- Preserves current function context
- Updates expiration tracking
- Maintains workflow continuity

#### **Generate API Key**

Create API keys for function operations:

```bash
# Generate with defaults (24h expiration)
nvcf-cli api-key generate

# Custom configuration
nvcf-cli api-key generate \
  --description "Production API key" \
  --expires-in 48h \
  --validate

# Output example:
# [SUCCESS] API key generated successfully!
# API Key: nvapi-nvcf-stg-DsIa_igIRtkMlquoB7AzGi3l...
```

**Options:**
- `--description`: Custom description (default: "Generated by nvcf-cli")
- `--expires-in`: Expiration duration (default: "24h")
- `--validate`: Validate the key after generation

#### **Check CLI Status**

Display comprehensive CLI state and configuration:

```bash
nvcf-cli status

# Show full tokens (for debugging only)
nvcf-cli status --show-tokens
```

**Displays:**
- Configuration details (config file, API endpoints)
- Authentication status (tokens, expiration, validity)
- Current function context (ID, version, name)
- API endpoints and account information
- Quick action suggestions

#### **Undeploy Function**

Remove function deployment while keeping function definition:

```bash
# Undeploy current function (from state)
nvcf-cli undeploy

# Undeploy specific function
nvcf-cli undeploy --function-id func-123 --version-id ver-456
```

**Key Features:**
- Uses saved function context automatically
- Supports both cluster and direct modes
- Function definition remains (can be redeployed)

---

### **Function Management Commands**

#### **Create a Function** *Uses `NVCF_TOKEN`*

Create a new function with comprehensive configuration:

```bash
# Set the required token
export NVCF_TOKEN="nvapi-your-function-creation-token"

# Create function with CLI flags
./nvcf-cli create \
  --name "my-function" \
  --image "nvcr.io/0651155215864979/ncp-dev/load_tester_supreme:0.0.8" \
  --inference-url "/echo" \
  --inference-port 8000 \
  --description "My test function" \
  --tags "test,demo" \
  --health-uri "/health" \
  --health-protocol "HTTP" \
  --health-port 8000 \
  --health-timeout "PT30S" \
  --health-expected-status 200 \
  --function-type "DEFAULT" \
  --container-env "MODEL_PATH=/models" \
  --container-env "BATCH_SIZE=32" \
  --secrets "api-key=sk-12345,db-password=mypassword"

# Or create with JSON configuration
./nvcf-cli create --input-file examples/create-function.json

# Create an LLM function model with a routing method override
./nvcf-cli function create \
  --name "my-llm-function" \
  --image "nvcr.io/example/openai-compatible:latest" \
  --inference-url "/" \
  --inference-port 8000 \
  --function-type "LLM" \
  --llm-model "name=dummy-model,uris=/v1/chat/completions|/v1/responses,routingMethod=round_robin,tokenRateLimit=1000-M"
```

**Required flags:**
- `--name`: Function name
- `--image`: Container image 
- `--inference-url`: Inference URL endpoint
- `--inference-port`: Port number for inference

**Example JSON file (`create-function.json`):**
```json
{
  "name": "sample-llm-function",
  "containerImage": "nvcr.io/example/openai-compatible:latest",
  "inferenceUrl": "/",
  "inferencePort": 8000,
  "functionType": "LLM",
  "description": "Example LLM function from JSON config",
  "tags": ["example", "demo"],
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
  "models": [
    {
      "name": "dummy-model",
      "llmConfig": {
        "uris": ["/v1/chat/completions", "/v1/responses"],
        "routingMethod": "round_robin",
        "tokenRateLimit": "1000-M"
      }
    }
  ],
  "secrets": [
    {"name": "api-key", "value": "sk-12345"},
    {"name": "db-password", "value": "mypassword"}
  ]
}
```

`--llm-model` accepts `name`, `uris`, `routingMethod`, and `tokenRateLimit`
key/value fields. Separate multiple URIs with `|`. Valid routing
methods are `round_robin`, `power_of_two`, and `random`; the CLI validates and
sends these API/auth spellings in the create request.

#### **Deploy a Function** *Uses `NVCF_TOKEN` (with `NVCF_API_KEY` fallback)*

Deploy a function with GPU specifications:

```bash
# Set the required tokens  
export NVCF_TOKEN="nvapi-your-function-creation-token"
export NVCF_API_KEY="nvapi-your-general-operations-token"  # optional fallback

# Deploy with CLI flags
./nvcf-cli deploy \
  --function-id "func-12345678-1234-1234-1234-123456789abc" \
  --version-id "ver-12345678-1234-1234-1234-123456789abc" \
  --instance-type "ON-PREM.GPU.H100_1x" \
  --gpu "H100" \
  --min-instances 1 \
  --max-instances 1 \
  --timeout 900

# Or deploy with JSON configuration
./nvcf-cli deploy --input-file deploy.json
```

**New JSON format (deploymentSpecifications):**
```json
{
  "deploymentSpecifications": [
    {
      "gpu": "H100",
      "maxInstances": 1,
      "minInstances": 1,
      "instanceType": "ON-PREM.GPU.H100_1x"
    }
  ]
}
```

**Legacy flat format (still supported):**
```json
{
  "functionId": "func-12345678-1234-1234-1234-123456789abc",
  "versionId": "ver-12345678-1234-1234-1234-123456789abc",
  "instanceType": "ON-PREM.GPU.H100_1x",
  "gpu": "H100",
  "minInstances": 1,
  "maxInstances": 1,
  "backend": "GFN",
  "timeout": 900
}
```

#### **Update a Function** *Uses `NVCF_TOKEN` (with `NVCF_API_KEY` fallback)*

Update various aspects of an existing function:

```bash
# Set the required tokens  
export NVCF_TOKEN="nvapi-your-function-creation-token"
export NVCF_API_KEY="nvapi-your-general-operations-token"  # optional fallback

# Update function metadata (tags, description)
./nvcf-cli update metadata \
  --function-id "func-12345678-1234-1234-1234-123456789abc" \
  --version-id "ver-12345678-1234-1234-1234-123456789abc" \
  --description "Updated function description" \
  --tags "production,ml-model,updated"

# Update function deployment specifications
./nvcf-cli update deployment \
  --function-id "func-12345678-1234-1234-1234-123456789abc" \
  --version-id "ver-12345678-1234-1234-1234-123456789abc" \
  --min-instances 2 \
  --max-instances 5 \
  --max-request-concurrency 20

# Or update with JSON configuration
./nvcf-cli update metadata --input-file update-metadata.json
./nvcf-cli update deployment --input-file update-deployment.json
```

**Update Metadata JSON format (`update-metadata.json`):**
```json
{
  "functionId": "func-12345678-1234-1234-1234-123456789abc",
  "versionId": "ver-12345678-1234-1234-1234-123456789abc",
  "description": "Updated function description",
  "tags": ["production", "ml-model", "updated"]
}
```

**Update Deployment JSON format (`update-deployment.json`):**
```json
{
  "functionId": "func-12345678-1234-1234-1234-123456789abc",
  "versionId": "ver-12345678-1234-1234-1234-123456789abc",
  "minInstances": 2,
  "maxInstances": 5,
  "maxRequestConcurrency": 20,
  "clusters": ["cluster-1", "cluster-2"],
  "availabilityZones": ["us-west-2a", "us-west-2b"]
}
```

**Update Command Features:**
- **Metadata Updates**: Change function description and tags without affecting code or deployment
- **Deployment Updates**: Modify instance counts, concurrency, clusters, and other deployment settings
- **Non-destructive**: Updates preserve existing function code and configuration
- **Note**: GPU type and backend configurations cannot be modified through update operations

#### **Invoke a Function** *Uses `NVCF_API_KEY`*

Execute a function with JSON payload using HTTP or gRPC:

```bash
# NEW: Invoke using saved context (no IDs needed!)
nvcf-cli invoke --request-body '{"input": "Hello, World!"}'

# NEW: gRPC invocation
nvcf-cli invoke --grpc --request-body '{"input": "test"}'

# Traditional: Invoke with explicit IDs
./nvcf-cli invoke \
  --function-id "func-12345678-1234-1234-1234-123456789abc" \
  --version-id "ver-12345678-1234-1234-1234-123456789abc" \
  --request-body '{"input": "Hello, World!", "parameters": {"temperature": 0.7}}' \
  --timeout 60 \
  --poll-duration 5

# Or invoke with JSON configuration
./nvcf-cli invoke --input-file invoke.json
```

Direct HTTP invocation routes through the function-specific invocation host, not function routing headers:

```bash
curl --request POST \
  --url "https://${FUNCTION_ID}.invocation.${INVOCATION_DOMAIN}/echo" \
  --header "Authorization: Bearer ${API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{"message": "hello"}'
```

For LLM functions, send OpenAI-compatible requests to the LLM invocation host:

```bash
curl -sS -X POST "https://llm.invocation.${INVOCATION_DOMAIN}/v1/chat/completions" \
  -H "Authorization: Bearer ${NVCF_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"${FUNCTION_ID}/${MODEL_NAME}\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}]}"
```

The OpenAI `model` value must use `${FUNCTION_ID}/${MODEL_NAME}`.

**New Features:**
- **Smart Context**: Uses saved function ID/version automatically
- **gRPC Support**: `--grpc` flag for cluster-mode invocation
- **Enhanced Response**: Colored output and better error messages
- **Multi-Mode**: HTTP (default) and gRPC protocols supported

**Example JSON file (`invoke.json`):**
```json
{
  "functionId": "func-12345678-1234-1234-1234-123456789abc",
  "versionId": "ver-12345678-1234-1234-1234-123456789abc",
  "requestBody": {
    "input": "Hello, World!",
    "parameters": {
      "temperature": 0.7,
      "max_tokens": 100
    }
  },
  "timeout": 120,
  "pollDurationSeconds": 2
}
```

`pollDurationSeconds` maps to the `NVCF-POLL-SECONDS` hold-open hint. The service may keep the invocation connection open for that duration before returning pending request metadata.

#### **Delete a Function** *Uses `NVCF_TOKEN` ONLY*

Delete a function version or deployment:

```bash
# Set the REQUIRED token (no fallback)
export NVCF_TOKEN="nvapi-your-function-creation-token"

# Delete entire function
./nvcf-cli delete \
  --function-id "func-12345678-1234-1234-1234-123456789abc" \
  --version-id "ver-12345678-1234-1234-1234-123456789abc"

# Delete deployment only (keep function)
./nvcf-cli delete \
  --function-id "func-12345678-1234-1234-1234-123456789abc" \
  --version-id "ver-12345678-1234-1234-1234-123456789abc" \
  --deployment-only \
  --graceful

# Or delete with JSON configuration
./nvcf-cli delete --input-file delete-deployment.json
```

**Example JSON file (`delete-deployment.json`):**
```json
{
  "functionId": "func-12345678-1234-1234-1234-123456789abc",
  "versionId": "ver-12345678-1234-1234-1234-123456789abc",
  "graceful": true,
  "deleteDeploymentOnly": true
}
```

**Example JSON file (`delete-function.json`):**
```json
{
  "functionId": "func-12345678-1234-1234-1234-123456789abc",
  "versionId": "ver-12345678-1234-1234-1234-123456789abc",
  "graceful": false,
  "deleteDeploymentOnly": false
}
```

---

### **Resource Discovery Commands** *Uses `NVCF_API_KEY`*

#### **List Functions and Resources**

```bash
# Set the required token
export NVCF_API_KEY="nvapi-your-general-operations-token"

# List all functions in your account
./nvcf-cli list functions

# List only function IDs (lightweight)
./nvcf-cli list function-ids

# List all versions of a specific function
./nvcf-cli list versions func-12345678-1234-1234-1234-123456789abc

# List available cluster groups and GPUs
./nvcf-cli list clusters
```

**Example outputs:**
```bash
# List functions output
$ ./nvcf-cli list functions
Functions:
- ID: func-abc123, Name: my-test-function, Status: ACTIVE, Created: 2023-12-01T10:00:00Z
- ID: func-def456, Name: ml-model-api, Status: INACTIVE, Created: 2023-12-01T11:00:00Z

# List clusters output  
$ ./nvcf-cli list clusters
Available Cluster Groups:
- Name: GFN, GPUs: [L40, A100, H100], Regions: [us-west-2, us-east-1]
- Name: OCI, GPUs: [H100, A100], Regions: [us-phoenix-1]
```

#### **Get Detailed Information**

```bash
# Set the required token
export NVCF_API_KEY="nvapi-your-general-operations-token"

# Get detailed function information
./nvcf-cli get function --function-id func-12345678-1234-1234-1234-123456789abc --version-id ver-87654321-4321-4321-4321-123456789abc

# Output as JSON for programmatic use
./nvcf-cli get function --function-id func-12345678-1234-1234-1234-123456789abc --version-id ver-87654321-4321-4321-4321-123456789abc --json
```

**Example get function output:**
```
Function Details:
  ID: func-12345678-1234-1234-1234-123456789abc
  Version ID: ver-87654321-4321-4321-4321-123456789abc
  Name: my-function
  Status: ACTIVE
  Created: 2023-12-01T10:00:00Z
  Updated: 2023-12-01T12:00:00Z
  
Container Configuration:
  Image: nvcr.io/0651155215864979/ncp-dev/load_tester_supreme:0.0.8
  Inference URL: /echo
  Inference Port: 8000
  
Health Check:
  Protocol: HTTP
  URI: /health
  Port: 8000
  Timeout: PT30S
  Expected Status: 200
  
Deployment:
  Status: ACTIVE
  GPU: H100
  Instances: 1/1 (min/max)
  Instance Type: ON-PREM.GPU.H100_1x
```

**Function details include:**
- Basic metadata (ID, name, status, created date)
- Container configuration (image, ports, environment variables)
- Health check settings (protocol, URI, expected status)
- Deployment specifications (GPU, instances, scaling)
- Secrets and security settings
- Rate limiting configuration
- Tags and descriptions

---

### **Queue Management Commands** *Uses `NVCF_API_KEY`*

Monitor function execution and queue status:

```bash
# Set the required token
export NVCF_API_KEY="nvapi-your-general-operations-token"

# Check queue status for all functions
./nvcf-cli queue status

# Check queue status for a specific function
./nvcf-cli queue status func-12345678-1234-1234-1234-123456789abc

# Check queue status for a specific function version
./nvcf-cli queue status func-12345678-1234-1234-1234-123456789abc ver-87654321-4321-4321-4321-123456789abc

# Get position in queue for a specific request
./nvcf-cli queue position req-abcdef12-3456-7890-abcd-ef1234567890
```

**Example queue monitoring:**
```bash
# Check queue status
$ ./nvcf-cli queue status func-12345678-1234-1234-1234-123456789abc
Queue Status for Function: func-12345678-1234-1234-1234-123456789abc
  Active Instances: 2
  Queue Size: 5
  Estimated Wait Time: 30 seconds
  
Version Details:
- Version: ver-87654321, Queue Size: 3, Processing: 1
- Version: ver-12345678, Queue Size: 2, Processing: 1

# Check request position
$ ./nvcf-cli queue position req-abcdef12-3456-7890-abcd-ef1234567890
Request Position:
  Request ID: req-abcdef12-3456-7890-abcd-ef1234567890
  Position in Queue: 3
  Estimated Wait Time: 45 seconds
  Status: QUEUED
```

**Queue information includes:**
- Current queue size per function/version
- Estimated wait times
- Request position (up to 1000)
- Active processing instances
- Function-specific queue details

---

## **Example Workflows**

### **Complete Function Lifecycle** 

```bash
# Step 1: Set up authentication tokens
export NVCF_TOKEN="nvapi-your-function-creation-token"        # For create, deploy, delete
export NVCF_API_KEY="nvapi-your-general-operations-token"    # For invoke, list, queue

# Step 2: Discover available GPU resources (uses NVCF_API_KEY)
./nvcf-cli list clusters

# Step 3: Create a function (uses NVCF_TOKEN)
./nvcf-cli create --input-file examples/create-function.json
# Returns: Function ID: func-12345678-1234-1234-1234-123456789abc
#          Version ID: ver-87654321-4321-4321-4321-123456789abc

# Step 4: Deploy the function with H100 GPU (uses NVCF_TOKEN)
./nvcf-cli deploy \
  --function-id func-12345678-1234-1234-1234-123456789abc \
  --version-id ver-87654321-4321-4321-4321-123456789abc \
  --gpu H100 \
  --instance-type ON-PREM.GPU.H100_1x \
  --min-instances 1 \
  --max-instances 1

# Alternative: Deploy with JSON file
echo '{
  "deploymentSpecifications": [
    {
      "gpu": "H100",
      "maxInstances": 1,
      "minInstances": 1,
      "instanceType": "ON-PREM.GPU.H100_1x"
    }
  ]
}' > deploy.json
./nvcf-cli deploy --function-id func-12345678-1234-1234-1234-123456789abc --version-id ver-87654321-4321-4321-4321-123456789abc --input-file deploy.json

# Step 5: Update function metadata (uses NVCF_TOKEN)
./nvcf-cli update metadata \
  --function-id func-12345678-1234-1234-1234-123456789abc \
  --version-id ver-87654321-4321-4321-4321-123456789abc \
  --description "Updated production function" \
  --tags "production,v2,optimized"

# Step 6: Update deployment (scale up) (uses NVCF_TOKEN)
./nvcf-cli update deployment \
  --function-id func-12345678-1234-1234-1234-123456789abc \
  --version-id ver-87654321-4321-4321-4321-123456789abc \
  --min-instances 2 \
  --max-instances 4 \
  --max-request-concurrency 20

# Step 7: Invoke the function (uses NVCF_API_KEY)
./nvcf-cli invoke \
  func-12345678-1234-1234-1234-123456789abc \
  ver-87654321-4321-4321-4321-123456789abc \
  '{"input": "Hello from CLI!"}'

# Step 8: Monitor queue status (uses NVCF_API_KEY)
./nvcf-cli queue status func-12345678-1234-1234-1234-123456789abc ver-87654321-4321-4321-4321-123456789abc

# Step 9: Clean up (uses NVCF_TOKEN)
./nvcf-cli delete func-12345678-1234-1234-1234-123456789abc ver-87654321-4321-4321-4321-123456789abc  # uses NVCF_TOKEN only
```

### **Resource Discovery Workflow** *Uses `NVCF_API_KEY`*

```bash
# Set up authentication
export NVCF_API_KEY="nvapi-your-general-operations-token"

# 1. List all functions
./nvcf-cli list functions

# 2. List function IDs only (lightweight)
./nvcf-cli list function-ids

# 3. Get detailed information about a specific function
./nvcf-cli get function --function-id func-12345678-1234-1234-1234-123456789abc --version-id ver-87654321-4321-4321-4321-123456789abc

# 4. Check deployment queue status
./nvcf-cli queue status func-12345678-1234-1234-1234-123456789abc ver-87654321-4321-4321-4321-123456789abc

# 5. List available GPU cluster groups
./nvcf-cli list clusters

# 6. Export function details as JSON for automation
./nvcf-cli get function --function-id func-12345678-1234-1234-1234-123456789abc --version-id ver-87654321-4321-4321-4321-123456789abc --json > function-details.json
```

### **Development & Testing Workflow** 

```bash
# Set up both tokens for full functionality
export NVCF_TOKEN="nvapi-your-function-creation-token"      # Create, deploy, delete
export NVCF_API_KEY="nvapi-your-general-operations-token"  # Invoke, list, queue

# 1. Enable debug mode to see token selection
export NVCF_CLI_DEBUG=true

# 2. Create test function
./nvcf-cli create \
  --name "test-function" \
  --image "nvcr.io/0651155215864979/ncp-dev/load_tester_supreme:0.0.8" \
  --inference-url "/echo" \
  --inference-port 8000 \
  --health-uri "/health" \
  --debug

# 3. Deploy for testing
./nvcf-cli deploy \
  --function-id <function-id-from-step-2> \
  --version-id <version-id-from-step-2> \
  --gpu H100 \
  --instance-type ON-PREM.GPU.H100_1x \
  --min-instances 1 \
  --max-instances 1 \
  --debug

# 4. Test invocation
./nvcf-cli invoke \
  <function-id> <version-id> \
  '{"input": "test message"}' \
  --timeout 60 \
  --debug

# 5. Monitor and debug
./nvcf-cli queue status <function-id> <version-id>
./nvcf-cli get function --function-id <function-id> --version-id <version-id> --json

# 6. Clean up test resources
./nvcf-cli delete <function-id> <version-id> --debug
```

---

## **Troubleshooting**

### **Common Authentication Issues**

#### **401 Unauthorized on Function Creation**
```
Error: API error 401: Invalid JWT serialization
```
**Solution**: Check `NVCF_TOKEN` is valid for function creation
```bash
export NVCF_TOKEN="nvapi-your-function-creation-token"
./nvcf-cli create --debug --name test --image nvcr.io/0651155215864979/ncp-dev/load_tester_supreme:0.0.8 --inference-url /echo --inference-port 8000
# Look for: "Using FUNCTION TOKEN for POST"
```

#### **Missing NVCF_TOKEN on Delete Operations**
```
Error: failed to load configuration: NVCF_TOKEN is required for delete operations
```
**Solution**: Delete operations require `NVCF_TOKEN` exclusively
```bash
export NVCF_TOKEN="nvapi-your-function-creation-token"
./nvcf-cli delete --function-id func-123 --version-id ver-456 --debug
# Look for: "Using FUNCTION TOKEN for DELETE"
```

#### **401 Unauthorized on Invoke/List Operations**
```
Error: API error 401: Unauthorized
```
**Solution**: Check `NVCF_API_KEY` is valid for general operations
```bash
export NVCF_API_KEY="nvapi-your-general-operations-token"
./nvcf-cli invoke --debug func-123 ver-456 '{"input": "test"}'
# Look for: "Using API KEY for"
```

#### **Deploy Operation Token Issues** 
```
Error: API error 401: Unauthorized
```
**Solution**: Deploy operations prefer `NVCF_TOKEN` but can fallback to `NVCF_API_KEY`
```bash
export NVCF_TOKEN="nvapi-your-function-creation-token"
export NVCF_API_KEY="nvapi-your-general-operations-token"  # fallback
./nvcf-cli deploy --debug --function-id func-123 --version-id ver-456 --gpu H100 --instance-type ON-PREM.GPU.H100_1x
# Look for: "Using FUNCTION TOKEN for POST" or "Using API KEY for POST"
```

### **Debug Token Selection**
```bash
# See exactly which token is being used for each command

# Function creation (uses NVCF_TOKEN)
./nvcf-cli create --debug --name test --image nvcr.io/0651155215864979/ncp-dev/load_tester_supreme:0.0.8 --inference-url /echo --inference-port 8000
# Output: DEBUG: Using FUNCTION TOKEN for POST /v2/nvcf/accounts/nvcf-default/functions

# Function deletion (uses NVCF_TOKEN only)
./nvcf-cli delete --debug --function-id func-123 --version-id ver-456
# Output: DEBUG: Using FUNCTION TOKEN for DELETE /v2/nvcf/functions/func-123/versions/ver-456

# Function invocation (uses NVCF_API_KEY)
./nvcf-cli invoke --debug func-123 ver-456 '{"input": "test"}'
# Output: DEBUG: Using API KEY for POST /v2/nvcf/functions/func-123/versions/ver-456/invocations

```

### **Authentication Token Summary**

| **Operation** | **Required Token** | **Fallback** | **Debug Command** |
|---------------|-------------------|--------------|-------------------|
| `create` | `NVCF_TOKEN` | `NVCF_API_KEY` | `./nvcf-cli create --debug ...` |
| `deploy` | `NVCF_TOKEN` | `NVCF_API_KEY` | `./nvcf-cli deploy --debug ...` |
| `delete` | `NVCF_TOKEN` | **None** | `./nvcf-cli delete --debug ...` |
| `invoke` | `NVCF_API_KEY` | `NVCF_TOKEN` | `./nvcf-cli invoke --debug ...` |
| `list`, `get`, `queue` | `NVCF_API_KEY` | `NVCF_TOKEN` | `./nvcf-cli list --debug ...` |

### **Configuration Issues**

1. **Start with debug mode** when encountering API issues
2. **Check the request URL** matches expected NVIDIA endpoints
3. **Verify request body** contains expected parameters  
4. **Compare response** with API documentation
5. **Use configuration templates** for consistent setup

---

## Development

### **Project Structure**

```
.
├── cmd/                    # CLI commands
│   ├── root.go            # Root command and initialization
│   ├── create.go          # Create function command
│   ├── deploy.go          # Deploy function command
│   ├── delete.go          # Delete function command
│   ├── invoke.go          # Invoke function command
│   ├── list.go            # List resources command
│   ├── get.go             # Get detailed information
│   └── queue.go           # Queue management commands
├── internal/
│   └── client/            # NVCF client implementation
│       ├── client.go      # Main client logic (1200+ lines)
│       ├── client_test.go # Client tests
│       ├── debug_transport.go        # HTTP debugging
│       └── multi_token_transport.go  # Multi-token auth
├── examples/              # JSON configuration examples
├── main.go                # Application entry point
├── go.mod                 # Go module definition (consumed by Bazel via go.work.bazel)
├── go.sum                 # Go module checksums
├── BUILD.bazel            # Bazel targets: nvcf-cli binary, dist matrix, OCI image
└── README.md             # This file
```

### Building

All build, test, and packaging operations go through Bazel. See `BAZEL.md` at the repo root for one-time setup.

```bash
# Build for current platform
bazel build //src/clis/nvcf-cli:nvcf-cli

# Build for all supported platforms (linux/darwin/windows x amd64/arm64)
bazel build //src/clis/nvcf-cli:dist

# Stamp the binary with full version metadata (Version, GitCommit, BuildDate, ...)
bazel build --stamp //src/clis/nvcf-cli:nvcf-cli

# Build the multi-arch OCI image
bazel build //src/clis/nvcf-cli:image_index

# Load the host-arch image into your local docker daemon
bazel run //src/clis/nvcf-cli:image_load

# Format Go and Bazel files (gofmt + buildifier)
gofmt -w .
bazel run @rules_go//go -- fmt ./...

# Run linter (still go-tooling; not yet wired into Bazel)
golangci-lint run ./...
```

### Running Tests

The CLI test suite runs under the Bazel sandbox. Tests that need real
infrastructure (k3d clusters, helmfile, NVCF backends) are tagged `e2e` and
live under `test/e2e/`; see that directory's README.

```bash
# Run all unit tests
bazel test //src/clis/nvcf-cli/...

# Run a single package's tests
bazel test //src/clis/nvcf-cli/internal/client:client_test

# Stream test output even on success
bazel test //src/clis/nvcf-cli/... --test_output=streamed

# Coverage (requires lcov on PATH; html report generation is a follow-up)
bazel coverage //src/clis/nvcf-cli/...
```

### Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Run `bazel test //src/clis/nvcf-cli/...` to verify tests pass
5. Commit your changes with DCO sign-off (`git commit -s -m 'Add amazing feature'`)
6. Push to the branch (`git push origin feature/amazing-feature`)
7. Open a Pull Request

### Development Guidelines

- Follow Go coding standards and conventions
- Write comprehensive tests for new features
- Update documentation for any API changes
- Ensure `bazel test //src/clis/nvcf-cli/...` is green before submitting PRs
- After adding a new Go import, run `bazel run //:gazelle` to refresh BUILD files

---

## **API Coverage**

The CLI now provides comprehensive coverage of NVIDIA Cloud Function APIs:

| **API Category** | **Status** | **Endpoints** |
|------------------|------------|---------------|
| **Function Management** | Complete | Create, Deploy, Update, Delete, List, Get Details |
| **Function Invocation** | Complete | Invoke with hold-open hint |
| **Cluster Groups** | Complete | List available GPU resources |
| **Queue Management** | Complete | Position, Details, Status |
| **Function Sharing** | ⏳ Planned | Authorization management |

**Total API Methods Supported**: Core function, invocation, cluster group, and queue endpoints

---

## Migration Guide for Existing Users

### **Upgrading from Previous Versions**

The enhanced CLI is **100% backward compatible** with existing workflows. Here's how to take advantage of new features:

#### **Current Users: Keep Using What Works**

```bash
# Your existing workflows continue to work unchanged
export NVCF_API_KEY="your-existing-key"
export NVCF_TOKEN="your-existing-token"

./nvcf-cli create --input-file function.json  # Still works
./nvcf-cli deploy --function-id func-123 --version-id ver-456  # Still works
./nvcf-cli invoke --function-id func-123 --version-id ver-456 --request-body '{}'  # Still works
```

#### **Gradual Enhancement Path**

1. **Phase 1**: Add automatic context (no code changes needed)
   ```bash
   # After create/deploy, you can now skip IDs
   ./nvcf-cli create --input-file function.json
   ./nvcf-cli deploy  # Uses saved context automatically
   ./nvcf-cli invoke --request-body '{"input": "test"}'  # Uses saved context
   ```

2. **Phase 2**: Try automatic token generation (optional)
   ```bash
   # Generate tokens automatically via API calls
   nvcf-cli init
   nvcf-cli api-key generate
   ```

3. **Phase 3**: Use multi-environment configs (when ready)
   ```bash
   # Separate dev/prod environments
   nvcf-cli --config dev.yaml create --input-file function.json
   nvcf-cli --config prod.yaml list functions
   ```

#### **What's New vs What's The Same**

| **Aspect** | **Before** | **After** | **Change Required** |
|------------|------------|-----------|-------------------|
| **Token Setup** | Manual export | Manual OR auto-generation | **None** (optional upgrade) |
| **Function Creation** | Manual IDs everywhere | Smart context + manual fallback | **None** (automatic improvement) |
| **Configuration** | Environment variables | Env vars OR YAML configs | **None** (additive) |
| **Commands** | All existing commands | Same + new helper commands | **None** (additive) |
| **Authentication** | Manual token management | Auto-management OR manual | **None** (optional upgrade) |

### **New Command Summary**

**New commands you can try (optional):**
- `nvcf-cli init` - Generate admin token
- `nvcf-cli refresh` - Refresh token while keeping context  
- `nvcf-cli api-key generate` - Generate API keys
- `nvcf-cli status` - Check current state
- `nvcf-cli undeploy` - Undeploy functions

**Enhanced existing commands:**
- All commands now support `--config` for multi-environment
- `create`, `deploy`, `invoke`, `delete` now use smart context
- `invoke` now supports `--grpc` for gRPC invocation
- All commands have improved output and error messages

### **Benefits You Get Immediately (No Changes Required)**

- **Better Error Messages**: More helpful debugging information
- **Automatic Context**: No more copying/pasting function IDs
- **Enhanced Output**: Colored success/warning/error messages
- **State Persistence**: CLI remembers your current function across commands

---

## **License**

This project is licensed under the terms specified in the repository license file.

## **Support**

For support and questions:
- Create an issue in the repository
- Check the debug output using `--debug` flag
- Review the comprehensive examples in the `examples/` directory
