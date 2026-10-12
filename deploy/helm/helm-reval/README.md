# ReVal Helm chart

Canonical Kubernetes chart for the ReVal HTTP service. It renders `config.yaml` keys that match `github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config.RevalConfig` (Viper and mapstructure). Authorization supports the self-managed JWKS JWT authorizer and OIDC token introspection.

## Install (from this repo path)

```bash
helm upgrade --install reval . -n reval --create-namespace
```

The default values enable both authorizers with the self-managed OpenBao and ICMS service endpoints. Override `reval.serviceConfig.auth.jwt.jwkSetUrl` and `reval.serviceConfig.auth.oidc.introspectUrl` when those services use different addresses. Keep at least one authorizer enabled. ReVal rejects requests when neither authorizer is configured.

Image: Build the workload image with [`docker/Dockerfile`](../../docker/Dockerfile) by running `make container`. Set `GITHUB_TOKEN` if private modules require it.

## Ports

- `http`: main API (`http.api-port`, default 8080)
- `metrics`: Prometheus metrics (`http.metrics-port`, default 8081)
- `management`: health and ops (`http.management-port`, default 8082; probes use `/healthz`)
