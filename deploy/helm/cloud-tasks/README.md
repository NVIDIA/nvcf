# NVCT API (nvct-service) deployment

This repository contains a Helm chart for deploying nvct-service (NVCT API) in colocated / self-managed environments.

## Prerequisites

- Kubernetes cluster with Helm installed
- Access to the container registry that hosts the nvct-service image
- OpenBao / Vault Agent Injector configured for JWT auth; service account and policies must match `nvcf-openbao-migrations` (`migrations/18_setup_nvct.sh`)

## Deployment

The Kubernetes `ServiceAccount` name **must** be `nvct-api` and the pod namespace **must** match the JWT role bound in OpenBao (default from migrations: namespace `nvcf`).

```bash
helm install nvct-api ./nvct-api --namespace nvcf --create-namespace \
  --values nvct-api/values.yaml \
  --values values.local.yaml
```

Use `make template` or `make lint` to render and validate the chart locally.

## Configuration

- Image registry, repository, and tag: `nvct-api/values.yaml` under `nvctApi.image`
- Runtime environment: `nvctApi.env` (Spring relaxed binding / `NVCT_*`, `SPRING_*`, `KAIZEN_*`, etc.)
- Vault-injected secrets template: `nvct-api/vault-agent-templates/secrets.json.tmpl` (paths under `/services/nvct-service/kv/...` and shared `services/all/...` keys)

## Remote Config RBAC

`spring-cloud-kubernetes` (v3.3.0, shipped in nvct-service `1.4.x`) calls `listNamespacedConfigMap` without a `fieldSelector=metadata.name` filter and matches the target name in memory. Because the API request is unfiltered, the Role must grant `list`/`watch` on the configmaps collection itself — no `resourceNames` scoping for those verbs — so the `nvct-api` SA can read every ConfigMap in the namespace. Chart assumes none are sensitive; clusters that block broad namespace reads need a policy exception.

Set `nvctApi.remoteConfig.enabled: false` to opt out: the chart no longer renders the broad RBAC, and the service runs on JAR sidecar defaults in `application-ncp.yaml`. The SA keeps default-token automount on (the K8s default) so the vault-k8s injector's fallback service-account discovery continues to find a mount. Hot reload is unavailable.

## Troubleshooting Steps

```bash
kubectl get pods -n nvcf -l app.kubernetes.io/name=helm-nvcf-nvct-api
kubectl logs -n nvcf deployment/<release-name>-helm-nvcf-nvct-api
```

Health endpoints are on the management port (default `8181`): `/actuator/health/liveness` and `/actuator/health/readiness`.
