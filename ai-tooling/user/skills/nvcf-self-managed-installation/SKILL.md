---
name: nvcf-self-managed-installation
description: >-
  Install and operate NVCF self-hosted control-plane and separate compute-plane
  stacks. Covers Helmfile values and CLI profile installation flows, teardown,
  values overrides, pull secrets, and troubleshooting. Use for
  nvcf-self-managed-stack, nvcf-compute-plane-stack, split compute-plane
  installation, control-plane installation, CLI-generated control-plane
  profiles, Helmfile, self-managed, or self-hosted deployments. Do NOT use for
  local k3d environments; use the local k3d development workflow instead.
license: Apache-2.0
compatibility: Requires helmfile >= 1.1.0 < 1.2.0, helm >= 3.12, helm-diff plugin, kubectl matching cluster version
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, self-managed, helmfile, self-hosted, control-plane, compute-plane, cli-profile, installation, deployment, pull-secrets]
tools: [Shell, Read, Edit, Grep, Glob]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0"
  tags: [nvcf, self-managed, helmfile, self-hosted, control-plane, compute-plane, cli-profile, installation, deployment, pull-secrets]
  languages: [bash, yaml]
  frameworks: [helmfile, helm, kubectl]
  domain: cloud-infrastructure
---

# NVCF Self-Managed Stack Operations

Operational guide for matching NVCF control-plane and compute-plane Helmfile bundles.

## Instructions

