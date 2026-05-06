---
name: nvcf-self-managed-cli
description: >-
  Manage NVIDIA Cloud Functions on self-managed deployments via the nvcf-cli.
  Create, deploy, invoke, and delete functions; manage API keys and registry
  credentials. Use when working with self-managed NVCF, self-hosted cloud
  functions, nvcf-cli, function deployment, invocation, API key generation, or
  registry credentials.
license: Apache-2.0
compatibility: Requires nvcf-cli binary installed and configured with .nvcf-cli.yaml
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, self-managed, self-hosted, cli, invocation]
tools: [Shell, Read, Write]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0"
  tags: [nvcf, self-managed, self-hosted, cloud-functions, cli, deployment, invocation, api-key, registry, function]
  languages: [bash]
  frameworks: [nvcf-cli]
  domain: cloud-infrastructure
---

# NVCF Self-Managed CLI Skill

Manage NVIDIA Cloud Functions on self-managed deployments via `nvcf-cli`.

## Instructions

- Use this skill for self-managed `nvcf-cli` work: config selection, authentication, function lifecycle operations, registry credential management, and local k3d validation.
- Prefer a local `nvcf-cli` source checkout over a downloaded binary when one is available nearby; update `main`, reinstall, and verify `nvcf-cli version` before testing.
- Always pass `--config` explicitly and treat `status` as a config-path check, not the source of truth for local endpoint/token rendering.
- For local k3d setups, choose between direct `.localhost` routes and gateway port-forwarding based on what is actually reachable; if both appear plausible and local context does not decide, ask the user which model they use.
- Use `init` and `api-key generate` to verify auth before function operations. If sandboxed state writes fail, use a writable temporary `HOME` with a copied config file.
- Keep all NVCF management operations on the CLI path; do not bypass the CLI with direct REST calls unless the user explicitly asks for direct invocation.

## Before You Start

### Always Use `--config`

The CLI resolves its config file from the current working directory or home directory. Agents typically run from the workspace root, **not** the directory containing `.nvcf-cli.yaml`, so the CLI will silently fall back to cloud-hosted defaults and fail. Always pass `--config` explicitly on every command:

```bash
nvcf-cli --config /path/to/.nvcf-cli.yaml status
```

At the start of a session, ask the user where their config file is (or search for it), then use that path for all subsequent commands.

