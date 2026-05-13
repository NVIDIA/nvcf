# Helmfile Structure Reference

## Directory Layout

```
nvcf-self-managed-stack/
├── helmfile.d/
│   ├── 000-prepare.yaml.gotmpl      # Validation hooks
│   ├── 01-dependencies.yaml.gotmpl  # NATS, Cassandra, OpenBao
│   ├── 02-core.yaml.gotmpl          # NVCF services + ingress
│   ├── 03-observability.yaml.gotmpl # Observability stack (optional)
│   └── 04-worker.yaml.gotmpl        # NVCA operator (opt-in via nvcaOperator.enabled)
├── environments/
│   ├── base.yaml                    # Default values (all environments)
│   └── <env-name>.yaml              # Per-environment overrides
├── secrets/
│   └── <env-name>-secrets.yaml      # Sensitive values (registry creds, passwords)
└── global.yaml.gotmpl               # Go template that constructs per-chart values
```

## Gotmpl Files and Their Releases

### 01-dependencies.yaml.gotmpl

| Release | Chart | Namespace | Notes |
|---------|-------|-----------|-------|
| nats | helm-nvcf-nats | nats-system | Messaging |
| openbao-server | helm-nvcf-openbao-server | vault-system | Secrets management, depends on nats |
| cassandra | helm-nvcf-cassandra | cassandra-system | Database |

Uses `<<: *dependency` template inheritance with `release-group: dependencies` label.

### 02-core.yaml.gotmpl

| Release | Chart | Namespace | Label |
|---------|-------|-----------|-------|
| api-keys | helm-nvcf-api-keys | api-keys | services |
| sis | helm-nvcf-sis | sis | services |
| api | helm-nvcf-api | nvcf | services |
| invocation-service | helm-nvcf-invocation-service | nvcf | services |
| grpc-proxy | helm-nvcf-grpc-proxy | nvcf | services |
| ess-api | helm-nvcf-ess-api | ess | services |
| notary-service | helm-nvcf-notary-service | nvcf | services |
| reval | helm-reval | nvcf | services |
| llm-request-router | ../../llm-request-router-colocated-deploy/chart | nvcf | services |
| llm-api-gateway | ../../llm-api-gateway-colocated-deploy/chart | nvcf | services |
| admin-issuer-proxy | helm-admin-token-issuer-proxy | api-keys | (no release-group label) |
| ingress | nvcf-gateway-routes | envoy-gateway-system | ingress |

Most services use `inherit: [{template: service}]`. In MR !183 the LLM releases use explicit sibling chart paths and `values: [../global.yaml.gotmpl]` until OCI chart releases are pinned in a follow-up. `admin-issuer-proxy` and `ingress` have standalone `values:` blocks.

### 03-observability.yaml.gotmpl

| Release | Chart | Namespace | Label |
|---------|-------|-----------|-------|
| (observability releases) | (various) | observability | observability |

Gated on observability-specific flags in the environment file. Skipped if disabled.

### 04-worker.yaml.gotmpl

| Release | Chart | Version | Namespace | Label | Condition |
|---------|-------|---------|-----------|-------|-----------|
| nvca-operator | `nvcf/helm-nvca-operator` | 1.6.6 | nvca-operator | workers | `nvcaOperator.enabled` |

Standalone `values:` block with explicit image path construction. The release is **opt-in**: helmfile sets `nvcaOperator.enabled: false` as its default, and `environments/local.yaml` (plus any other env that needs functions) enables it with:

```yaml
nvcaOperator:
  enabled: true
```

Without this override the release is skipped at sync time and no Deployment is created.

## Template Inheritance

### `<<: *dependency` (YAML merge)

Used in `01-dependencies.yaml.gotmpl`. Merges the template's properties into the release.

**Gotcha**: YAML merge replaces lists. If you add a `values:` key to the release, it **replaces** the template's `values:` list entirely. You must re-include all template values:

In the template below, `<private-values>` refers to the `secrets/` directory at the helmfile stack root.

```yaml
# Template defines:
templates:
  dependency: &dependency
    chart: nvcf/helm-nvcf-{{ .Release.Name }}
    values:
      - ../global.yaml.gotmpl
      - ../<private-values>/{{ requiredEnv "HELMFILE_ENV" }}-secrets.yaml

# When overriding, MUST re-include both:
- name: cassandra
  <<: *dependency
  values:
    - ../global.yaml.gotmpl                                              # Must re-include
    - ../<private-values>/{{ requiredEnv "HELMFILE_ENV" }}-secrets.yaml   # Must re-include
    - cassandra:                                                          # Your override
        resources:
          limits:
            memory: 8192Mi
```

