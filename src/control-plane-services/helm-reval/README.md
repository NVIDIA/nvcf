# NVCF HELM REVAL API

Helm ReVal HTTP service — render/validate API, `pkg/authorizers`, and related libraries.

**Module:** `github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval`

## Local Setup

1. Copy `examples/reval-config.yaml` to `config.yaml` and fill out the fields.

1. Run the server:

    ```bash
    go run ./cmd/reval-service --config ./config.yaml
    ```

   Or use the Makefile shortcut (uses `test/config.yaml` with a local mock JWKS):

    ```bash
    make run
    ```

## Build

All dependencies are public. No credentials needed.

```bash
make build      # go build → _output/bin/reval
make test       # go test ./...
make container  # docker build
```

## API Endpoints

### `POST /v1/validate`

Validates a Helm chart; returns `valid: true` or `valid: false` with a list of errors.

**Request body:**

| Field | Type | Required | Description |
|---|---|---|---|
| `helmChart` | `string` | ✓ | URL or OCI ref of the chart |
| `namespace` | `string` | | Kubernetes namespace |
| `configuration` | `object` | | Helm values override |
| `k8sVersion` | `string` | | Kubernetes version for `Capabilities.KubeVersion` |
| `apiVersions` | `[]string` | | Additional API versions |
| `validationPolicies` | `[]ValidationPolicy` | | One or more named policies |

**Response:**

| Field | Type | Description |
|---|---|---|
| `valid` | `bool` | Overall validity |
| `validationErrors` | `[]string` | Top-level errors (no-policy path) |
| `validationPolicies` | `[]PolicyResult` | Per-policy results when policies are provided |

### `POST /v1/render`

Same as `/v1/validate` but also returns the rendered Kubernetes manifests.

**Additional response field:**

| Field | Type | Description |
|---|---|---|
| `output` | `json.RawMessage` | Rendered manifest list (only when `valid: true`) |

### Policy names

| Name | Behavior |
|---|---|
| `"Default"` | Core Kubernetes types only; list extra types in `allowedExtraKubernetesTypes` |
| `"Unrestricted"` | All types allowed; skips type and business-rule checks |

### Authentication

* **Header:** `Authorization: Bearer <token>`
* **Required scopes:** `helmreval:validate` / `helmreval:render` (when using the `Local` authorizer)

## Authorization

Two authorization modes are supported and can be enabled independently. If neither is enabled, auth is disabled and a warning is logged at startup.

| Config | Authorizer | Behavior |
|---|---|---|
| `auth.jwt.enabled: true` + `auth.jwt.jwk-set-url` set | `Local` | Verifies JWT signature locally against a JWKS URL, then checks per-endpoint scopes (`helmreval:validate` / `helmreval:render`) |
| `auth.oidc.enabled: true` + `auth.oidc.introspect-url` set | `ICMSIntrospect` | Delegates token verification to a remote RFC 7662 introspection endpoint (e.g. ICMS); used for Kubernetes PSAT tokens in self-managed clusters |

Both modes can be active simultaneously (OR semantics — a token accepted by either authorizer is allowed through).

Custom authorizer backends can be injected via `cli.Options.AuthorizerFactory` — the server calls the factory at startup instead of `BuildChain`. See [`pkg/authorizers/`](./pkg/authorizers/) for the `Authorizer` interface.

See [`examples/`](./examples/) for ready-to-use configs.

## Testing the API

### VS Code REST Client

In the `api-tests/` folder you will find a `requests.http` file compatible with the
[REST Client VS Code extension](https://marketplace.visualstudio.com/items?itemName=humao.rest-client).

1. Install the extension.
1. Copy `api-tests/.env.example` to `api-tests/.env` and fill out the fields.
1. Start the server: `make run`
1. Open `api-tests/requests.http` and click **Send Request** above any endpoint.

### Shell-based integration test

Starts the server, a mock JWKS endpoint, and local test registries, then validates the bundled test chart:

```bash
make test-server
```

This runs `test/test_server.sh`, which:
1. Generates an ephemeral RSA keypair, starts a mock JWKS server on port `8888`, and signs a short-lived JWT carrying the required scopes
2. Starts local helm (`:8282`) and image (`:8383`) registries via `make run-test-regs`
3. Starts the reval server via `make run`
4. Waits for `/healthz` to return `200`, then POSTs a render request against the local test chart using the signed JWT

### Example `curl` requests

```bash
# When running locally via `make test-server`, the script writes the signed
# bearer token to /tmp/reval-test-token; export it first:
TOKEN="$(cat /tmp/reval-test-token)"
CHART="oci://registry-1.docker.io/bitnamicharts/nginx"

# Validate (no policy — default type restrictions)
curl -X POST http://localhost:8080/v1/validate \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"helmChart\":\"$CHART\",\"namespace\":\"test\"}"

# Validate (Unrestricted policy)
curl -X POST http://localhost:8080/v1/validate \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "helmChart": "'"$CHART"'",
    "namespace": "test",
    "validationPolicies": [{"id":"p1","name":"Unrestricted","allowedExtraKubernetesTypes":[]}]
  }'

# Render (Unrestricted policy — returns full manifest list in output)
curl -X POST http://localhost:8080/v1/render \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "helmChart": "'"$CHART"'",
    "namespace": "test",
    "validationPolicy": {"name":"Unrestricted","allowedExtraKubernetesTypes":[]}
  }'
```

## Kubernetes

- Chart: [`deploy/helm/reval-service/`](./deploy/helm/reval-service/README.md)
- Config samples: [`examples/`](./examples/)

## License

SPDX-License-Identifier: Apache-2.0