Also ask the user if they already have the `nvcf-cli` binary downloaded and, if so, where it is located. Use that explicit path (e.g., `./nvcf-cli` or `/home/user/bin/nvcf-cli`) for all subsequent commands rather than assuming it is on `$PATH`. If they do not have it yet, proceed with the download steps in the [Prerequisites](#prerequisites) section below.

If you are working in a local dev checkout, also check adjacent folders for an `nvcf-cli` repo before downloading a new binary. Prefer that checkout's `nvcf-cli` binary and config template when present.

When an adjacent source checkout exists, update it before testing the local stack:

```bash
cd /path/to/cli
git pull --ff-only origin main
make install
nvcf-cli version
```

Use the installed binary from that refreshed checkout for all subsequent commands. Only fall back to the NGC-distributed binary when no source checkout is available.

### Check Status

Run `nvcf-cli status` **once** at the beginning of a session to confirm which config file the CLI is using. Report that path to the user. After that, do not re-run it -- remember the result for the rest of the session.

Do not rely on `status` alone for endpoint or token truth on local self-hosted setups. In the validated local flow, `status` still showed `(not set)` for endpoints and tokens even though `--config`, `init`, `api-key generate`, `function create`, `deploy`, and `invoke` all worked.

### Do Not Modify CLI Configuration

**Never** change the CLI configuration file (`.nvcf-cli.yaml`) on behalf of the user. This includes editing endpoints, credentials, host headers, or any other settings. Only the user may change their configuration explicitly.

If the user needs to target a different environment, suggest using the `--config` flag:

```bash
nvcf-cli --config staging.yaml function list
```

### Verify Authentication

Before performing any operations, confirm that authentication works with real commands. If tokens are missing or expired:

```bash
nvcf-cli init
nvcf-cli api-key generate
```

If `init` fails, **stop and help the user resolve the configuration** before proceeding. Do not attempt function operations -- they will fail with 401/403 errors.

In sandboxed agent environments, state writes to `~/.nvcf-cli.state` or `~/.nvcf-cli-state.json` may fail even when the CLI can still reach the cluster. When that happens, use an isolated writable `HOME` with a copied config file rather than assuming the endpoints are broken:

```bash
tmp_home=$(mktemp -d)
cp /path/to/.nvcf-cli.yaml "$tmp_home/.nvcf-cli.yaml"
HOME="$tmp_home" nvcf-cli --config "$tmp_home/.nvcf-cli.yaml" init
HOME="$tmp_home" nvcf-cli --config "$tmp_home/.nvcf-cli.yaml" api-key generate
```

### Do Not Bypass the CLI

All NVCF management operations must go through `nvcf-cli`. Do not call NVCF REST APIs directly (via curl, Python requests, etc.) to work around CLI errors. If a CLI command fails, stop and report the issue to the user.

**Exception:** Direct HTTP invocation via curl is acceptable when the user explicitly requests it and has `NVCF_API_KEY` set. See [Invocation Reference](references/invocation.md).

## Prerequisites

### Prefer a Local Source Checkout

If the user already has the CLI source repo checked out locally, use that copy first:

```bash
cd /path/to/cli
git pull --ff-only origin main
make install
nvcf-cli version
```

This is the preferred path for local self-hosted testing because docs and committed source can diverge from an older downloaded binary.

### Download nvcf-cli from NGC

The `nvcf-cli` is published as an NGC resource under the `0833294136851237/nvcf-ncp-staging` org/team. You need the NGC CLI installed and an NGC API key with access to this org.

```bash
# 1. Authenticate to NGC (org: 0833294136851237, team: nvcf-ncp-staging)
export NGC_API_KEY="<your-ngc-api-key>"
printf '%s\n' "$NGC_API_KEY" "json" "0833294136851237" "nvcf-ncp-staging" | ngc config set

# 2. Find the latest version
ngc registry resource list 0833294136851237/nvcf-ncp-staging/nvcf-cli --format_type json
# Look for "latestVersionIdStr" in the output (e.g., "0.0.23")

# 3. Download (replace 0.0.23 with the latest version from step 2)
ngc registry resource download-version 0833294136851237/nvcf-ncp-staging/nvcf-cli:0.0.23

# 4. Extract and install (pick your platform: darwin-arm64, darwin-amd64, linux-amd64, linux-arm64)
tar -xzf nvcf-cli_v0.0.23/darwin-arm64/nvcf-cli-darwin-arm64-0.0.23.tar.gz
cp nvcf-cli-darwin-arm64-0.0.23/nvcf-cli ./nvcf-cli && chmod +x ./nvcf-cli

# 5. Confirm
./nvcf-cli --help
```

### Configure and Authenticate

1. **Copy configuration template**: `cp .nvcf-cli.yaml.template .nvcf-cli.yaml`
2. **Configure for your environment**: See [Configuration Reference](references/configuration.md)
3. **Initialize authentication**: `nvcf-cli init && nvcf-cli api-key generate`

For local self-hosted testing, prefer creating a fresh dedicated config such as `.nvcf-cli.local-k3d.yaml` rather than reusing an existing ELB or remote self-hosted config. Two local access models are valid:

- Direct `.localhost` routes exposed by the k3d load balancer, such as `http://api.localhost:8080`, `http://api-keys.localhost:8080`, and `http://invocation.localhost:8080`
- Port-forwarding the gateway service to a local port and using host-header overrides

Prefer the direct `.localhost` model when those endpoints are reachable because it is simpler and was validated on the default `ncp-local` stack. If the local setup is clearly using a gateway port-forward instead, keep using that model. If both are plausible and local context does not disambiguate, ask the user which model their cluster uses before changing course.

If `.localhost` DNS fails during local k3d testing, do not assume the stack is broken. Use `127.0.0.1:<HTTP_PORT>` for transport while preserving the Envoy route host headers:

```yaml
base_http_url: "http://127.0.0.1:8080"
invoke_url: "http://127.0.0.1:8080"
base_grpc_url: "127.0.0.1:10081"
api_keys_service_url: "http://127.0.0.1:8080"

api_keys_host: "api-keys.localhost"
api_host: "api.localhost"
invoke_host: "invocation.localhost"

api_keys_service_id: "nvidia-cloud-functions-ncp-service-id-<suffix>"
api_keys_issuer_service: "nvcf-api"
api_keys_owner_id: "svc@nvcf-api.local"

client_id: "nvcf-default"
```

This pattern is useful in sandboxed agent sessions and on hosts where `api-keys.localhost` does not resolve, while the k3d load balancer is still reachable on `127.0.0.1:8080`.

Use the API Keys service ID from the stack's generated CLI config, such as `nvcf-cli-local.yaml`, or from the target deployment's API Keys metadata. Do not copy the `<suffix>` placeholder verbatim.

## Configuration

Config files are searched in this order (highest priority first):

1. Explicit path via `--config` flag
2. Current directory: `./.nvcf-cli.yaml`
3. Home directory: `~/.nvcf-cli.yaml`

For self-hosted deployments, the CLI must communicate with your Envoy Gateway. The gateway uses hostname-based routing, so **host header overrides** are required:

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

For production with DNS/HTTPS, host header overrides are not needed. See [Configuration Reference](references/configuration.md) for full details including environment variables, multi-environment setup, and staging endpoints.

## Authentication

The CLI uses two token types:

| Token | Env Var | Purpose | Command |
|-------|---------|---------|---------|
| Admin Token (JWT) | `NVCF_TOKEN` | create, deploy, update, delete | `nvcf-cli init` |
| API Key | `NVCF_API_KEY` | invoke, list, queue status | `nvcf-cli api-key generate` |

```bash
nvcf-cli init                  # generate admin token (clears ALL state including API key)
nvcf-cli refresh               # refresh admin token (preserves state)
nvcf-cli api-key generate      # generate API key (default: 24h)
```

**Important:** `init` clears all saved state, including the API key. You **must** run `api-key generate` again after every `init`, otherwise invocations will fail with 403 Forbidden. Prefer `refresh` over `init` when you only need to renew the admin token.

See [API Keys Reference](references/api-keys.md) for scopes, expiration, and key management.

## Quick Start

```bash
nvcf-cli init
nvcf-cli api-key generate
nvcf-cli function create --input-file function.json
nvcf-cli function deploy create --input-file deploy.json
nvcf-cli function invoke --request-body '{"message": "hello world"}'
nvcf-cli function deploy remove
nvcf-cli function delete
```

After `function create`, the CLI attempts to save the function ID and version ID to `~/.nvcf-cli-state.json`. However, state persistence is unreliable -- it can fail silently due to permission issues, `--config` usage, or working directory differences. **Always capture the function ID and version ID from the `create` output and pass them explicitly with `--function-id` and `--version-id` on all subsequent commands.** Do not rely on automatic state resolution.

For local k3d validation, use a disposable function name and an explicit create -> deploy get -> invoke flow. If no functions exist yet, a Helm-chart-based sample is a good smoke test because it exercises both registration and deployment without first building a custom image.

If your function config includes a health check, include an explicit health timeout too. The validated local CLI flow required `health.timeout` / `--health-timeout PT10S` when `healthUri` was set.

## Command Structure

```bash
nvcf-cli [--config <path>] [--debug] <command> [subcommand] [flags]
```

## Troubleshooting

```bash
nvcf-cli --debug function list
```

| Symptom | Fix |
|---------|-----|
| 401 Unauthorized | `nvcf-cli init --debug` |
| 403 Forbidden | `nvcf-cli api-key generate --validate` |
| Token expired | `nvcf-cli refresh` then `nvcf-cli api-key generate` |
| 404 on self-hosted | Verify host headers match HTTPRoute hostnames. See [Configuration Reference](references/configuration.md). |
| `lookup api-keys.localhost: no such host` or similar local DNS failure | For k3d, keep `api_keys_host`, `api_host`, and `invoke_host` on the `.localhost` route names, but set transport URLs such as `base_http_url`, `invoke_url`, and `api_keys_service_url` to `http://127.0.0.1:8080`. |
| `status` shows endpoints or tokens as `(not set)` on local self-hosted | Treat `status` as a config-path check, not a source of truth for local endpoint/token rendering. Verify with `--debug init`, `--debug api-key generate`, or a real function command. |
| `failed to write state file` or `read-only file system` | Use a writable temporary `HOME` with a copied config file, then re-run `init` and `api-key generate`. |
| `failed to create function: health.timeout is required when health is specified` | Add `health.timeout` in the input JSON or pass `--health-timeout PT10S` alongside `--health-uri`. |
| Function stuck in `DEPLOYING` | Three possible causes: (1) health check path mismatch — verify `health.uri` matches the path your container actually serves at startup; (2) container crash — run `kubectl logs -n nvcf-backend <pod>` to check for startup errors; (3) GPU capacity exhausted — check nvcf-api logs for `"Max instance count reached"` and remove unused deployments with `nvcf-cli function deploy remove`. |
| `Invalid GPU '<name>' specified` or `Invalid InstanceType` | The CLI defaults to `H100`/`NCP.GPU.H100_1x`. Run `kubectl get nodes -l nvidia.com/gpu.product -o custom-columns='NAME:.metadata.name,GPU:.metadata.labels.nvidia\.com/gpu\.product,COUNT:.status.capacity.nvidia\.com/gpu'` to find your cluster's GPU type, then pass `--gpu <type> --instance-type NCP.GPU.<type>_<count>x`. See [Deployment Reference](references/deployment.md#discovering-available-gpus-and-building-the-deployment-spec). |
| `Missing CONTAINER registry credential for hostname '<hostname>'` | No credential is registered for the container image's registry hostname. Register one with `nvcf-cli registry add --hostname <hostname> --username <user> --password <token> --artifact-type CONTAINER`. If a credential already exists, update it: `nvcf-cli registry update <credential-id> --username <user> --password <token>`. Verify with `nvcf-cli registry list --artifact-type CONTAINER`. For AWS ECR registries, do not use temporary tokens from `aws ecr get-login-password` -- they expire in 12 hours. Instead create a dedicated IAM user with ECR read access and use its long-lived access keys. See [AWS ECR Credentials](references/registry.md#aws-ecr-credentials). |

## Reference Docs

- [Functions](references/functions.md) -- create, list, get, update, delete
- [Deployments](references/deployment.md) -- deploy, scale, remove
- [Invocation](references/invocation.md) -- REST, gRPC, curl, queue monitoring
- [API Keys](references/api-keys.md) -- generate, list, revoke, scopes
- [Registry](references/registry.md) -- add, list, update, delete credentials
- [Configuration](references/configuration.md) -- endpoints, env vars, multi-env, staging
