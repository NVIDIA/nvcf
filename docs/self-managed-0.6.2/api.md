# API

This page provides a brief overview of the NVCF API. All API endpoints are served through your gateway. See [gateway-routing](./gateway-routing.md) for details on configuring your gateway domain and DNS.

## OpenAPI Specification

This page does not cover all endpoints.

Please refer to the [OpenAPI Spec](https://api.nvcf.nvidia.com/v3/openapi) for the latest API information.

<Note>
The OpenAPI spec linked above documents the full NVCF API surface. Replace the hosted domain with your own gateway domain when making requests. See [gateway-routing](./gateway-routing.md) for your deployment's base URL.

</Note>

The NVCF API is divided into the following sets of APIs:

| APIs                  | Usage                                                                                 |
| --------------------- | ------------------------------------------------------------------------------------- |
| Function Invocation   | Execution of a function that runs on a worker node. Usually an inference call.        |
| Cluster Groups & GPUs | Defines endpoints to list Cluster Groups and GPUs as targets for function deployment. |
| Function Management   | The creation, modification and deletion of functions                                  |
| Function Deployment   | Endpoints for creating and managing function deployments.                             |
| Task Management       | GPU-backed batch jobs via NVIDIA Cloud Tasks (NVCT).                                  |

**API Versioning**

All API endpoints include versioning in the path prefix.

```bash
/v2/nvcf
```

## Authorization

Self-hosted NVCF uses three bearer credential types:

| Credential | CLI source | Typical use |
| --- | --- | --- |
| `NVCF_TOKEN` | `nvcf-cli init` | Default CLI credential for management operations and self-hosted cluster management |
| `NVCF_API_KEY` | `nvcf-cli api-key generate` | Default CLI credential for function invocation, function discovery, and queue status |
| `NVCF_NVCT_API_KEY` | `nvcf-cli api-key generate` | Credential for task commands (`task create`, `task list`, etc.) |

All three credential types are sent as bearer credentials:

```bash
Authorization: Bearer <credential>
```

Use the CLI for normal credential management:

```bash
nvcf-cli init
nvcf-cli api-key generate
```

`nvcf-cli init` calls the API Keys admin endpoint and mints the JWT used as
`NVCF_TOKEN`. The CLI saves the token in `~/.nvcf-cli.state` for the default
configuration and reads it automatically on later commands. Export it only when
you are making direct API calls or using tooling that reads environment
variables:

```bash
export NVCF_TOKEN=$(jq -r .token ~/.nvcf-cli.state)
```

The default self-hosted admin issuer role gives this token the following
scopes:

- `register_function`
- `list_functions`
- `list_functions_details`
- `deploy_function`
- `update_function`
- `update_secrets`
- `delete_function`
- `manage_telemetries`
- `manage_registry_credentials`
- `cluster-management`

The function API key (`NVCF_API_KEY`) is created with these default scopes:

- `invoke_function`
- `list_functions`
- `queue_details`
- `list_functions_details`

The task API key (`NVCF_NVCT_API_KEY`) is created with these default scopes:

- `launch_task`
- `list_tasks`
- `task_details`
- `cancel_task`
- `delete_task`
- `list_events`
- `list_results`
- `update_secrets`

NVCF API authorization is scope-based. For the NVCF API endpoints below, either
`NVCF_TOKEN` or `NVCF_API_KEY` can be used if that bearer includes the required
scope. The CLI prefers `NVCF_API_KEY` for read, invoke, and queue commands. It
prefers `NVCF_TOKEN` for management commands when both credentials are
configured.

Pass `--scopes` when you need to narrow a key or create a key with management
scopes for testing.

### Scope reference

The OpenAPI spec describes the permission checked for each endpoint. "Accepted
bearer" means which saved CLI credential type can be sent as
`Authorization: Bearer <credential>` when it includes the listed scope.

| Category | Method and endpoint | Accepted bearer | Scope |
| --- | --- | --- | --- |
| Function invocation | `POST /v2/nvcf/pexec/functions/{functionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `invoke_function` |
| Function invocation | `POST /v2/nvcf/pexec/functions/{functionId}/versions/{versionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `invoke_function` |
| Function invocation | `GET /v2/nvcf/pexec/status/{requestId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `invoke_function` |
| Queue details | `GET /v2/nvcf/queues/{requestId}/position` | `NVCF_TOKEN` or `NVCF_API_KEY` | `queue_details` |
| Queue details | `GET /v2/nvcf/queues/functions/{functionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `queue_details` |
| Queue details | `GET /v2/nvcf/queues/functions/{functionId}/versions/{versionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `queue_details` |
| Function management | `GET /v2/nvcf/functions` | `NVCF_TOKEN` or `NVCF_API_KEY` | `list_functions` or `list_functions_details` |
| Function management | `GET /v2/nvcf/functions/ids` | `NVCF_TOKEN` or `NVCF_API_KEY` | `list_functions` or `list_functions_details` |
| Function management | `POST /v2/nvcf/functions` | `NVCF_TOKEN` or `NVCF_API_KEY` | `register_function` |
| Function management | `GET /v2/nvcf/functions/{functionId}/versions` | `NVCF_TOKEN` or `NVCF_API_KEY` | `list_functions` or `list_functions_details` |
| Function management | `POST /v2/nvcf/functions/{functionId}/versions` | `NVCF_TOKEN` or `NVCF_API_KEY` | `register_function` |
| Function management | `GET /v2/nvcf/functions/{functionId}/versions/{functionVersionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `list_functions` or `list_functions_details` |
| Function management | `PUT /v2/nvcf/functions/{functionId}/versions/{functionVersionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `update_function` |
| Function management | `DELETE /v2/nvcf/functions/{functionId}/versions/{functionVersionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `delete_function` |
| Function management | `PUT /v2/nvcf/metadata/functions/{functionId}/versions/{functionVersionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `update_function` |
| Function deployment | `GET /v2/nvcf/deployments/functions/{functionId}/versions/{functionVersionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `deploy_function` |
| Function deployment | `POST /v2/nvcf/deployments/functions/{functionId}/versions/{functionVersionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `deploy_function` |
| Function deployment | `PUT /v2/nvcf/deployments/functions/{functionId}/versions/{functionVersionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `deploy_function` |
| Function deployment | `DELETE /v2/nvcf/deployments/functions/{functionId}/versions/{functionVersionId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `deploy_function` |
| Function deployment | `GET /v2/nvcf/deployments/{deploymentId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `deploy_function` |
| Function deployment | `PATCH /v2/nvcf/deployments/{deploymentId}/gpu-specifications/{gpuSpecId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `deploy_function` |
| Registry credential management | `GET /v2/nvcf/registry-credentials` | `NVCF_TOKEN` or `NVCF_API_KEY` | `manage_registry_credentials` |
| Registry credential management | `POST /v2/nvcf/registry-credentials` | `NVCF_TOKEN` or `NVCF_API_KEY` | `manage_registry_credentials` |
| Registry credential management | `GET /v2/nvcf/registry-credentials/{registryCredentialId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `manage_registry_credentials` |
| Registry credential management | `PATCH /v2/nvcf/registry-credentials/{registryCredentialId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `manage_registry_credentials` |
| Registry credential management | `DELETE /v2/nvcf/registry-credentials/{registryCredentialId}` | `NVCF_TOKEN` or `NVCF_API_KEY` | `manage_registry_credentials` |
| Registry credential management | `GET /v2/nvcf/recognized-registries` | `NVCF_TOKEN` or `NVCF_API_KEY` | `manage_registry_credentials` |

Self-hosted cluster management uses SIS endpoints from the Spot API. In
self-hosted CLI workflows, use `NVCF_TOKEN` with the `cluster-management` scope
for these endpoints.

| Method and endpoint | Accepted bearer | Scope |
| --- | --- | --- |
| `GET /v1/accounts/{ncaId}/clusters` | `NVCF_TOKEN` | `cluster-management` |
| `POST /v1/accounts/{ncaId}/clusters` | `NVCF_TOKEN` | `cluster-management` |
| `GET /v1/accounts/{ncaId}/clusters/{clusterId}` | `NVCF_TOKEN` | `cluster-management` |
| `PUT /v1/accounts/{ncaId}/clusters/{clusterId}` | `NVCF_TOKEN` | `cluster-management` |
| `DELETE /v1/accounts/{ncaId}/clusters/{clusterId}` | `NVCF_TOKEN` | `cluster-management` |
| `GET /v1/accounts/{ncaId}/clusterVersions` | `NVCF_TOKEN` | `cluster-management` |

Task management uses NVCT endpoints served through your gateway. All task
endpoints require `NVCF_NVCT_API_KEY` with the listed scope.

| Method and endpoint | Accepted bearer | Scope |
| --- | --- | --- |
| `POST /v1/nvct/tasks` | `NVCF_NVCT_API_KEY` | `launch_task` |
| `GET /v1/nvct/tasks` | `NVCF_NVCT_API_KEY` | `list_tasks` |
| `POST /v1/nvct/tasks/bulk` | `NVCF_NVCT_API_KEY` | `list_tasks` |
| `GET /v1/nvct/tasks/{taskId}` | `NVCF_NVCT_API_KEY` | `task_details` |
| `DELETE /v1/nvct/tasks/{taskId}` | `NVCF_NVCT_API_KEY` | `delete_task` |
| `POST /v1/nvct/tasks/{taskId}/cancel` | `NVCF_NVCT_API_KEY` | `cancel_task` |
| `GET /v1/nvct/tasks/{taskId}/events` | `NVCF_NVCT_API_KEY` | `list_events` |
| `GET /v1/nvct/tasks/{taskId}/results` | `NVCF_NVCT_API_KEY` | `list_results` |
| `PUT /v1/nvct/secrets/tasks/{taskId}` | `NVCF_NVCT_API_KEY` | `update_secrets` |

### Direct API key creation

The CLI calls the API Keys service for you. If you need to call it directly,
set `expires_at` and the `scopes` array explicitly.

```bash
GATEWAY_ADDR=<your-gateway-address>
EXPIRES_AT=$(date -u -v+1d '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d '+1 day' '+%Y-%m-%dT%H:%M:%SZ')
SERVICE_ID="nvidia-cloud-functions-ncp-service-id-aketm"

curl -s -X POST "http://${GATEWAY_ADDR}/v1/keys" \
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
        "scopes": ["invoke_function", "list_functions", "queue_details", "list_functions_details"]
      }]
    },
    "audience_service_ids": ["'"${SERVICE_ID}"'"]
  }' | jq .
```
