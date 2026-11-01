# Examples

Worked examples based on real deployment scenarios.

## Example 1: Override Cassandra Resources

The default Cassandra resource limits cause OOM on large instance types (e.g., `p5.48xlarge`).

Problem: Cassandra pods restart with `OOMKilled`.

Solution: Override resources via helmfile release values block.

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

Note: `resourcePreset` (a Bitnami feature) is not available in the NVCF cassandra wrapper chart. Use explicit `resources` instead.

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
      repository: nvidia/nvcf
  image:
    registry: nvcr.io
    repository: nvidia/nvcf
```

### Step 2: Authenticate locally (for helmfile chart pulls)

```bash
docker login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"
helm registry login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"
```

### Step 3: Create namespaces and pull secrets

```bash
export NGC_API_KEY="<your-ngc-api-key>"

for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system cert-manager; do
  kubectl --context <control-plane-context> create namespace "$ns" \
    --dry-run=client -o yaml | kubectl --context <control-plane-context> apply -f -
done

for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system cert-manager; do
  kubectl --context <control-plane-context> create secret docker-registry nvcr-creds \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl --context <control-plane-context> apply -f -
done
```

### Step 4: Add imagePullSecrets to helmfile releases

Add the chart-specific `imagePullSecrets` key to each release. Example for cassandra in `helmfile.d/01-dependencies.yaml.gotmpl`:

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
        global:
          imagePullSecrets:
            - nvcr-creds
```

See [references/pull-secrets.md](references/pull-secrets.md) for the key for every chart.

