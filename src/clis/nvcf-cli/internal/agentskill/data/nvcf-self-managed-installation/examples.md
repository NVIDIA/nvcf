# Examples

Worked examples based on real deployment scenarios.

## Example 1: Override Cassandra Resources

The default Cassandra resource limits cause OOM on large instance types (e.g., `p5.48xlarge`).

**Problem**: Cassandra pods restart with `OOMKilled`.

**Solution**: Override resources via helmfile release values block.

### Step 1: Capture baseline

```bash
HELMFILE_ENV=<env> helmfile --selector name=cassandra template > /tmp/cass-before.yaml
grep -A8 'resources:' /tmp/cass-before.yaml | head -10
```

### Step 2: Add override to helmfile.d/01-dependencies.yaml.gotmpl

In the examples below, `<private-values>` refers to the `secrets/` directory at the helmfile stack root.

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

### Step 3: Verify and apply

```bash
HELMFILE_ENV=<env> helmfile --selector name=cassandra template > /tmp/cass-after.yaml
diff /tmp/cass-before.yaml /tmp/cass-after.yaml  # Confirm resources changed
HELMFILE_ENV=<env> helmfile --selector name=cassandra sync
```

**Note**: `resourcePreset` (a Bitnami feature) is not available in the NVCF cassandra wrapper chart. Use explicit `resources` instead.

---

## Example 2: Deploy from NGC Private Registry with Pull Secrets

Deploy the NVCF stack pulling images directly from NGC (`nvcr.io`) instead of a mirrored registry, using image pull secrets.

### Step 1: Configure environment for NGC

```yaml
# environments/<env>.yaml
global:
  helm:
    sources:
      registry: nvcr.io
      repository: 0833294136851237/nvcf-ncp-staging
  image:
    registry: nvcr.io
    repository: 0833294136851237/nvcf-ncp-staging
```

### Step 2: Authenticate locally (for helmfile chart pulls)

```bash
docker login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"
```

### Step 3: Create namespaces and pull secrets

```bash
export NGC_API_KEY="<your-ngc-api-key>"

for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system nvca-operator nvca-system nvcf-backend; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done

for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system nvca-operator nvca-system nvcf-backend; do
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

### Step 4: Add imagePullSecrets to the helmfile environment

Add the global `imagePullSecrets` key to the environment values:

```yaml
global:
  imagePullSecrets:
    - name: nvcr-pull-secret
```

See [references/pull-secrets.md](references/pull-secrets.md) for details.

### Step 5: Deploy

```bash
HELMFILE_ENV=<env> helmfile sync
```

Verify:

```bash
kubectl get events -n vault-system --sort-by='.lastTimestamp' | grep -i pull
# Should show "Successfully pulled image" from nvcr.io
```

---

## Example 3: Full Teardown and Reinstall

When a deployment is broken and you need a clean slate.

### Step 1: Destroy helmfile releases

```bash
HELMFILE_ENV=<env> helmfile destroy
```

### Step 2: Force cleanup stuck NVCA resources

If `helmfile destroy` hangs or `nvca-operator`/`nvcf-backend` namespaces are stuck in `Terminating`:

```bash
# Remove finalizers from NVCFBackend custom resources
kubectl get nvcfbackends -A -o json | \
  jq '.items[] | .metadata.namespace + "/" + .metadata.name' -r | \
  xargs -I{} sh -c 'ns="${1%%/*}"; name="${1##*/}"; kubectl patch nvcfbackend "$name" -n "$ns" --type=merge -p "{\"metadata\":{\"finalizers\":[]}}"' _ {}

# Force-delete stuck namespaces by clearing their finalizers
for ns in nvca-operator nvcf-backend; do
  kubectl get namespace "$ns" -o json 2>/dev/null | \
    jq '.spec.finalizers = []' | \
    kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f - 2>/dev/null
done
```

### Step 3: Delete all namespaces

```bash
for ns in cassandra-system nats-system nvcf api-keys ess sis nvca-operator vault-system; do
  kubectl delete namespace "$ns" --ignore-not-found
done
```

### Step 4: Verify clean

```bash
kubectl get ns | grep -E '(cassandra|nats|vault|nvcf|api-keys|ess|sis|nvca)'
# Should be empty (nvca-modelcache-init is unrelated)
```

### Step 5: Reinstall from Step 1

Follow the [Clean Installation](#) steps in SKILL.md.

---

## Example 4: Recover from Gateway Address Change

The Gateway/LB was recreated (e.g., TCPRoute misconfiguration) and got a new address.

### Step 1: Get new address

```bash
GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway \
  -o jsonpath='{.status.addresses[0].value}')
echo "$GATEWAY_ADDR"
```

### Step 2: Update environment file

Edit `environments/<env>.yaml` and replace the `domain` value:

```yaml
global:
  domain: "NEW_GATEWAY_ADDR"
```

### Step 3: Re-sync affected releases

```bash
HELMFILE_ENV=<env> helmfile --selector release-group=ingress sync
HELMFILE_ENV=<env> helmfile --selector release-group=services sync
HELMFILE_ENV=<env> helmfile --selector name=admin-issuer-proxy sync
```

### Step 4: Verify routes

```bash
kubectl get httproutes -A
kubectl get tcproutes -A
```

---

## Example 5: Deploy Only Dependencies

Useful for testing or when iterating on service configuration.

```bash
# Deploy just NATS, Cassandra, OpenBao
HELMFILE_ENV=<env> helmfile --selector release-group=dependencies sync

# Check status
kubectl get pods -n nats-system
kubectl get pods -n cassandra-system
kubectl get pods -n vault-system

# Deploy just one specific release
HELMFILE_ENV=<env> helmfile --selector name=cassandra sync
```

### Selector reference

| Selector | Releases |
|----------|----------|
| `release-group=dependencies` | nats, cassandra, openbao-server |
| `release-group=services` | api-keys, sis, api, invocation-service, grpc-proxy, ess-api, notary-service, reval, optional llm-request-router and llm-api-gateway when `llm.enabled=true` |
| `name=llm-request-router` | Deploy just the LLM request router when `llm.enabled=true` |
| `name=llm-api-gateway` | Deploy just the LLM API gateway when `llm.enabled=true` |
| `release-group=ingress` | ingress (gateway routes) |
| `release-group=workers` | nvca-operator |
| `name=<release>` | Any individual release by name |

Note: `admin-issuer-proxy` does not have a release-group label. Target it with `--selector name=admin-issuer-proxy`.