### `inherit` (Helmfile native)

Used in `02-core.yaml.gotmpl`. Helmfile's native inheritance mechanism.

```yaml
- name: api
  version: 1.6.0
  namespace: nvcf
  inherit:
    - template: service
```

When adding `values:` to an inherited release, you also need to re-include the template's values files since `values` is a list that gets replaced.

## Values Precedence

From lowest to highest priority:

1. `environments/base.yaml` -- defaults shared across all environments
2. `environments/<env>.yaml` -- per-environment overrides
3. `global.yaml.gotmpl` -- Go template processing (constructs chart-specific structure)
4. `<private-values>/<env>-secrets.yaml` -- sensitive values
5. Inline `values:` blocks on releases -- highest precedence

## What global.yaml.gotmpl Passes Through

`global.yaml.gotmpl` reads from `.Values` (the merged environment + env-specific YAML) and constructs chart-specific values. It only passes through keys it explicitly references:

### Cassandra
- `cassandra.replicaCount`
- `cassandra.image.*` (registry, repository)
- `cassandra.migrations.image.*`
- `cassandra.persistence.size`
- `cassandra.nodeSelector` (if `global.nodeSelectors.enabled`)
- `cassandra.global.defaultStorageClass`

### NATS
- `nats.container.image.*`
- `nats.reloader.image.*`
- `nats.natsBox.container.image.*`
- `nats.config.jetstream.fileStore.pvc.storageClassName`
- `nats.podTemplate.merge.spec.nodeSelector` (if enabled)

### OpenBao
- `openbao.migrations.image.*` and `openbao.migrations.env`
- `openbao.injector.image.*`
- `openbao.server.image.*`
- `openbao.server.dataStorage.*`
- Node selectors (if enabled)

### Services (API, SIS, etc.)
- `<service>.image.*` (registry, repository)
- `<service>.nodeSelector` (if enabled)
- `<service>.env.*` (observability settings)

### LLM API Gateway / LLM Request Router
- `llm.enabled`
- `llm.gateway.replicaCount`
- `llm.gateway.auth.grpcInsecure`
- `llm.requestRouter.replicaCount`
- `llm.requestRouter.loadBalancer`
- `ingress.gatewayApi.routes.llmInvocation.routeAnnotations`
- Global image registry/repository, image pull secrets, node selectors, and tracing settings are mapped into the LLM chart values.

**Not passed through**: `llm.gateway.image.*`, `llm.requestRouter.image.*`, `resourcePreset`, `resources`, or any other arbitrary key. These must be set via `global.yaml.gotmpl` or release inline `values:` blocks.

For sticky LLM routing, `llm.requestRouter.loadBalancer.config` can embed
Stargate JSON. The public gateway header is `x-multi-turn-session-id`, but
Stargate only consumes the internal `x-cache-affinity-key`. Sticky backend
selection requires a cache-affinity-aware algorithm: `groq-multiregion` with
`cache_affinity_backend_selection_count > 0`, or `pulsar` with backend KV
metrics and capacity values. `power-of-two`, `round-robin`, and `random` do not
provide multi-turn session stickiness.

If a model config sets `require_cache_affinity_key: true`, direct router
validation calls must include `x-cache-affinity-key`. Gateway clients should
still use only `x-multi-turn-session-id`; the gateway supplies the router
affinity header.

## Helmfile Selectors

Target specific releases or groups:

```bash
# By release group
HELMFILE_ENV=<env> helmfile --selector release-group=dependencies sync
HELMFILE_ENV=<env> helmfile --selector release-group=services sync
HELMFILE_ENV=<env> helmfile --selector release-group=ingress sync
HELMFILE_ENV=<env> helmfile --selector release-group=observability sync
HELMFILE_ENV=<env> helmfile --selector release-group=workers sync   # no-op unless nvcaOperator.enabled=true

# By release name
HELMFILE_ENV=<env> helmfile --selector name=cassandra sync
HELMFILE_ENV=<env> helmfile --selector name=admin-issuer-proxy sync
HELMFILE_ENV=<env> helmfile --selector name=llm-request-router sync
HELMFILE_ENV=<env> helmfile --selector name=llm-api-gateway sync

# Template only (dry run)
HELMFILE_ENV=<env> helmfile --selector name=cassandra template

# Destroy a single release
HELMFILE_ENV=<env> helmfile --selector name=cassandra destroy
```

Note: `admin-issuer-proxy` has no `release-group` label. Use `--selector name=admin-issuer-proxy`.
