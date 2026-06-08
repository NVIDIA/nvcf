# Troubleshooting / FAQ

This appendix contains common issues, tips, and clarifications learned from deploying self-hosted NVCF.

## Configuration Issues and Best Practices

### Incorrect Base64 Docker Credentials Format

**Symptom:**

- API migration job fails during installation
- Error appears as a "timeout" in the logs
- Re-running `helmfile sync` or `helmfile apply` appears to succeed but the deployment doesn't work properly
- Functions fail to deploy or pull images

**Root Cause:**

The base64-encoded Docker credential in `secrets.yaml` was incorrectly formatted. A common mistake is encoding **only the NGC API key** instead of the full **basic auth credential** in the format `$oauthtoken:API_KEY`.

**Incorrect (will fail):**

```bash
# ❌ WRONG - Only encoding the API key
echo -n 'nvapi-1234567890abcdef' | base64
# Results in: bnZhcGktMTIzNDU2Nzg5MGFiY2RlZg==
```

**Correct:**

```bash
# ✅ CORRECT - Encoding the full credential in basic auth format
echo -n '$oauthtoken:nvapi-1234567890abcdef' | base64
# Results in: JG9hdXRodG9rZW46bnZhcGktMTIzNDU2Nzg5MGFiY2RlZg==
```

**How to Diagnose:**

1. Check the migration job logs specifically:

   ```bash
   kubectl logs -n nvcf job/nvcf-api-migration -c migration
   ```

2. If you don't see detailed errors, add debug output to migration scripts:

   ```bash
   # Add set -x to the migration script for verbose output
   kubectl edit configmap -n nvcf nvcf-api-migration-scripts
   # Add 'set -x' at the top of the script
   ```

3. Fix your secrets.yaml with correct base64 credential, then follow the [clean-install-procedure](./troubleshooting.md).

**How to Prevent:**

1. **Always use the correct format**: Encode `$oauthtoken:YOUR_API_KEY`, not just the API key

2. **Verify before deploying**: Decode your base64 string to verify it's correct:

   ```bash
   # Verify your encoded credential
   echo 'YOUR_BASE64_STRING' | base64 -d
   # Should output: $oauthtoken:nvapi-1234567890abcdef
   ```

3. **Test NGC authentication**: Before deploying, test that your credential works:

   ```bash
   # Test NGC login with your credential
   echo 'YOUR_BASE64_STRING' | base64 -d | IFS=: read username password
   docker login nvcr.io -u "$username" -p "$password"
   ```

## Identifying Deployment Issues

Use these commands to diagnose deployment problems. For phase-by-phase monitoring during installation, see the Deployment Progression section in [helmfile-installation](./helmfile-installation.md).

**Find Stuck Deployments:**

```bash
# Pods stuck in pending state
kubectl get pods -A | grep Pending

# Pods with image pull issues
kubectl get pods -A | grep -E "ImagePullBackOff|ErrImagePull"

# Pods in crash loops
kubectl get pods -A | grep CrashLoopBackOff

# Check recent events for errors
kubectl get events --sort-by='.lastTimestamp' -A | grep -i error | tail -10
```

**Resource Check:**

```bash
# Check node resources
kubectl top nodes

# Check pod resource usage
kubectl top pods -A | grep -E "nvcf|nats|cassandra|openbao"
```

## Installation & Deployment Issues

### Account Bootstrap Job Failures

**Symptom:**

- `helmfile sync` hangs or fails during the services phase
- Events show `BackoffLimitExceeded` for `nvcf-api-account-bootstrap`
- Bootstrap pod shows `CrashLoopBackOff` or `Error` status

**Diagnosis:**

1. Watch events in real-time (run this as soon as helmfile reaches services phase):

   ```bash
   kubectl get events -n nvcf -w
   ```

2. Check the bootstrap job logs:

   ```bash
   kubectl logs job/nvcf-api-account-bootstrap -n nvcf
   ```