These are control-plane namespaces. For a separate compute cluster, create the
secret only in `nvca-operator`; the chart and operator mirror it to
`nvca-system`. See [Split Compute-Plane Installation](references/compute-plane-installation.md#pull-secrets).

### Step 5: Deploy

```bash
HELMFILE_ENV=<env> helmfile --kube-context <control-plane-context> sync
```

### Step 6: Patch ServiceAccounts for charts without native support

After Phase 1 (dependencies) completes, OpenBao pods will be in `ImagePullBackOff`. Patch and restart:

```bash
for ns in vault-system nvcf ess; do
  for sa in $(kubectl --context <control-plane-context> get sa -n "$ns" \
    --no-headers -o custom-columns=":metadata.name"); do
    kubectl --context <control-plane-context> patch serviceaccount -n "$ns" "$sa" \
      -p '{"imagePullSecrets": [{"name": "nvcr-creds"}]}'
  done
done
kubectl --context <control-plane-context> delete pods -n vault-system --all
```

Verify:

```bash
kubectl --context <control-plane-context> get events -n vault-system \
  --sort-by='.lastTimestamp' | grep -i pull
# Should show "Successfully pulled image" from nvcr.io
```

---

## Example 3: Full Teardown and Reinstall

When a deployment is broken and you need a clean slate.

### Step 1: Destroy the compute-plane stack

```bash
make -C deploy/stacks/nvcf-compute-plane destroy \
  CLUSTER_NAME=<cluster-name> \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=<compute-plane-kubeconfig>
```

If compute cleanup is stuck on finalizers, use the compute-context procedure in
[debugging.md](references/debugging.md#failure-nvca-cleanup-stuck-finalizers).

### Step 2: Deregister the compute cluster

Read `clusterID` and `ncaID` from
`deploy/stacks/nvcf-compute-plane/registration/<cluster-name>-register-values.yaml`.
Show the ID and get confirmation before deleting the ICMS row:

```bash
<nvcf-cli> --config <config> cluster delete \
  --cluster-id <cluster-id-from-registration-values> \
  --nca-id <nca-id> \
  --ignore-missing
```

Repeat steps 1 and 2 for every compute cluster.

### Step 3: Destroy the control-plane stack

Preserve the `cert-manager` namespace by default. Namespace preservation does
not preserve the Helm release. Before running destroy, identify whether
cert-manager is external. For an external installation, require effective
`certManager.enabled: false` and abort if it is true. For an intended
stack-managed release with effective `certManager.enabled: true`, verify
matching stack Helm release metadata and abort on missing or mismatched
evidence. Only plan namespace removal after verifying namespace provenance and
getting explicit confirmation; otherwise do not delete it.

```bash
KUBECONFIG=<control-plane-kubeconfig> \
make -C deploy/stacks/self-managed destroy \
  HELMFILE_ENV=<env> \
  KUBECONFIG_FILE=<control-plane-kubeconfig>
```

Pass both kubeconfig forms because control-plane namespace cleanup uses
ambient `kubectl`.

### Step 4: Verify clean

```bash
kubectl --context <compute-plane-context> get ns | \
  grep -E '(nvca|nvcf-backend|grove|dynamo)'
kubectl --context <control-plane-context> get ns | \
  grep -E '(cassandra|nats|vault|nvcf|api-keys|ess|sis)'
# Both commands should return empty.
```

### Step 5: Reinstall in split order

Follow [Clean Installation](SKILL.md#clean-installation), then return to the
same handoff in [Split Compute-Plane Installation](references/compute-plane-installation.md).

---

## Example 4: Recover from a Gateway Endpoint Change

The separately provisioned Gateway or its load balancer was recreated and got a new endpoint.

### Step 1: Get the new endpoint

```bash
GATEWAY_ADDR=$(kubectl --context <control-plane-context> get gateway \
  nvcf-gateway -n envoy-gateway \
  -o jsonpath='{.status.addresses[0].value}')
test -n "$GATEWAY_ADDR"
```

### Step 2: Update DNS and direct address fields

Keep `global.domain`, route hostnames, and CLI Host fields unchanged. Point the
DNS records for rendered route hostnames, such as `api.<domain>` and
`*.invocation.<domain>`, to `GATEWAY_ADDR`. For a temporary environment without
DNS, keep its reserved route suffix and connect to `GATEWAY_ADDR` with the
matching `Host` header.

For a split deployment configured with direct Gateway addresses, replace only
the address-bearing values: control-plane `nvcfNatsServiceURL`, compute-plane
ICMS, ReVal, and NATS URLs, and CLI URL fields. Keep their domain and Host
overrides unchanged. Refresh registration and installation through the same
handoff used originally. See
[Split Compute-Plane Installation](references/compute-plane-installation.md)
for the fields and complete commands.

### Step 3: Verify both contexts

```bash
dig +short "api.<domain>"
curl -H "Host: api.<domain>" "http://$GATEWAY_ADDR/health"
kubectl --context <control-plane-context> get gateway nvcf-gateway \
  -n envoy-gateway -o wide
kubectl --context <control-plane-context> get httproutes -A
kubectl --context <control-plane-context> get tcproutes -A
kubectl --context <compute-plane-context> rollout status \
  deployment/nvca-operator -n nvca-operator --timeout=10m
kubectl --context <compute-plane-context> wait \
  nvcfbackend/<compute-plane-cluster-name> -n nvca-operator \
  --for=jsonpath='{.status.agentStatus}'=healthy --timeout=10m
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
| `release-group=services` | api-keys, sis, api, invocation-service, grpc-proxy, ess-api, notary-service, reval, optional llm-request-router and llm-api-gateway when `llm.enabled=true`, optional vanity-gateway only in stack packages that include the addon and have `addons.vanityGateway.enabled=true`, optional nvcf-ui only in stack packages that include the addon and have `addons.nvcfUi.enabled=true` |
| `name=llm-request-router` | Deploy just the LLM request router when `llm.enabled=true` |
| `name=llm-api-gateway` | Deploy just the LLM API gateway when `llm.enabled=true` |
| `name=vanity-gateway` | Deploy just Vanity Gateway only when the stack package includes that release and `addons.vanityGateway.enabled=true` |
| `name=nvcf-ui` | Deploy NVCF UI only when the stack package includes that release and `addons.nvcfUi.enabled=true` |
| `release-group=ingress` | ingress (gateway routes) |
| `name=<release>` | Any individual release by name |

Note: `admin-issuer-proxy` does not have a release-group label. Target it with `--selector name=admin-issuer-proxy`.