Use this skill for install, upgrade, or teardown work in matching `nvcf-self-managed-stack` and `nvcf-compute-plane-stack` bundles; keep Helmfile values and CLI profile handoffs separate. For `functionType: "LLM"`, read [LLM Function Enablement](references/helmfile-structure.md#llm-function-enablement).

## Prerequisites

Ask for the extracted `nvcf-self-managed-stack` path. For split installation,
also require the matching-version `nvcf-compute-plane-stack` path. Verify both:

```bash
ls <control-plane-stack>/helmfile.d/ <control-plane-stack>/environments/ <control-plane-stack>/secrets/ <control-plane-stack>/global.yaml.gotmpl
ls <compute-plane-stack>/helmfile.d/ <compute-plane-stack>/environments/ <compute-plane-stack>/global.yaml.gotmpl
```

If either directory is missing, download matching bundle versions from NGC:

```bash
ngc registry resource download-version <org>/nvcf-self-managed-stack:<version>
ngc registry resource download-version <org>/nvcf-compute-plane-stack:<version>
```

Control-plane commands below assume the control-plane bundle root. Split-flow
commands and root substitution are in
[Split Compute-Plane Installation](references/compute-plane-installation.md).

## Before You Start

Verify tooling and context before any operation:

```bash
helmfile --version   # Must be 1.1.x (1.2.0 removed sequential mode)
helm version         # Must be >= 3.12
helm plugin list     # Must include helm-diff >= 3.11
kubectl version      # Client must be within 1 minor version of cluster
```

Control-plane commands run from the extracted `nvcf-self-managed-stack/` root:

```bash
cd path/to/nvcf-self-managed-stack
ls helmfile.d/ environments/ secrets/ global.yaml.gotmpl
```

Identify the environment from `environments/<name>.yaml` and
`secrets/<name>-secrets.yaml`; for EKS, use the canonical
[CSP End-to-End Example](https://docs.nvidia.com/nvcf/v0.6.0-rc/csp-end-to-end-example).

## How Values Flow

Understanding value precedence prevents the most common configuration mistakes.

```
environments/base.yaml          (defaults)
    -> merged with
environments/<env>.yaml         (your overrides)
    -> consumed by
global.yaml.gotmpl              (Go template, constructs per-chart values)
    -> consumed by
secrets/<env>-secrets.yaml      (sensitive values)
    -> overridden by
release inline values: blocks   (highest precedence)
```

Critical: `global.yaml.gotmpl` only passes through specific keys to each chart (image registries, node selectors, storage, replica counts). Setting an arbitrary chart value in the environment file will be silently ignored if `global.yaml.gotmpl` does not propagate it.

To override arbitrary chart values, use a helmfile release `values:` block. See [Overriding Helm Chart Values](#overriding-helm-chart-values).

## Clean Installation

### 1. Create namespaces and image pull secrets (if using a private registry)

```bash
for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system cert-manager; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done

# Only if pulling from a private registry (e.g., NGC nvcr.io)
export NGC_API_KEY="<your-key>"
for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system cert-manager; do
  kubectl create secret docker-registry nvcr-creds \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

If using pull secrets, you must also configure each helmfile release to reference them. See [Image Pull Secrets](#image-pull-secrets).

### 2. Authenticate helm to your chart registry

Both `docker login` AND `helm registry login` are required for NGC. Helmfile uses helm (not docker) to pull OCI charts, so the helm credential store must be authenticated separately.

```bash
# NGC -- both commands are required
docker login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"
helm registry login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"

# ECR
aws ecr get-login-password --region <region> | \
  helm registry login --username AWS --password-stdin <account>.dkr.ecr.<region>.amazonaws.com
```

> Note: `helm registry login` keeps only the last login per host (multi-org gotcha).
> The stack and the caches span several NGC orgs that need different keys, but they all
> live under the one host `nvcr.io`. A second `helm registry login nvcr.io` (or
> `docker login`) with a different key silently overwrites the first, so pulls for the
> first org then fail with a misleading `403 Access Denied` (not a 401: the auth
> succeeded, it's the wrong-org key). If you pull charts/images from more than one org,
> either re-run the login with the correct key immediately before each pull, or pass
> `--username '$oauthtoken' --password <key>` per `helm pull` invocation instead of relying
> on the stored login. (Pulling everything from a single registry mirror avoids this
> entirely.)

### 2b. (Recommended) Pull from a registry the cluster authenticates to automatically

If you mirror the NVCF images into a registry that the cluster can pull from without an
explicit image pull secret (for example, a registry where the node or workload identity is
granted pull access automatically), point the stack at it and skip the credential steps
above.

1. Point the stack at the mirror in `environments/<env>.yaml`:

   ```yaml
   global:
     image:
       registry: <your-registry>            # e.g. registry.example.com
       repository: <mirror-sub-path>
     helm:
       sources:                              # only if you also mirrored the helm charts
         registry: <your-registry>
         repository: <mirror-sub-path>
   ```

   > The manifest has a mix of OCI and non-OCI artifacts, so convert non-OCI charts before
   > mirroring. Container images (`nvcr.io/...`) are already OCI and mirror as-is. Some Helm
   > charts, though, are published to an `https://` Helm repository (for example
   > `helm.ngc.nvidia.com`). Those are not OCI and cannot be resolved by `global.helm.sources`
   > (OCI) or mirrored directly. Before pointing the stack at your mirror, convert each
   > `https://` chart to OCI: `helm repo add`/`helm pull` the `.tgz`, then
   > `helm push <chart>.tgz oci://<your-registry>/<mirror-sub-path>`. Only OCI charts can live
   > under `global.helm.sources`.

2. Skip the credential steps above. When the cluster authenticates to the registry
   automatically (the node or workload identity has pull access), the pull needs no
   Kubernetes secret, so you do not need: the per-namespace `nvcr-pull-secret` from step 1
   (still create the namespaces, just not the secret), the `global.imagePullSecrets` entry,
   or the `docker login`/`helm registry login nvcr.io` from step 2.

   > One residual credential: nvcf-api still validates the user function image at
   > `function create` via the account registry credential, so that credential must point at
   > whichever registry holds the user image (set it to your mirror if the image is mirrored
   > there).

### 3. Set the route domain

`global.domain` sets the hostname suffix for routes such as `api.<domain>`. It does not configure the Gateway or its load balancer. Query the exact existing Gateway:

```bash
GATEWAY_ADDRESS=$(kubectl get gateway <gateway-name> -n <gateway-namespace> -o jsonpath='{.status.addresses[0].value}')
test -n "$GATEWAY_ADDRESS"
```

The address may be a hostname or IP. Cloud provider and Gateway configuration determine the load balancer type and address format. The self-managed stack does not select either.

For temporary testing without DNS, use a stable reserved suffix such as `global.domain: "nvcf.test"`, connect to `GATEWAY_ADDRESS`, and send `Host: api.nvcf.test`. For production, use your base DNS domain and point its route records to `GATEWAY_ADDRESS`. If the endpoint later changes, keep `global.domain` stable and update only DNS or the client destination. See Example 4 in [examples.md](examples.md).

### 4. Preview and deploy

```bash
HELMFILE_ENV=<env-name> helmfile template   # Preview rendered manifests
HELMFILE_ENV=<env-name> helmfile sync       # Deploy
```

> Set BOTH Cassandra passwords in the secrets file. The secrets file documents
> `DEFAULT_CASSANDRA_PASSWORD`, but the cassandra chart has a second credential,
> `cassandra.serviceRolePassword`, that defaults to `ch@ng3m3`. If you set only
> `DEFAULT_CASSANDRA_PASSWORD`, the service role keeps that default and every DB-backed
> service crash-loops with `Bad credentials` once it tries to authenticate. Set
> `cassandra.serviceRolePassword` to the same value as `DEFAULT_CASSANDRA_PASSWORD`.

### 5. Deployment phases

Helmfile deploys in order with dependencies:

| Phase | Selector | Services | Wait time |
|-------|----------|----------|-----------|
| 1 | `release-group=dependencies` | NATS, OpenBao, Cassandra | 5-10 min |
| 2 | `release-group=services` | API, SIS, ESS, invocation, grpc-proxy, notary, api-keys, optional LLM gateway/router, optional Vanity Gateway when the stack package includes the addon | 5-10 min |
| 3 | `release-group=ingress` | Gateway routes | 1-2 min |
| 4 | `release-group=observability` | Observability stack (if enabled) | 1-2 min |

Monitor in a separate terminal:

```bash
kubectl get pods -A -w
```

### 6. Enable Vanity Gateway (optional)

Vanity Gateway is disabled by default and is only needed for customer-facing
hostnames or path mappings in front of the standard NVCF API and invocation
routes. It is available only in stack packages that include the Vanity Gateway
addon. If the extracted stack package does not contain a `vanity-gateway`
release and `vanityGateway` route values, skip these commands and use a newer
stack package that includes them.

If the stack package includes the addon, enable it in
`environments/<env-name>.yaml`:

```yaml
addons:
  vanityGateway:
    enabled: true
    mappingConfig: {}
```

The default route host is `vanity.<domain>` and the backend is
`vanity-gateway.nvcf:8080`. Put deployment-specific host and path mappings under
`addons.vanityGateway.mappingConfig`. If you need custom vanity hosts instead of
`vanity.<domain>`, use the route hostname overrides supported by the stack
package and create matching DNS records.

After confirming the package includes the `vanity-gateway` release, preview and
apply the service and route:

```bash
HELMFILE_ENV=<env-name> helmfile --selector name=vanity-gateway template
HELMFILE_ENV=<env-name> helmfile --selector name=vanity-gateway sync
HELMFILE_ENV=<env-name> helmfile --selector release-group=ingress sync
```

Verify the route only when the addon is present and enabled:

```bash
kubectl get deploy,svc -n nvcf -l app.kubernetes.io/name=vanity-gateway
kubectl get httproute -A | grep -i vanity
curl -H "Host: vanity.<domain>" "http://<gateway-address>/health"
```

Do not enable Vanity Gateway for standard API, API Keys, invocation, LLM
invocation, or gRPC traffic. Those routes are provided by the base gateway
routes.

### 7. Install the separate compute-plane stack

The control-plane stack does not install compute-plane components. After it is healthy, choose exactly one compute-plane handoff:

- Helmfile values flow: author separate environment files, then register and
  install with the compute-plane Makefile.
- CLI profile flow: use the profile generated by the CLI control-plane install,
  then run the complete CLI register and install commands.

Do not mix these handoffs. Follow [Split Compute-Plane Installation](references/compute-plane-installation.md)
for commands, EKS routing, pull secrets, verification, and teardown order.

## Clean Teardown

Destroy every compute plane before the control plane, using the teardown for
the same handoff; see [Split Compute-Plane Installation](references/compute-plane-installation.md#teardown).

Scope: Only destroy releases managed by this control-plane helmfile stack. The NVCF releases are: `nats`, `openbao-server`, `cassandra`, `api-keys`, `sis`, `api`, `invocation-service`, `grpc-proxy`, `ess-api`, `notary-service`, optional `vanity-gateway`, `admin-issuer-proxy`, and `ingress`; `cert-manager` is included only when `certManager.enabled: true`. The control-plane namespace inventory also includes `cert-manager`, but default namespace cleanup preserves it. Namespace preservation does not preserve the Helm release. External cert-manager must use `certManager.enabled: false`; otherwise Helmfile treats the release as stack-managed. Do not delete other helm releases or namespaces on the cluster.

Before control-plane destroy, identify whether cert-manager is external. For an external installation, require effective `certManager.enabled: false` and abort if it is true. For an intended stack-managed release with effective `certManager.enabled: true`, verify matching stack Helm release metadata and abort on missing or mismatched evidence. Preserve the namespace by default; only plan its removal after verifying namespace provenance and getting explicit confirmation.

### Standard teardown

Run from inside the `nvcf-self-managed-stack/` directory:

```bash
HELMFILE_ENV=<env-name> helmfile destroy
```

### Delete namespaces

```bash
for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system; do
  kubectl delete namespace "$ns" --ignore-not-found
done
```

### Verify clean

```bash
kubectl get ns | grep -E '(cassandra|nats|vault|nvcf|api-keys|ess|sis)'
# Should return empty
```

## Overriding Helm Chart Values

### Environment file (limited)

Works only for keys that `global.yaml.gotmpl` propagates (e.g., `cassandra.replicaCount`, `global.storageClass`, `global.image`).

### Release values block (any chart value)

Edit the release in `helmfile.d/*.yaml.gotmpl` and add a `values:` block. In the examples below, `<private-values>` refers to the `secrets/` directory at the helmfile stack root.

```yaml
- name: cassandra
  version: 0.8.0
  condition: cassandra.enabled
  namespace: cassandra-system
  <<: *dependency
  values:
    - ../global.yaml.gotmpl
    - ../<private-values>/{{ requiredEnv "HELMFILE_ENV" }}-secrets.yaml
    - cassandra:
        resources:
          limits:
            cpu: "8"
            memory: 8192Mi
          requests:
            cpu: "2"
            memory: 4096Mi
```

YAML merge gotcha: When a release uses `<<: *dependency` or `inherit`, specifying `values:` replaces the template's values list. You must re-include `global.yaml.gotmpl` and the secrets file.

### Preview and apply a single release

```bash
HELMFILE_ENV=<env> helmfile --selector name=cassandra template  # Preview
HELMFILE_ENV=<env> helmfile --selector name=cassandra sync      # Apply
```

## Image Pull Secrets

There are three distinct credential types. Do not conflate them, and mind the
format rule below (two of them fail even with the correct key if the format is wrong):

| | Control Plane Pull Secrets | API Bootstrap Registry Creds | Sidecar Image-Pull Secret |
|---|---|---|---|
| Purpose | K8s pulls NVCF service images | nvcf-api validates the user function image at `function create` | nvcf-api pulls the platform sidecars injected into every worker pod |
| Where | K8s `docker-registry` Secrets + Kyverno ClusterPolicy | `<private-values>/<env>-secrets.yaml` (account-bootstrap) | `NVCF_API_SIDECARS_IMAGE_PULL_SECRET` in `<env>-secrets.yaml` -> OpenBao `services/nvcf-api/kv/sidecars/image-pull-secret` (field `secret`) |
| Format | standard `.dockerconfigjson` (kubectl builds it for you) | `base64("$oauthtoken:<key>")` | `base64("$oauthtoken:<key>")` |

> Note: the account-bootstrap cred and the sidecar secret are NOT a
> dockerconfigjson. The secrets template ships the placeholder
> `REPLACE_WITH_BASE64_DOCKER_CREDENTIAL`, which reads as "base64 of a dockerconfigjson."
> It isn't. Both values are consumed as a raw docker `auth` string: nvcf-api takes the
> value verbatim and drops it into the `auth` field of the dockerconfig it builds. So the
> value must be `base64("$oauthtoken:<key>")`, e.g. `printf '$oauthtoken:%s' "$KEY" | openssl base64 -A`,
> not `base64(<dockerconfigjson>)`.
>  - Wrong account-bootstrap value: `function create` returns
>    `400 "must be base64 encoded username:password format"`.
>  - Wrong sidecar value: every worker's sidecar pull gets a malformed credential and
>    `403`s, regardless of which key is inside. The symptom looks like a bad or wrong-org
>    key, but the key is fine and the encoding is the bug. To repair an already-installed
>    stack without a full re-sync:
>    ```bash
>    AUTH=$(printf '$oauthtoken:%s' "$KEY" | openssl base64 -A)
>    bao kv put services/nvcf-api/kv/sidecars/image-pull-secret secret="$AUTH"
>    # if NVIDIA Cloud Tasks is enabled, repeat for services/nvct-api/kv/sidecars/image-pull-secret
>    kubectl rollout restart -n nvcf deploy/nvcf-api   # picks up the new sidecar cred
>    ```

> Note (multi-org): when pulling from NGC (not a mirror), the user function image and
> the platform sidecars often live in different nvcr.io orgs, each needing a different
> key, so the account-bootstrap cred (function image) and the sidecar secret (sidecars)
> are frequently two different keys. Mirroring everything into a single registry the
> cluster authenticates to automatically collapses this to one credential path.

### Configuring with Kyverno (recommended)

Use a Kyverno mutating admission policy to automatically inject `imagePullSecrets` into all pods in NVCF namespaces. This works uniformly for all charts -- no per-chart configuration or helmfile modifications needed.

```bash
# 1. Install Kyverno
helm repo add kyverno https://kyverno.github.io/kyverno/
helm repo update
helm install kyverno kyverno/kyverno -n kyverno --create-namespace

# 2. Create pull secret in each namespace
export NGC_API_KEY="<your-key>"
for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system cert-manager; do
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done

# 3. Apply the Kyverno ClusterPolicy
kubectl apply -f kyverno-imagepullsecret-policy.yaml
```

The policy mutates every pod at admission time, adding `imagePullSecrets: [{name: nvcr-pull-secret}]`. Verify with:

```bash
kubectl get pod -n <namespace> <pod-name> -o jsonpath='{.spec.imagePullSecrets}'
# Should show: [{"name":"nvcr-pull-secret"}]
```

Not needed if using a CSP built-in credential helper (e.g., ECR with IAM node roles).

For the Kyverno policy YAML and pull secret creation script, see [references/pull-secrets.md](references/pull-secrets.md).

## Debugging

### Quick status check

```bash
kubectl get pods -A -o wide                        # All pods
helm list -A                                        # All helm releases
kubectl get events -n <ns> --sort-by='.lastTimestamp'  # Recent events
```

### Common failure patterns

| Symptom | Cause | Fix |
|---------|-------|-----|
| `ImagePullBackOff` + `401 Unauthorized` | Missing or wrong pull secret | Check secret exists, check SA has imagePullSecrets |
| `Init:0/1` stuck on service pods | Vault-agent waiting for OpenBao | Check OpenBao pods + migration job status |
| `Init:0/1` + `vault-agent-init` shows `auth/jwt/login` -> `400 ... no known key successfully validated the token signature` | OpenBao JWT auth configured with a static pubkey instead of the cluster's live JWKS -- the base default `openbao.migrations.issuerDiscovery.enabled: false`. Common on AKS / any OIDC-issuer cluster | Set `openbao.migrations.issuerDiscovery.enabled: true`, delete job `openbao-server-migrations`, re-sync `openbao-server`. See [references/debugging.md](references/debugging.md) "Init:0/1 Stuck" fix #4. |
| `OOMKilled` on Cassandra | Default resources too small | Override `cassandra.resources` via values block |
| DB-backed services crash-loop `Bad credentials` connecting to Cassandra | Secrets file set only `DEFAULT_CASSANDRA_PASSWORD`; `cassandra.serviceRolePassword` still defaults to `ch@ng3m3` | Set `cassandra.serviceRolePassword` in the secrets file equal to `DEFAULT_CASSANDRA_PASSWORD`, then re-sync cassandra + the DB-backed services |
| `Pending` pods | Node selector mismatch or no storage class | `kubectl describe pod`, check labels and storage |
| Helm release in `failed` state | First install failed partway | `helmfile destroy` the release, then `sync` again |
| Account bootstrap timeout | Wrong base64 credentials in secrets file | Check `kubectl logs job/nvcf-api-account-bootstrap -n nvcf` |
| function / account calls return `404 Unknown client_id` | The account-bootstrap post-install hook never ran: the `api` release install failed, or was repaired with live patches instead of a clean re-sync, so the hook (and the account) was skipped | Verify and re-run the hook; see [Verify the account bootstrap ran](#verify-the-account-bootstrap-ran) |
| Services fail to read vault secrets; `secrets.json` not found | Vault path hardcoded to `/home/app/vault/` in `_helpers.tpl`; the runtime resolves the mounted path relative to the working directory and drops the leading `/` | Override `podAnnotations` and set `JAVA_TOOL_OPTIONS: "-Duser.dir=/"` in release values |
| NATS connection fails at startup; placement tag mismatch | NATS server tags hardcoded to `dc:ncp`; app derives tag from `AWS_REGION` (e.g., `us-gov-west-1`) | Set `AWS_REGION=ncp` and `NVCF_AWS_REGION=ncp` in env config |

### Verify the account bootstrap ran

The default account is created by a **`post-install` hook on the `api` release**
(`job/nvcf-api-account-bootstrap` in `nvcf`). Because it is a post-install hook, it only
fires on a successful `api` install. If that install failed and you repaired it with live
patches (rather than a clean re-sync), the hook never runs, no account is created, and
every subsequent function/account call returns a cryptic `404 Unknown client_id` with
nothing obviously wrong in the running pods.

Verify it ran after the first sync:

```bash
kubectl get job -n nvcf nvcf-api-account-bootstrap \
  -o jsonpath='{.status.succeeded}'   # expect 1
```

If the job is absent or `0`, re-run it by re-syncing the `api` release so the hook fires
again (delete the prior job first if it lingers in a completed/failed state and blocks the
hook):

```bash
kubectl delete job -n nvcf nvcf-api-account-bootstrap --ignore-not-found
HELMFILE_ENV=<env> helmfile --selector name=api sync
kubectl logs -n nvcf job/nvcf-api-account-bootstrap   # confirm the account was created
```

## Additional Resources

- Installation: [examples.md](examples.md), the [CSP End-to-End Example](https://docs.nvidia.com/nvcf/v0.6.0-rc/csp-end-to-end-example), and [split compute-plane workflows](references/compute-plane-installation.md)
- Operations: [Helmfile structure](references/helmfile-structure.md), [pull secrets](references/pull-secrets.md), and [debugging](references/debugging.md)

After deployment, use the `nvcf-self-managed-cli` skill to create functions, create tasks, manage API keys, and invoke endpoints.
