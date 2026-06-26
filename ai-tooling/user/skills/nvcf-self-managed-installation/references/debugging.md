# Debugging Reference

Recipes for diagnosing and fixing common NVCF self-managed stack failures.

## Quick Status Commands

```bash
# All pods across all namespaces
kubectl get pods -A -o wide

# Pods in a specific namespace
kubectl get pods -n <namespace>

# All helm releases
helm list -A

# Recent events (most useful for diagnosing failures)
kubectl get events -n <namespace> --sort-by='.lastTimestamp'

# Describe a specific pod (shows events, conditions, volumes)
kubectl describe pod -n <namespace> <pod-name>
```

## Failure: ImagePullBackOff

### Symptoms

```
NAME           READY   STATUS             RESTARTS   AGE
cassandra-0    0/1     ImagePullBackOff   0          5m
```

Events show:
```
Failed to pull image "nvcr.io/.../image:tag": 401 Unauthorized
```

### Diagnosis

```bash
# Check what image is failing
kubectl describe pod -n <namespace> <pod-name> | grep -A5 "Events:"

# Check if pull secret exists in the namespace
kubectl get secret nvcr-creds -n <namespace>

# Check if the pod spec has imagePullSecrets
kubectl get pod -n <namespace> <pod-name> -o jsonpath='{.spec.imagePullSecrets}'

# Check if the ServiceAccount has imagePullSecrets
kubectl get sa <sa-name> -n <namespace> -o jsonpath='{.imagePullSecrets}'
```

### Fixes

1. **Secret missing in namespace**: Create it
   ```bash
   kubectl create secret docker-registry nvcr-creds \
     --docker-server=nvcr.io \
     --docker-username='$oauthtoken' \
     --docker-password="$NGC_API_KEY" \
     --namespace=<namespace> \
     --dry-run=client -o yaml | kubectl apply -f -
   ```

2. **Secret exists but pod doesn't reference it**: The chart needs `imagePullSecrets` configured via helmfile values. See [pull-secrets.md](pull-secrets.md).

3. **Chart doesn't support imagePullSecrets** (openbao, invocation-service, ess-api, notary-service): Patch ServiceAccounts and restart pods. See [pull-secrets.md](pull-secrets.md).

4. **Wrong credentials in secret**: Delete and recreate
   ```bash
   kubectl delete secret nvcr-creds -n <namespace>
   # Recreate with correct credentials
   ```

## Failure: Init:0/1 Stuck (Vault Agent)

### Symptoms

Service pods stuck in `Init:0/1` for minutes:
```
NAME                        READY   STATUS     RESTARTS   AGE
nvcf-api-7f4c76f788-44vlt  0/2     Init:0/1   0          10m
```

### Diagnosis

The init container is the vault-agent-init injector waiting for OpenBao.

```bash
# Check OpenBao pods
kubectl get pods -n vault-system

# Check if OpenBao migration job ran
kubectl get jobs -n vault-system

# Check OpenBao pod logs
kubectl logs -n vault-system openbao-server-0 -c openbao

# Check init container logs on the stuck pod
kubectl logs -n <namespace> <pod-name> -c vault-agent-init
```

### Fixes

1. **OpenBao pods not running**: Check their events for image pull issues, resource issues, etc.

2. **OpenBao migration job didn't run**: This happens when `helmfile sync` was interrupted. Destroy and re-sync openbao:
   ```bash
   HELMFILE_ENV=<env> helmfile --selector name=openbao-server destroy
   kubectl delete namespace vault-system
   kubectl create namespace vault-system
   # Re-create pull secret if needed
   HELMFILE_ENV=<env> helmfile --selector name=openbao-server sync
   ```

3. **OpenBao pods running but not initialized**: Check unseal status:
   ```bash
   kubectl exec -n vault-system openbao-server-0 -c openbao -- bao status
   ```

