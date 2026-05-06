# Configuration Reference

Detailed reference for configuring `nvcf-cli`.

## Config File

The CLI uses YAML configuration files. Copy the included template to get started:

```bash
cp .nvcf-cli.yaml.template .nvcf-cli.yaml
```

### Search Order

Config files are searched in this order (highest priority first):

1. **Explicit path** via `--config` flag
2. **Current directory**: `./.nvcf-cli.yaml`
3. **Home directory**: `~/.nvcf-cli.yaml`

### Configuration Priority

Values are resolved in this order (highest to lowest):

1. Command-line flags (e.g., `--debug`)
2. Environment variables (e.g., `NVCF_BASE_HTTP_URL`)
3. Config file in current directory
4. Config file in home directory
5. Built-in defaults

## Self-Hosted Configuration

For self-hosted deployments, the CLI must communicate with your Envoy Gateway. The gateway uses hostname-based routing for HTTP services, which requires host header overrides.

### Get Your Gateway Address

After deploying the control plane:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway \
  -o jsonpath='{.status.addresses[0].value}')
echo "Gateway Address: $GATEWAY_ADDR"
```

### Complete Self-Hosted Config

Replace `<GATEWAY_ADDR>` with your gateway address (e.g., an AWS ELB hostname like `a1b2c3d4.us-west-2.elb.amazonaws.com`):

```yaml
# .nvcf-cli.yaml

base_http_url: "http://<GATEWAY_ADDR>"
invoke_url: "http://<GATEWAY_ADDR>"
base_grpc_url: "<GATEWAY_ADDR>:10081"
api_keys_service_url: "http://<GATEWAY_ADDR>"

api_keys_host: "api-keys.<GATEWAY_ADDR>"
api_host: "api.<GATEWAY_ADDR>"
invoke_host: "invocation.<GATEWAY_ADDR>"

api_keys_service_id: "nvidia-cloud-functions-ncp-service-id-<suffix>"
api_keys_issuer_service: "nvcf-api"
api_keys_owner_id: "svc@nvcf-api.local"

client_id: "nvcf-default"
```

Use the `api_keys_service_id` value from the stack's generated CLI config, such as `nvcf-cli-local.yaml`, or from the target deployment's API Keys metadata. The suffix is deployment-specific; do not copy the placeholder verbatim.

### Production Setup (DNS/HTTPS)

With proper DNS and TLS configured, host header overrides are not needed. DNS records resolve service hostnames directly to your gateway's load balancer:

```yaml
# .nvcf-cli.yaml (production with DNS/HTTPS)

base_http_url: "https://api.nvcf.example.com"
invoke_url: "https://invocation.nvcf.example.com"
base_grpc_url: "grpc.nvcf.example.com:443"
api_keys_service_url: "https://api-keys.nvcf.example.com"
```

### Local Development (k3d)

Two local access models are valid for k3d:

1. Direct `.localhost` routes exposed by the k3d load balancer
2. Port-forwarding the gateway service to a local port and using host-header overrides

Prefer the direct `.localhost` model when it is reachable because it is simpler and was validated on the default `ncp-local` stack. If the local environment is already set up around a gateway port-forward, keep using that model. If both appear plausible and the local context does not clearly choose one, ask the user which model their cluster is using.

#### Option A: Direct `.localhost` routes

On the default `ncp-local` k3d stack, the validated local setup used:

```yaml
# .nvcf-cli.local-k3d.yaml
base_http_url: "http://api.localhost:8080"
invoke_url: "http://invocation.localhost:8080"
base_grpc_url: "grpc.localhost:443"
api_keys_service_url: "http://api-keys.localhost:8080"

client_id: "nvcf-default"
```

Check the control-plane routes before running function commands:

```bash
curl -sf http://api-keys.localhost:8080/health
kubectl --context k3d-ncp-local get ns
kubectl --context k3d-ncp-local get nodes
```

Then verify authentication through the CLI itself:

```bash
nvcf-cli --config .nvcf-cli.local-k3d.yaml init
nvcf-cli --config .nvcf-cli.local-k3d.yaml api-key generate
```

If the agent environment cannot write to the real home directory, use a writable temporary `HOME` and copy the config there before running `init` and `api-key generate`.

#### Option B: Gateway port-forward

Use port-forwarding when the `.localhost` routes are unavailable or when the local setup is explicitly using a forwarded gateway service:

```bash
kubectl --context k3d-ncp-local get svc -n envoy-gateway-system
kubectl --context k3d-ncp-local port-forward \
  -n envoy-gateway-system \
  svc/<gateway-service-name> 18080:80
