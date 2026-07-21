# Helmfile Structure Reference

## Directory Layout

```
nvcf-self-managed-stack/
|-- helmfile.d/
|   |-- 01-dependencies.yaml.gotmpl  # NATS, Cassandra, OpenBao
|   |-- 02-core.yaml.gotmpl          # NVCF services + ingress
|   `-- 03-observability.yaml.gotmpl # Observability stack (optional)
|-- environments/
|   |-- base.yaml                    # Default values (all environments)
|   `-- <env-name>.yaml              # Per-environment overrides
|-- secrets/
|   `-- <env-name>-secrets.yaml      # Sensitive values (registry creds, passwords)
`-- global.yaml.gotmpl               # Go template that constructs per-chart values

nvcf-compute-plane-stack/
|-- helmfile.d/
|   |-- 01-dependencies.yaml.gotmpl  # Compute plane dependency components
|   `-- 02-nvca.yaml.gotmpl          # nvca-operator chart and nvca configuration
|-- environments/
|   |-- base.yaml                    # Default values (all environments)
|   `-- <env-name>.yaml              # Per-environment overrides
`-- global.yaml.gotmpl               # Go template that constructs per-chart values
```

These stacks have separate environments and cluster contexts. The Helmfile
values flow authors both environment files. The CLI profile flow instead uses
the CLI-generated control-plane profile as its endpoint source. Do not mix the
handoffs. See [Split Compute-Plane Installation](compute-plane-installation.md).

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
| llm-request-router | nvcf/helm-nvcf-llm-request-router | nvcf | services |
| llm-api-gateway | nvcf/helm-nvcf-llm-api-gateway | nvcf | services |
| admin-issuer-proxy | helm-admin-token-issuer-proxy | api-keys | (no release-group label) |
| ingress | nvcf-gateway-routes | envoy-gateway-system | ingress |

Most services use `inherit: [{template: service}]`. LLM releases use OCI charts from the `nvcf` repository with standalone `values: [../global.yaml.gotmpl]` blocks; they are gated on `addons.llm.enabled`. `admin-issuer-proxy` and `ingress` have standalone `values:` blocks.

### 03-observability.yaml.gotmpl

| Release | Chart | Namespace | Label |
|---------|-------|-----------|-------|
| (observability releases) | (various) | observability | observability |

Gated on observability-specific flags in the environment file. Skipped if disabled.

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

### LLM Function Enablement

Enable the LLM addon before creating or invoking functions with
`functionType: "LLM"`. The addon deploys `llm-request-router` and
`llm-api-gateway`, adds the `llm.invocation.<domain>` route when Gateway API
ingress is enabled, and configures workers to use the LLM sidecar. The Helmfile
condition is `addons.llm.enabled`. Do not use the obsolete `llm.enabled` path.

For a single-node isolated test cluster, add this configuration to
`environments/<env>.yaml` before applying the stack:

```yaml
addons:
  llm:
    enabled: true
    gateway:
      replicaCount: 1
      auth:
        grpcInsecure: true
      metrics:
        serviceMonitor:
          enabled: false
    requestRouter:
      replicaCount: 1
      metrics:
        serviceMonitor:
          enabled: false
      loadBalancer:
        config: |
          {
            "default": "power-of-two",
            "request_algorithms": {
              "power-of-two": "power-of-two",
              "round-robin": "round-robin",
              "random": "random",
              "groq-multiregion": "groq-multiregion"
            }
          }

agentConfig:
  mergeConfig: |
    cluster:
      validationPolicy:
        name: Unrestricted
    workload:
      stargateQUICInsecure: true
```

Use `replicaCount: 1` only for local or single-node test clusters. For shared
or production clusters, use the required replica count and TLS-capable service
configuration. `addons.llm.gateway.auth.grpcInsecure` and
`workload.stargateQUICInsecure` enable plaintext transports. Do not use them in
production.

The request-router configuration uses hyphenated algorithm IDs, while function
models use underscored `routingMethod` values. Include every non-default
algorithm used by a function in `request_algorithms`, or invocation can fail
with HTTP `400` before a backend is selected.

If the sidecar image is mirrored outside the stack's default image registry and
repository, set the generated worker sidecar image explicitly:

```yaml
api:
  env:
    NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE: <registry>/<repository>/pylon:0.2.1
```

Render and apply the updated control-plane environment, then refresh every
registered compute plane using the same handoff used for its installation so
NVCA receives `agentConfig.mergeConfig`:

```bash
HELMFILE_ENV=<env> helmfile template
HELMFILE_ENV=<env> helmfile sync
```

Use the complete compute-plane install command from
[Split Compute-Plane Installation](compute-plane-installation.md). A
CLI-profile installation must remain profile-driven; a Helmfile installation
must remain values-driven.

Existing LLM function pods keep their existing sidecar arguments. Recreate or
redeploy those functions after the compute-plane refresh. Verify the control
plane, route, and worker sidecar:

```bash
kubectl get deploy -n nvcf llm-api-gateway llm-request-router
kubectl get pods -n nvcf | grep -E 'llm-api-gateway|llm-request-router'
kubectl get httproute -A | grep llm
kubectl -n nvcf-backend get pod <function-pod> \
  -o jsonpath='{range .spec.containers[?(@.name=="llm-worker")].args[*]}{.}{"\\n"}{end}'
```

For local plaintext clusters, the `llm-worker` args must include
`--quic-insecure`.

For sticky LLM routing, `addons.llm.requestRouter.loadBalancer.config` can embed
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