3. Check the NVCF API logs for detailed error messages:

   ```bash
   kubectl logs -n nvcf -l app.kubernetes.io/name=nvcf-api --tail=100
   ```

<Note>
The bootstrap job auto-deletes after ~5 minutes (`ttlSecondsAfterFinished: 300`). Monitor events to catch failures in real-time.

</Note>

<Tip>
**Enable debug logging for the bootstrap job.** The account bootstrap script supports
a `DEBUG` environment variable that enables verbose output. To enable it before
redeploying, patch the bootstrap secret:

```bash
kubectl patch secret nvcf-api-account-bootstrap-secret -n nvcf \
  -p '{"stringData":{"DEBUG":"true"}}'
```

Then follow the "Recovering from Services Failures" steps in [helmfile-installation](./helmfile-installation.md)
to redeploy. The next bootstrap job run will include detailed debug logs visible via
`kubectl logs job/nvcf-api-account-bootstrap -n nvcf`.

To disable debug logging afterward:

```bash
kubectl patch secret nvcf-api-account-bootstrap-secret -n nvcf \
  -p '{"stringData":{"DEBUG":"false"}}'
```

</Tip>

**Common Causes:**

- **Invalid registry credentials format** - See [Incorrect Base64 Docker Credentials Format]
- **Wrong registry hostname** - Hostname in secrets doesn't match actual registry (e.g., using `nvcr.io` but credentials are for ECR)
- **Missing \`\`\$oauthtoken\`\` prefix** - NGC credentials must be in format `$oauthtoken:API_KEY`

**Solution:**

Fix your `secrets/<environment-name>-secrets.yaml` file, then follow the "Recovering from Services Failures" steps in [helmfile-installation](./helmfile-installation.md) to preserve your dependencies.

### Pods Stuck in ImagePullBackOff

**Symptoms:** Pods cannot pull container images

**Solutions:**

1. Verify registry credentials:

   ```bash
   # Check secret exists
   kubectl get secret -n nvcf nvcf-image-pull-secret

   # Verify credential is valid
   kubectl get secret -n nvcf nvcf-image-pull-secret -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d
   ```

2. Verify images exist in your registry:

   ```bash
   # For ECR (replace with your repository name)
   aws ecr describe-images --repository-name <your-ecr-repository-name> --region <your-region>

   # For NGC (if using)
   ngc registry image list 0833294136851237/nvcf-ncp-staging/*
   ```

3. Check network connectivity from cluster to registry

### Pods Stuck in Pending

**Symptoms:** Pods remain in Pending state

**Solutions:**

1. Check cluster resources:

   ```bash
   kubectl describe node <node-name>
   ```

2. Verify storage class exists:

   ```bash
   kubectl get storageclass
   ```

3. Check node selectors:

   ```bash
   # View pod events
   kubectl describe pod -n <namespace> <pod-name>

   # Check node labels
   kubectl get nodes --show-labels
   ```

### Helm Release in Failed State After First Install

**Symptom:**

- First `helmfile sync` fails partway through
- Re-running `helmfile sync` or `helmfile apply` appears to succeed but things don't work
- Migrations or initialization jobs weren't executed

**Root Cause:**

When a Helm installation fails, the release remains in a failed state. Subsequent commands run `helm upgrade` instead of `helm install`, which **skips initialization hooks** (migrations, account bootstrap, etc.).

**Solution:**

Fix the underlying issue (credentials, config, etc.), then follow the appropriate recovery procedure in [helmfile-installation](./helmfile-installation.md):

- **If only services failed** (dependencies are healthy): Use the "Recovering from Services Failures" steps to preserve your dependencies
- **If dependencies are also broken**: Follow the "Uninstalling" section in [helmfile-installation](./helmfile-installation.md)

### NVCA Operator Fails: nvcfbackends CRD Not Found

**Symptom:**

NVCA Operator installation fails with CRD not found error:

```text
Error: customresourcedefinitions.apiextensions.k8s.io "nvcfbackends.nvcf.nvidia.io" not found
```

**Root Cause:**

A race condition occurs where Helm validates CRD references before the CRD is created by the operator's installation hooks. This can happen during first install or when reinstalling after the CRD was deleted.

**Solution:**

Two changes are required in `helmfile.d/03-worker.yaml.gotmpl`:

1. **Add \`\`disableValidation: true\`\`** to the nvca-operator release to disable OpenAPI validation:

```yaml
wait: true
waitForJobs: true
disableValidation: true  # Add this line
labels:
  release-group: workers
```

2. **Remove \`\`--dry-run=server\`\`** from the `helmDefaults.diffArgs` section. This prevents server-side validation during the diff phase, which fails when the CRD doesn't exist:

```yaml
helmDefaults:
  createNamespace: true
  devel: true
  timeout: 900
  wait: true
  waitForJobs: true
  # Note: --dry-run=server removed for worker releases to avoid CRD validation failures
  # when reinstalling nvca-operator after CRD deletion
```

Then run `./force-cleanup-nvcf.sh` followed by `HELMFILE_ENV=<environment> helmfile sync`.

## Accessing OpenBao Secrets (CLI)

NVCF stores most service credentials, signing keys, and internal passwords in
[OpenBao](https://openbao.org/) (a Vault-compatible secrets manager) running in the
`vault-system` namespace. Use the `bao` CLI inside the OpenBao pod to inspect or
manage these secrets.

<Note>
For the full `bao` CLI reference, see the
[OpenBao CLI documentation](https://openbao.org/docs/commands/).
The KV secrets engine commands are documented at
[OpenBao KV commands](https://openbao.org/docs/commands/kv/).

</Note>

### Retrieve the Root Token

The OpenBao root token is stored in a Kubernetes secret created during initialization:

```bash
# Retrieve the root token
export BAO_ROOT_TOKEN=$(kubectl get secret openbao-server-root-token \
  -n vault-system -o jsonpath='{.data.root_token}' | base64 -d)
```

<Warning>
The root token grants unrestricted access to all secrets in OpenBao. Treat it as a
highly sensitive credential and avoid storing it in shell history or logs.

</Warning>

### List Secrets Engines

To see all mounted secrets engines (each NVCF service has its own path):

```bash
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao secrets list
```

Example output (abbreviated):

```text
Path                             Type                        Description
----                             ----                        -----------
services/all/kv/                 kv                          n/a
services/api-keys-api/jwt/       vault-plugin-secrets-jwt    n/a
services/api-keys-api/kv/        kv                          n/a
services/ess-api/jwt/            vault-plugin-secrets-jwt    n/a
services/ess-api/kv/             kv                          n/a
services/invocation-api/jwt/     vault-plugin-secrets-jwt    n/a
services/invocation-api/kv/      kv                          n/a
services/nvcf-api/jwt/           vault-plugin-secrets-jwt    n/a
services/nvcf-api/kv/            kv                          n/a
services/nvcf-notary/kv/         kv                          n/a
services/sis-api/jwt/            vault-plugin-secrets-jwt    n/a
services/sis-api/kv/             kv                          n/a
...
```

### List and Read Secrets

Browse secrets under a specific engine path:

```bash
# List top-level keys under a secrets engine
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao kv list services/nvcf-api/kv

# List keys in a subdirectory (paths ending in / are directories)
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao kv list services/nvcf-api/kv/cassandra

# Read a specific secret
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao kv get services/nvcf-api/kv/cassandra/creds
```

<Tip>
Use `bao kv get -format=json <path>` for machine-readable output, or
`bao kv get -field=<key> <path>` to extract a single field.

</Tip>

### Run Arbitrary `bao` Commands

You can run any `bao` subcommand by exec-ing into the pod with the root token:

```bash
# General pattern
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao <command> [args]

# Examples:
# Check server status
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao status

# List auth methods
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao auth list
```

## Debugging Techniques

### Enabling Verbose Logging

To get more detailed logs from specific components:

**For Migration Jobs:**

```bash
# Edit the migration script configmap
kubectl edit configmap -n nvcf nvcf-api-migration-scripts

# Add to the top of the script:
set -x  # Enable command tracing
set -e  # Exit on error
```

**Example For API Service:**

```bash
# Set log level via environment variable
kubectl set env -n nvcf deployment/nvcf-api LOG_LEVEL=debug
```

### Inspecting Failed Pods

```bash
# Get pod status with more details
kubectl get pods -n nvcf -o wide

# Describe a problematic pod
kubectl describe pod -n nvcf <pod-name>

# View logs (current)
kubectl logs -n nvcf <pod-name>

# View logs (previous if pod restarted)
kubectl logs -n nvcf <pod-name> --previous

# Follow logs in real-time
kubectl logs -n nvcf <pod-name> -f

# Logs for all containers in a pod
kubectl logs -n nvcf <pod-name> --all-containers
```

### Checking Events

Kubernetes events often contain valuable debugging information:

```bash
# Get recent events for a namespace
kubectl get events -n nvcf --sort-by='.lastTimestamp'

# Get events for a specific pod
kubectl get events -n nvcf --field-selector involvedObject.name=<pod-name>

# Watch events in real-time
kubectl get events -n nvcf --watch
```

## Recovery Procedures

For detailed recovery steps, see the **Recovering from Partial Deployments** section in [helmfile-installation](./helmfile-installation.md). This section provides quick reference for common scenarios.

### Choosing a Recovery Strategy

| Failure Scenario | Recovery Strategy | Reference |
| --- | --- | --- |
| **Dependencies failed** (Cassandra, NATS, OpenBao) | Redeploy individual dependency | See `Redeploy Stuck Dependencies`_ below |
| **Services failed** (API, api-keys, etc.) but dependencies OK | Partial recovery (preserve dependencies) | See "Recovering from Services Failures" in [helmfile-installation](./helmfile-installation.md) |
| **Everything broken** or uncertain state | Full uninstall and reinstall | See "Uninstalling" in [helmfile-installation](./helmfile-installation.md) |

<Warning>
**Do not attempt to fix failed services by re-running** `helmfile sync` **or** `helmfile apply`. Helm will skip initialization hooks (migrations, account bootstrap) on upgrade, resulting in a deployment that appears successful but doesn't function correctly.

</Warning>

### Redeploy Stuck Dependencies

Dependency services (Cassandra, NATS, OpenBao) can be safely redeployed without affecting other components:

```bash
# Redeploy only Cassandra
HELMFILE_ENV=<environment-name> helmfile --selector name=cassandra apply

# Redeploy all dependencies
HELMFILE_ENV=<environment-name> helmfile --selector release-group=dependencies apply
```

### Reinstalling NVCA Operator Only

If only NVCA needs reinstalling (and NVCF services are working):

```bash
./force-cleanup-nvcf.sh
HELMFILE_ENV=<environment-name> helmfile --selector release-group=workers sync
```

If NVCF services are also broken, follow the "Recovering from Services Failures" steps in [helmfile-installation](./helmfile-installation.md).

### NVCA Force Cleanup Script

If `helmfile destroy` hangs on NVCA cleanup (typically when functions are still deployed in `nvcf-backend`), use the force cleanup script in a new terminal. See [force-cleanup-script](./troubleshooting.md) for the full script and usage instructions.

```bash
./force-cleanup-nvcf.sh --dry-run  # Preview
./force-cleanup-nvcf.sh            # Execute
```

## Database Issues

### Cassandra Migration Stuck Due to Missing ConfigMap

**Symptoms:**

Cassandra pods are running but migration job is stuck.

```bash
kubectl get pods -n cassandra-system

...
pod/cassandra-initialize-cluster-qp4ft   0/1     ContainerCreating # Stuck in ContainerCreating state perpetually
```

**Diagnosis:**

Check all Cassandra resources including ConfigMaps:

```bash
kubectl -n cassandra-system get all,secrets,sa,cm
```

Expected output should show **3 ConfigMaps**:

```text
NAME                              DATA   AGE
configmap/cassandra-init-cql      1      5d19h # This one may be missing
configmap/cassandra-init-script   1      100s  # This one may be missing
configmap/kube-root-ca.crt        1      8d
```

If you only see 2 ConfigMaps (missing `cassandra-migrations`), this is a race condition during deployment.

**Root Cause:**

A race condition can occur where the Cassandra migration job starts before all ConfigMaps are created, causing the deployment to hang.

**Solution:**

Force a sync to recreate missing resources:

```bash
# Use helmfile sync instead of apply to force resource recreation
HELMFILE_ENV="<environment>" \
helmfile --environment default --selector name=cassandra sync
```

<Note>
The `sync` command differs from `apply` in that it will recreate resources if needed, which resolves the ConfigMap race condition.

</Note>

**Alternative Solution:**

If the above doesn't work, you can safely redeploy Cassandra (it's a dependency without complex initialization hooks):

```bash
# Delete the stuck migration job first
kubectl delete job -n cassandra-system cassandra-migrations

# Then redeploy Cassandra
HELMFILE_ENV=<environment-name> helmfile --selector name=cassandra apply
```

## Streaming Issues

### WebRTC Streaming Fails After Function Shows Active

**Symptom:**

- A streaming function deploys and shows ACTIVE
- WebRTC clients fail to connect with `NVST_R_GENERIC_ERROR`
- No errors in the function pod logs

**Root Cause:**

UDP traffic on the Kubernetes NodePort range (30000-32767) is blocked by a
cloud-provider network security rule. The function health checks pass over
TCP, so the function appears healthy, but the UDP media path is unreachable.

On Azure (AKS), AKS attaches a second NSG to node NICs in the managed
resource group (`MC_<resource-group>_<cluster>_<region>`). Even if the subnet NSG
allows UDP, the NIC NSG blocks it by default.

**Diagnosis:**

1. Confirm the function is ACTIVE and pods are running:

   ```bash
   kubectl get pods -n nvcf-backend -l nvcf-function-name=<function-name>
   ```

2. On Azure, check whether the NIC NSG has a UDP allow rule:

   ```bash
   MC_RG="MC_${RESOURCE_GROUP}_${CLUSTER_NAME}_${LOCATION}"
   for NIC_NSG in $(az network nsg list -g "$MC_RG" --query "[].name" -o tsv); do
     echo "=== $NIC_NSG ==="
     az network nsg rule list -g "$MC_RG" --nsg-name "$NIC_NSG" \
       --query "[?protocol=='Udp']" -o table
   done
   ```

   If any NSG is missing a UDP rule, add it to all of them:

   ```bash
   for NIC_NSG in $(az network nsg list -g "$MC_RG" --query "[].name" -o tsv); do
     az network nsg rule create -g "$MC_RG" --nsg-name "$NIC_NSG" \
       -n allow-udp-nodeports-webrtc --priority 510 \
       --direction Inbound --access Allow --protocol Udp \
       --source-address-prefix Internet --source-port-range "*" \
       --destination-address-prefix "*" --destination-port-range "30000-32767"
   done
   ```

See [Cloud Provider Network Requirements](./streaming-functions.md#cloud-provider-network-requirements)
for the full CSP networking checklist.

### gRPC Session Resumption Fails

#### Symptom

A streaming client receives a 404 (gRPC NotFound) error with "no existing
session found" when reconnecting to a function that was previously working. The
function shows active in the control plane. The proxy also clears the
`nvcf-request-id` cookie so the next retry starts a fresh session.

#### Root cause

The client is sending a stale `nvcf-reqid` header or `nvcf-request-id` cookie
from a previous session that no longer exists. The gRPC proxy looks up the
request ID, finds no matching worker session, and returns NotFound.

Common causes of stale request IDs:

- The worker pod was evicted or restarted between requests.
- The session timed out because the connection was idle too long.
- The client is reusing a request ID from a different function or function
  version.

#### Diagnosis

Check whether the client is sending the `nvcf-reqid` header or
`nvcf-request-id` cookie:

```bash
# Look for session-not-found errors in gRPC proxy logs.
kubectl logs -n nvcf -l app=grpc-proxy --tail=200 | grep "no existing session found\|worker connection not found"
```

If the logs show "no existing session found for request id", the client is
referencing an expired session.

#### Resolution

Remove the stale request ID from the client and reconnect without it. The proxy
creates a new session and returns a fresh request ID. Update the client to
handle 404 by discarding the stored request ID and retrying without it.

See [Session Resumption](./grpc-function-invocation.md#session-resumption) for
the full request ID lifecycle.

## Getting Help

When requesting support, provide:

1. **Environment details:**

   ```bash
   kubectl version
   helm version
   kubectl get nodes -o wide
   ```

2. **Deployment configuration:**

   - Environment file (sanitized)
   - Secrets file structure (sanitized - no actual secrets!)

3. **Relevant logs:**

   ```bash
   kubectl logs -n <namespace>  <problematic-pod> > pod-logs.txt
   ```

4. **Events:**

   ```bash
   kubectl get events -n <namespace> --sort-by='.lastTimestamp' > events.txt
   ```

5. **Resource status:**

   ```bash
   kubectl get all -n <namespace>  -o wide > resources.txt
   ```

## Appendix: NVCA Force Cleanup Script

This script forcefully removes all NVCA components from a cluster. Use it when `helmfile destroy` hangs on NVCA cleanup, typically because functions are still deployed in `nvcf-backend`.

<Warning>
This script bypasses normal cleanup procedures by removing finalizers. Always try `helmfile destroy` first.

</Warning>

```
#!/bin/bash
# =============================================================================
# force-cleanup-nvcf.sh - NVCA Component Removal Script
# =============================================================================
# This script forcefully removes all NVCA components from a cluster.
# Use this as a LAST RESORT when normal cleanup methods fail due to:
# - Stuck finalizers on namespaces or custom resources
# - Orphaned resources blocking deletion
# - Partial deployments that need complete removal
#
# WARNING: This script will FORCEFULLY remove all NVCA resources, including
# removing finalizers which bypasses normal cleanup procedures.
#
# Usage: ./force-cleanup-nvcf.sh [--dry-run]
# =============================================================================

set -euo pipefail

# --- Configuration ---
# NVCA-related namespaces
NVCA_NAMESPACES=(
    "nvcf-backend"
    "nvca-system"
    "nvca-operator"
)

# CRDs created by NVCA components
NVCA_CRDS=(
    "nvcfbackends.nvcf.nvidia.io"
)

# --- Parse Arguments ---
DRY_RUN=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--dry-run]"
            echo ""
            echo "Options:"
            echo "  --dry-run            Show what would be deleted without making changes"
            echo "  -h, --help           Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

echo "=============================================="
echo "NVCA Force Cleanup Script"
echo "=============================================="
if $DRY_RUN; then
    echo "MODE: DRY-RUN (no changes will be made)"
fi
echo ""

# --- Step 1: Show and delete function pods in nvcf-backend ---
echo ">>> Step 1: Checking for function pods in nvcf-backend namespace..."
if kubectl get namespace nvcf-backend >/dev/null 2>&1; then
    pods=$(kubectl get pods -n nvcf-backend -o name 2>/dev/null || true)
    if [[ -n "$pods" ]]; then
        echo "    Found the following pods that will be deleted:"
        kubectl get pods -n nvcf-backend -o wide 2>/dev/null || true
        echo ""
        if ! $DRY_RUN; then
            echo "    Deleting all pods in nvcf-backend..."
            kubectl delete pods -n nvcf-backend --all --force --grace-period=0 2>/dev/null || true
        else
            echo "[DRY-RUN] Would delete all pods in nvcf-backend namespace"
        fi
    else
        echo "    No pods found in nvcf-backend namespace"
    fi
else
    echo "    nvcf-backend namespace not found, skipping..."
fi
echo ""

# --- Step 2: Delete NVCFBackend Custom Resources ---
echo ">>> Step 2: Deleting NVCFBackend custom resources..."
if kubectl get crd nvcfbackends.nvcf.nvidia.io >/dev/null 2>&1; then
    echo "    Found NVCFBackend CRD, deleting all instances..."
    if ! $DRY_RUN; then
        kubectl delete nvcfbackends -A --all --wait=false 2>/dev/null || true
        echo "    Waiting 15 seconds for operator cleanup..."
        sleep 15
    else
        echo "[DRY-RUN] Would delete all NVCFBackends and wait for cleanup"
    fi
else
    echo "    NVCFBackend CRD not found, skipping..."
fi
echo ""

# --- Step 3: Delete Helm Releases ---
echo ">>> Step 3: Deleting Helm releases in NVCA namespaces..."
for ns in "${NVCA_NAMESPACES[@]}"; do
    if kubectl get namespace "$ns" >/dev/null 2>&1; then
        releases=$(helm list -n "$ns" -q 2>/dev/null || true)
        if [[ -n "$releases" ]]; then
            for release in $releases; do
                echo "    Deleting Helm release: $release (namespace: $ns)"
                if ! $DRY_RUN; then
                    helm delete -n "$ns" "$release" --wait=false 2>/dev/null || true
                fi
            done
        fi
    fi
done
echo ""

# --- Step 4: Force-delete stuck NVCFBackend resources (remove finalizers) ---
echo ">>> Step 4: Removing finalizers from stuck NVCFBackend resources..."
if kubectl get crd nvcfbackends.nvcf.nvidia.io >/dev/null 2>&1; then
    nvcfbackends=$(kubectl get nvcfbackends -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
    if [[ -n "$nvcfbackends" ]]; then
        while IFS= read -r backend; do
            if [[ -n "$backend" ]]; then
                ns=$(echo "$backend" | cut -d'/' -f1)
                name=$(echo "$backend" | cut -d'/' -f2)
                echo "    Removing finalizers from NVCFBackend: $name (namespace: $ns)"
                if ! $DRY_RUN; then
                    kubectl patch nvcfbackend "$name" -n "$ns" -p '{"metadata":{"finalizers":[]}}' --type=merge 2>/dev/null || true
                    kubectl delete nvcfbackend "$name" -n "$ns" --wait=false 2>/dev/null || true
                fi
            fi
        done <<< "$nvcfbackends"
    else
        echo "    No stuck NVCFBackend resources found"
    fi
else
    echo "    NVCFBackend CRD not found, skipping..."
fi
echo ""

# --- Step 5: Delete Namespaces ---
echo ">>> Step 5: Deleting NVCA namespaces..."
for ns in "${NVCA_NAMESPACES[@]}"; do
    if kubectl get namespace "$ns" >/dev/null 2>&1; then
        echo "    Deleting namespace: $ns"
        if ! $DRY_RUN; then
            kubectl delete namespace "$ns" --wait=false 2>/dev/null || true
        fi
    fi
done
echo "    Waiting 10 seconds for namespace deletion..."
if ! $DRY_RUN; then
    sleep 10
fi
echo ""

# --- Step 6: Force-remove finalizers from stuck namespaces ---
echo ">>> Step 6: Removing finalizers from stuck namespaces..."
for ns in "${NVCA_NAMESPACES[@]}"; do
    phase=$(kubectl get namespace "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [[ "$phase" == "Terminating" ]]; then
        echo "    Namespace $ns is stuck in Terminating, removing finalizers..."
        if ! $DRY_RUN; then
            # First, try to remove finalizers from all resources in the namespace
            for resource_type in deployments statefulsets daemonsets replicasets pods services configmaps secrets serviceaccounts roles rolebindings; do
                kubectl get "$resource_type" -n "$ns" -o name 2>/dev/null | while read -r resource; do
                    kubectl patch "$resource" -n "$ns" -p '{"metadata":{"finalizers":[]}}' --type=merge 2>/dev/null || true
                done
            done
            
            # Remove namespace finalizers using the API
            kubectl get namespace "$ns" -o json | \
                jq '.spec.finalizers = []' | \
                kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f - 2>/dev/null || true
        fi
    fi
done
echo ""

# --- Step 7: Delete CRDs ---
echo ">>> Step 7: Deleting NVCA CRDs..."
for crd in "${NVCA_CRDS[@]}"; do
    if kubectl get crd "$crd" >/dev/null 2>&1; then
        echo "    Deleting CRD: $crd"
        if ! $DRY_RUN; then
            kubectl delete crd "$crd" --wait=false 2>/dev/null || true
        fi
    fi
done
echo ""

# --- Step 8: Verification ---
echo ">>> Step 8: Verification..."
echo ""
echo "Remaining NVCA namespaces:"
remaining_ns=0
for ns in "${NVCA_NAMESPACES[@]}"; do
    if kubectl get namespace "$ns" >/dev/null 2>&1; then
        phase=$(kubectl get namespace "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
        echo "    - $ns (status: $phase)"
        remaining_ns=$((remaining_ns + 1))
    fi
done
if [[ $remaining_ns -eq 0 ]]; then
    echo "    None - all namespaces removed successfully"
fi

echo ""
echo "Remaining NVCA CRDs:"
remaining_crds=0
for crd in "${NVCA_CRDS[@]}"; do
    if kubectl get crd "$crd" >/dev/null 2>&1; then
        echo "    - $crd"
        remaining_crds=$((remaining_crds + 1))
    fi
done
if [[ $remaining_crds -eq 0 ]]; then
    echo "    None - all CRDs removed successfully"
fi

echo ""
echo "=============================================="
if $DRY_RUN; then
    echo "DRY-RUN complete. No changes were made."
else
    if [[ $remaining_ns -eq 0 ]] && [[ $remaining_crds -eq 0 ]]; then
        echo "Cleanup complete! All NVCA resources have been removed."
    else
        echo "Cleanup finished with some resources remaining."
        echo "You may need to run this script again or investigate manually."
    fi
fi
echo "=============================================="

```

[force-cleanup-nvcf.sh](samples/scripts/force-cleanup-nvcf.sh)

**Usage:**

1. Download or copy the script to your working directory

2. Make executable: `chmod +x force-cleanup-nvcf.sh`

3. Preview what will be deleted:

   ```bash
   ./force-cleanup-nvcf.sh --dry-run
   ```

4. Run the cleanup:

   ```bash
   ./force-cleanup-nvcf.sh
   ```

**What the script does:**

1. Lists and force-deletes all function pods in `nvcf-backend` namespace
2. Deletes all NVCFBackend custom resources
3. Deletes Helm releases in NVCA namespaces
4. Removes finalizers from stuck NVCFBackend resources
5. Deletes the NVCA namespaces (`nvcf-backend`, `nvca-system`, `nvca-operator`)
6. Removes finalizers from namespaces stuck in Terminating state
7. Deletes the NVCFBackend CRD
8. Verifies cleanup completion
