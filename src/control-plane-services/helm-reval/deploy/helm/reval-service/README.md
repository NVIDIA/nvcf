# ReVal Helm chart

Canonical Kubernetes chart for the ReVal HTTP service. It renders `config.yaml` keys that match `github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config.RevalConfig` (Viper / mapstructure). Authorization is performed by the self-hosted `Local` JWKS JWT authorizer (configured under `auth`).

## Install (from this repo path)

```bash
helm upgrade --install reval . -n reval --create-namespace \
  --set reval.serviceConfig.auth.jwt.enabled=true \
  --set reval.serviceConfig.auth.jwt.jwkSetUrl=https://openbao.example.com/v1/identity/oidc/.well-known/jwks.json
```

If neither `auth.jwt.enabled` nor `auth.oidc.enabled` is true, the server starts with auth disabled and logs a warning.

**Image:** Build the workload image with [`docker/Dockerfile`](../../docker/Dockerfile) — just `make container` (optionally with `GITHUB_TOKEN` for private modules).

## Ports

- `http`: main API (`http.api-port`, default 8080)
- `metrics`: Prometheus metrics (`http.metrics-port`, default 8081)
- `management`: health and ops (`http.management-port`, default 8082; probes use `/healthz`)