4. **`vault-agent-init` logs `auth/jwt/login` -> `400 ... no known key successfully validated the token signature`**
   (common on AKS, and on any cluster whose OIDC issuer publishes a rotating multi-key JWKS):
   the OpenBao `auth/jwt` method was configured with a single static mounted public key
   (`jwt_validation_pubkeys`) instead of the issuer's live JWKS, so it cannot validate the
   cluster-signed ServiceAccount tokens the vault-agents present. Every service's
   `vault-agent-init` then loops on that 400 and the pod never leaves `Init:0/1`. This is the
   behavior of the base default `openbao.migrations.issuerDiscovery.enabled: false`; the
   migration log shows `OIDC issuer discovery is disabled. Using ServiceAccount issuer and
   mounted public key fallback`. Enable issuer discovery so the migration uses the live JWKS,
   then re-run it:
   ```bash
   # In your env values (e.g. environments/<env>.yaml), set:
   #   openbao:
   #     migrations:
   #       issuerDiscovery:
   #         enabled: true
   kubectl delete job -n vault-system openbao-server-migrations
   HELMFILE_ENV=<env> helmfile --selector name=openbao-server sync
   ```
   The migration log should then show `Received discovery document from
   .../.well-known/openid-configuration` and `Final OpenBao JWT JWKS URL set to:
   .../openid/v1/jwks`, and the service pods authenticate and reach Ready within ~1 min.
   AKS clusters with the cluster OIDC issuer enabled (the `aks-cluster` default) always need
   this — the static-pubkey fallback cannot match the issuer's rotating signing keys.
   (Ref: NVBug 6371575.)

## Failure: OOMKilled on Cassandra

### Symptoms

```
NAME          READY   STATUS    RESTARTS   AGE
cassandra-0   0/1     OOMKilled  3         10m
```

### Diagnosis

```bash
kubectl describe pod -n cassandra-system cassandra-0 | grep -A3 "Last State"
```

### Fix

Override Cassandra resources via helmfile values. See Example 1 in [examples.md](/ai-tooling/user/skills/nvcf-self-managed-installation/examples.md).

```yaml
- cassandra:
    resources:
      limits:
        cpu: "8"
        memory: 8192Mi
      requests:
        cpu: "2"
        memory: 4096Mi
```

Note: `resourcePreset` is not available in the NVCF cassandra wrapper chart. Use explicit `resources`.

## Failure: Pods Stuck in Pending

### Symptoms

```
NAME          READY   STATUS    RESTARTS   AGE
cassandra-0   0/1     Pending   0          10m
```

### Diagnosis

```bash
# Check events for scheduling failures
kubectl describe pod -n <namespace> <pod-name>

# Common causes:
# - "0/12 nodes are available: 12 node(s) didn't match Pod's node affinity"
# - "0/12 nodes are available: 12 Insufficient memory"
# - "persistentvolumeclaim not found"
```

### Fixes

1. **Node selector mismatch**: Check `nodeSelectors` in environment file
   ```bash
   kubectl get nodes --show-labels | grep nvcf
   ```

2. **Storage class not found**: Check storage class exists
   ```bash
   kubectl get storageclass
   ```

3. **Insufficient resources**: Check node capacity
   ```bash
   kubectl describe node <node-name> | grep -A5 "Allocated resources"
   ```

## Failure: Helm Release in Failed State

### Symptoms

```bash
helm list -A
# Shows release with STATUS: failed
```

Re-running `helmfile sync` appears to succeed but services don't work (migrations skipped).

### Diagnosis

```bash
helm history <release-name> -n <namespace>
```

### Fix

Must destroy the failed release and re-sync (not just apply):

```bash
HELMFILE_ENV=<env> helmfile --selector name=<release> destroy
# If namespace needs cleanup:
kubectl delete namespace <namespace>
kubectl create namespace <namespace>
# Re-create pull secret if needed
HELMFILE_ENV=<env> helmfile --selector name=<release> sync
```