```

With the port-forward active, point the CLI at `http://localhost:18080` and set `api_keys_host`, `api_host`, and `invoke_host` to the corresponding `.localhost:18080` hostnames:

```yaml
# .nvcf-cli.local-k3d.yaml
base_http_url: "http://localhost:18080"
invoke_url: "http://localhost:18080"
base_grpc_url: "localhost:10081"
api_keys_service_url: "http://localhost:18080"

api_keys_host: "api-keys.localhost:18080"
api_host: "api.localhost:18080"
invoke_host: "invocation.localhost:18080"

client_id: "nvcf-default"
```

### Why Host Headers?

The Envoy Gateway uses hostname-based routing to direct traffic to different backend services through a single load balancer. Without the correct `Host` header, the gateway cannot match the request to a route and returns 404.

gRPC does not need host headers because it uses a dedicated TCP listener on port 10081. The gateway routes all traffic on that port directly to the gRPC service without hostname matching.

### Verifying Your Configuration

```bash
nvcf-cli --config .nvcf-cli.local-k3d.yaml init

# If you see 404 on local k3d, verify:
# 1. api.localhost / api-keys.localhost / invocation.localhost resolve on the host
# 2. The k3d load balancer is listening on port 8080
# 3. The API Keys service is healthy: curl -sf http://api-keys.localhost:8080/health
# 4. If using port-forward fallback, api_keys_host/api_host/invoke_host match the HTTPRoute hostnames
```

## Environment Variables

Config keys map to environment variables. Environment variables override config file values.

| Config Key | Environment Variable | Default |
|-----------|---------------------|---------|
| `base_http_url` | `NVCF_BASE_HTTP_URL` | `https://api.nvcf.nvidia.com` |
| `invoke_url` | `NVCF_INVOKE` / `NVCF_BASE_INVOKE_URL` | Same as `base_http_url` |
| `base_grpc_url` | `NVCF_BASE_GRPC_URL` | `grpc.nvcf.nvidia.com:443` |
| `api_keys_service_url` | `API_KEYS_SERVICE_URL` | `https://api-keys.nvcf.nvidia.com` |
| `api_key` | `NVCF_API_KEY` | -- |
| `token` | `NVCF_TOKEN` | -- |
| `client_id` | `NVCF_CLIENT_ID` | `nvcf-default` |
| `api_keys_host` | `API_KEYS_HOST` | -- |
| `api_host` | `API_HOST` | -- |
| `invoke_host` | `INVOKE_HOST` | -- |
| `api_keys_service_id` | `API_KEYS_SERVICE_ID` | deployment-specific API Keys service ID |
| `api_keys_issuer_service` | `API_KEYS_ISSUER_SERVICE` | `nvcf-api` |
| `api_keys_owner_id` | `API_KEYS_OWNER_ID` | `svc@nvcf-api.local` |
| `debug` | `NVCF_DEBUG` | `false` |
| `default_timeout` | `NVCF_DEFAULT_TIMEOUT` | -- |

Note: API Keys and host header environment variables do not use the `NVCF_` prefix.

OAuth2 client credentials (alternative authentication): `NVCF_OAUTH_CLIENT_ID`, `NVCF_OAUTH_CLIENT_SECRET`, `NVCF_OAUTH_TOKEN_ENDPOINT`.

## Multi-Environment Setup

Use separate config files for different environments:

```bash
nvcf-cli --config dev.yaml init
nvcf-cli --config dev.yaml function list

nvcf-cli --config prod.yaml init
nvcf-cli --config prod.yaml function list
```

Each configuration maintains separate state files (e.g., `~/.nvcf-cli.dev.state` for `dev.yaml`).

## Staging Environment

```yaml
base_http_url: "https://api.shqa.stg.nvcf.nvidia.com"
base_grpc_url: "grpc.shqa.stg.nvcf.nvidia.com:443"
api_keys_service_url: "https://api-keys.shqa.stg.nvcf.nvidia.com"
invoke_url: "https://invocation.shqa.stg.nvcf.nvidia.com"
```

## State File

The CLI stores tokens and function context in `~/.nvcf-cli-state.json`. This file is managed automatically -- do not edit it manually.

## Debug Mode

```bash
nvcf-cli --debug function list
NVCF_DEBUG=true nvcf-cli function list
```
