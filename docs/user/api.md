# API

This page provides a brief overview of the NVCF API. All API endpoints are served through your gateway. See [gateway-routing](./gateway-routing) for details on configuring your gateway domain and DNS.

## OpenAPI Specification

This page does not cover all endpoints.

Please refer to the [OpenAPI Spec](https://api.nvcf.nvidia.com/v3/openapi) for the latest API information.

<Note>
The OpenAPI spec linked above documents the full NVCF API surface. Replace the hosted domain with your own gateway domain when making requests. See [gateway-routing](./gateway-routing) for your deployment's base URL.

</Note>

The NVCF API is divided into the following sets of APIs:

| APIs                  | Usage                                                                                 |
| --------------------- | ------------------------------------------------------------------------------------- |
| Function Invocation   | Execution of a function that runs on a worker node. Usually an inference call.        |
| Cluster Groups & GPUs | Defines endpoints to list Cluster Groups and GPUs as targets for function deployment. |
| Function Management   | The creation, modification and deletion of functions                                  |
| Function Deployment   | Endpoints for creating and managing function deployments.                             |

**API Versioning**

All API endpoints include versioning in the path prefix.

```bash
/v2/nvcf
```

## Authorization

The NVCF API supports API key-based authorization. API keys can be generated using the [CLI](./cli) or directly via the API Keys service endpoint.

### Generate an API Key

API keys can be generated using the [CLI](./cli) or directly via the API Keys service. There are two types of API keys:

**Using the CLI**

The simplest way to generate an API key is via the [CLI](./cli):

```bash
nvcf-cli api-key create
```

Refer to the [CLI documentation](./cli) for full usage and additional token generation options.

**Admin Token**

An admin token provides full access to all NVCF API endpoints. This is useful for initial setup, managing functions, and administrative tasks.

```bash
export GATEWAY_ADDR=<your-gateway-address>

export NVCF_TOKEN=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/admin/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  | grep -o '"value":"[^"]*"' | cut -d'"' -f4)

echo "Token generated: ${NVCF_TOKEN:0:20}..."
```

**Scoped API Key**

A scoped API key restricts access to specific operations and resources. This is recommended for application use, such as invoking functions.

```bash
EXPIRES_AT=$(date -u -v+1d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d '+1 day' '+%Y-%m-%dT%H:%M:%SZ')
SERVICE_ID="nvidia-cloud-functions-ncp-service-id-aketm"

export API_KEY=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  -H "Content-Type: application/json" \
  -H "Key-Issuer-Service: nvcf-api" \
  -H "Key-Issuer-Id: ${SERVICE_ID}" \
  -H "Key-Owner-Id: test@nvcf-api.local" \
  -d '{
    "description": "invocation key",
    "expires_at": "'"${EXPIRES_AT}"'",
    "authorizations": {
      "policies": [{
        "aud": "'"${SERVICE_ID}"'",
        "auds": ["'"${SERVICE_ID}"'"],
        "product": "nv-cloud-functions",
        "resources": [
          {"id": "*", "type": "account-functions"},
          {"id": "*", "type": "authorized-functions"}
        ],
        "scopes": ["invoke_function", "list_functions", "list_functions_details"]
      }]
    },
    "audience_service_ids": ["'"${SERVICE_ID}"'"]
  }' | jq -r '.value')

echo "API Key: ${API_KEY:0:20}..."
```

<Note>
The `scopes` array controls which API operations the key can perform. See [self-hosted-scopes](./api) for the full list. The `expires_at` field is required for scoped API keys.

</Note>

**API Key Usage**

Both key types are passed in the `Authorization` header.

```bash
Authorization: Bearer $API_KEY
```

### API Key Scopes

The [OpenAPI Spec](https://api.nvcf.nvidia.com/v3/openapi) describes the scopes required for each endpoint.

| Scope Name          | API Category            |
| ------------------- | ----------------------- |
| update_function     | Function Management     |
| register_function   | Function Management     |
| list_functions      | Function Management     |
| list_cluster_groups | Cluster Groups and GPUs |
| invoke_function     | Function Invocation     |
| deploy_function     | Function Deployment     |
| delete_function     | Function Management     |
