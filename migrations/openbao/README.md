# NVCF OpenBao Migrations Init Container

Container image and bootstrap scripts that configure an OpenBao (or Vault) instance for NVCF deployments. The container runs as a Kubernetes Job and applies a sequence of numbered shell migrations against a freshly-installed OpenBao server, enabling auth methods, mounting secret engines, and onboarding each NVCF service.

## Overview

This repository ships:

- A container image definition (`Dockerfile`)
- The job entrypoint (`entrypoint.sh`)
- Numbered shell migrations under `migrations/` that run in order against an OpenBao leader
- Helper utilities under `migrations/utils/`
- Optional addons under `addons/` (e.g., LLS / TURN secret rotation)
- An example Kubernetes Job manifest (`job.yaml`)

## Prerequisites

- A reachable OpenBao or Vault deployment (the entrypoint waits for the service to become healthy and locates the leader before applying migrations)
- Kubernetes cluster with a service account that has access to the OpenBao root token secret
- A cluster JWT signing key available at `/secrets/jwt/cluster_jwt.pem` (this is typically provisioned by the OpenBao install pipeline)

### Cluster JWT signing key

The first migration enables JWT auth and binds it to the cluster's issuer. The public signing key must be mounted into the container at `/secrets/jwt/cluster_jwt.pem`.

If you need to extract it manually:

```bash
JWK=$(kubectl get --raw "$(kubectl get --raw /.well-known/openid-configuration | jq -r '.jwks_uri' | sed -r 's/.*\.[^/]+(.*)/\1/')" | jq '.keys[0]' -r)
echo "$JWK" | jwker
```

### Default Cassandra password

Several services need a Cassandra username and password placed into OpenBao so that consumer pods can render credentials at runtime. The migration scripts populate `services/<service>/kv/cassandra/creds` from the `DEFAULT_CASSANDRA_PASSWORD` environment variable.

The shipped `job.yaml` sets a default placeholder value for this variable so the migrations run end-to-end during a fresh install. **This default MUST be overridden in any non-trivial deployment** via your cluster's secret management (External Secrets Operator, Sealed Secrets, Helm `--set` from a sealed source, etc.). Leaving the default in production is a security risk.

## Building the container

The `Dockerfile` uses the public upstream OpenBao image (`openbao/openbao:2.5.1`) as the base. To use a different base, edit the `FROM` line directly.

```bash
docker build \
  --build-arg KUBECTL_VERSION=v1.31.0 \
  -t <your-registry>/<your-org>/openbao-migrations:<version> .
```

## Running the migrations

The container expects the following at runtime:

| Path | Source | Purpose |
|---|---|---|
| `/secrets/root_token/root_token` | Kubernetes secret | OpenBao root token used by `bao` CLI inside the container |
| `/secrets/jwt/cluster_jwt.pem` | Kubernetes secret | Cluster JWT signer's PEM, used to bind the JWT auth method |

Environment variables consumed by the entrypoint:

| Variable | Default | Purpose |
|---|---|---|
| `BAO_SERVICE` | `openbao-server.vault-system.svc.cluster.local` | DNS name of the OpenBao Service |
| `CORE_MIGRATIONS_ENABLED` | `true` | Run the numbered scripts under `migrations/` |
| `ADDONS_LLS_ENABLED` | `false` | Run the LLS addon under `addons/lls/setup_lls.sh` (fail-hard when enabled — see [addons/lls/README.md](addons/lls/README.md)) |
| `ADDONS_LLM_ENABLED` | `false` | Run the LLM addon under `addons/llm/setup_llm.sh` (fail-hard when enabled — see [addons/llm/README.md](addons/llm/README.md)) |
| `DEFAULT_CASSANDRA_PASSWORD` | `ch@ng3m3` (override required) | See above |
| `NVCF_API_SIDECARS_IMAGE_PULL_SECRET` | `""` | Image pull secret name passed to the NVCF API sidecar mount |
| `MIGRATIONS_ALLOW_FAILURES` | `false` | Emergency rollback only. When `true`, the entrypoint exits 0 even if core migrations or opted-in addons failed. Default behavior fails the Job non-zero so a misconfigured deployment blocks the Helm hook Job instead of silently leaving OpenBao in a partial state. |

### Example Kubernetes Job

`job.yaml` is a minimal example showing how to schedule the container. Update the image reference, override the password, and apply:

```bash
kubectl apply -f job.yaml
```

## Service onboarding

Each NVCF service has its own `migrations/NN_setup_<service>.sh` script. A new service is onboarded by:

1. Adding a `migrations/NN_setup_<service>.sh` file (where `NN` is the next available number).
2. Using the helpers in `migrations/utils/functions.sh` to enable the service's JWT issuer mount, KV mount, and policies.
3. Choosing a service name and DNS that match the chart deployment naming. The convention is `<namespace>-<service-type>` for the service account, with the service available at `<service-type>.<namespace>.svc.cluster.local`.

The service-DNS values that the migrations configure are tied to NVCF's chart-default service naming. If you deploy the NVCF charts with non-default namespaces or service names, override the relevant issuer / JWKS URLs in the migration scripts before building your container.

## JWT authentication

The first migration enables a `jwt/` mount and binds it to the cluster issuer. Pods authenticate to OpenBao using their projected service account token, and the migration scripts create per-service roles and policies that grant the appropriate read paths.

## Notes

- The `migrations/*.sh` scripts include `# Issuer:` comment lines documenting the in-cluster service URLs each migration configures. These match NVCF's chart-default service DNS; override them if your deployment uses different names.
- The example image reference in `job.yaml` is a placeholder. Set it to the registry and tag where you publish your built container.