## Failure: Account Bootstrap Job Failed

### Symptoms

Services deploy but functions can't be created. Events show bootstrap job failure.

### Diagnosis

```bash
# Check bootstrap job status
kubectl get jobs -n nvcf

# Get logs (job auto-deletes after ~5 minutes)
kubectl logs job/nvcf-api-account-bootstrap -n nvcf

# Check API logs
kubectl logs -n nvcf -l app.kubernetes.io/name=nvcf-api --tail=100
```

### Common Causes

1. **Wrong base64 credentials**: Credentials in `secrets/<env>-secrets.yaml` must be `$oauthtoken:API_KEY` base64-encoded, not just the API key
   ```bash
   # Verify your encoded credential
   echo 'YOUR_BASE64_STRING' | base64 -d
   # Should output: $oauthtoken:nvapi-xxxxx
   ```

2. **Registry unreachable**: API can't reach the registry specified in `accountBootstrap.registryCredentials`

### Fix

Fix credentials in secrets file, then recover services without destroying dependencies:

```bash
HELMFILE_ENV=<env> helmfile --selector release-group=services destroy
kubectl delete namespace nvcf api-keys ess sis --ignore-not-found
kubectl create namespace nvcf && kubectl create namespace api-keys && \
  kubectl create namespace ess && kubectl create namespace sis
# Re-create pull secrets if needed
HELMFILE_ENV=<env> helmfile --selector release-group=services sync
```

## Failure: NVCA Cleanup Stuck (Finalizers)

### Symptoms

`compute-plane uninstall` or compute-stack `helmfile destroy` hangs. Namespaces
stuck in `Terminating`:

```bash
kubectl get ns
# nvca-operator    Terminating   10m
# nvcf-backend     Terminating   10m
```

### Fix

Remove finalizers from NVCFBackend custom resources and force-delete stuck namespaces:

```bash
# Remove finalizers from NVCFBackend resources
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

## Failure: NVCA Operator Missing or Not Ready

### Symptoms

After compute-plane install completes, no `nvca-operator` Deployment exists, or
it exists but pods are not ready:

```bash
kubectl get deployment nvca-operator -n nvca-operator
kubectl get pods -n nvca-operator
```

### Diagnosis and Fixes

Use the split compute-plane workflow:

1. Re-run `nvcf-cli self-hosted compute-plane register` and confirm the
   values file under `deploy/stacks/nvcf-compute-plane/out/`.
2. Re-run `nvcf-cli self-hosted compute-plane install --values <file>`.
3. If pods still fail, inspect operator and agent logs:
   ```bash
   kubectl describe deploy nvca-operator -n nvca-operator
   kubectl logs deployment/nvca-operator -n nvca-operator --tail=200
   ```
4. For fake GPU and cluster prerequisite issues, follow the
   `nvcf-self-managed-cli` skill's compute-plane troubleshooting workflow.

## Failure: Gateway Address Changed

### Symptoms

SIS cluster registration fails. API returns connection errors. HTTPRoutes/TCPRoutes reference old address.

### Diagnosis

```bash
# Check current gateway address
kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}'

# Compare with environment file domain
grep domain environments/<env>.yaml
```

### Fix

See Example 4 in [examples.md](/ai-tooling/user/skills/nvcf-self-managed-installation/examples.md): update domain in environment file, re-sync ingress and services.

## Useful Namespace-to-Service Mapping

Quick reference for which services run in which namespace:

| Namespace | Services |
|-----------|----------|
| nats-system | nats |
| cassandra-system | cassandra |
| vault-system | openbao-server |
| nvcf | api, invocation-service, grpc-proxy, notary-service, reval, llm-request-router, llm-api-gateway |
| api-keys | api-keys, admin-issuer-proxy |
| ess | ess-api |
| sis | sis |
| envoy-gateway-system | envoy gateway (ingress controller) |
| envoy-gateway | gateway resource + routes |
