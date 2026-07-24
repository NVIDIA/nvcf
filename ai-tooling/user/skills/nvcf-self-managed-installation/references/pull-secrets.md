# Image Pull Secrets Reference

## Overview

When pulling NVCF images from a private registry (for example, NGC
`nvcr.io`), Kubernetes needs `imagePullSecrets` on every pod. Control-plane and
compute-plane credentials are configured in their own cluster contexts. Do not
use one mixed namespace list.

The recommended control-plane approach uses Kyverno to inject the secret into
control-plane pods at admission time. The compute-plane chart and operator use
the secret in `nvca-operator` and mirror it to `nvca-system`.

## Kyverno Approach (Recommended)

### 1. Install Kyverno

```bash
helm repo add kyverno https://kyverno.github.io/kyverno/
helm repo update
helm install kyverno kyverno/kyverno -n kyverno --create-namespace \
  --kube-context <control-plane-context>
```

### 2. Create control-plane pull secrets

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

For non-NGC registries, replace `--docker-server`, `--docker-username`, and `--docker-password`.

### 3. Apply Kyverno ClusterPolicy

```yaml
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: nvcf-add-imagepullsecrets
spec:
  background: false
  rules:
    - name: add-imagepullsecret-to-nvcf-pods
      match:
        any:
        - resources:
            kinds:
            - Pod
            namespaces:
            - "nvcf"
            - "api-keys"
            - "sis"
            - "ess"
            - "nats-system"
            - "cassandra-system"
            - "vault-system"
            - "cert-manager"
      mutate:
        patchStrategicMerge:
          metadata:
            annotations:
              nvcf.nvidia.com/imagepullsecret-injected-by: kyverno
          spec:
            imagePullSecrets:
            - name: nvcr-creds
```

```bash
kubectl --context <control-plane-context> apply \
  -f kyverno-imagepullsecret-policy.yaml
```

### 4. Install the control plane

Continue with the selected control-plane installation in
[Split Compute-Plane Installation](compute-plane-installation.md). No
control-plane helmfile modifications are needed. Kyverno injects the pull
secret into new control-plane pods.

## Compute-Plane Secret

Create the secret only in `nvca-operator` on the compute cluster:

```bash
kubectl --context <compute-plane-context> create namespace nvca-operator \
  --dry-run=client -o yaml | \
  kubectl --context <compute-plane-context> apply -f -

kubectl --context <compute-plane-context> create secret docker-registry nvcr-creds \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password="$NGC_API_KEY" \
  --namespace=nvca-operator \
  --dry-run=client -o yaml | \
  kubectl --context <compute-plane-context> apply -f -
```

Reference it in the compute-plane environment file:

```yaml
global:
  imagePullSecrets:
    - name: nvcr-creds
```

The chart and operator use that reference in `nvca-operator` and mirror the
secret to `nvca-system`. Do not create a broader manual compute namespace list. Follow
[Split Compute-Plane Installation](compute-plane-installation.md) for the
selected install flow.

## Verification

```bash
# Check that a control-plane pod has the injected secret
kubectl --context <control-plane-context> get pod -n <namespace> <pod-name> \
  -o jsonpath='{.spec.imagePullSecrets}'
# Expected: [{"name":"nvcr-creds"}]

# Check the Kyverno annotation
kubectl --context <control-plane-context> get pod -n <namespace> <pod-name> \
  -o jsonpath='{.metadata.annotations.nvcf\.nvidia\.com/imagepullsecret-injected-by}'
# Expected: kyverno

# Check pull events
kubectl --context <control-plane-context> get events -n <namespace> \
  --sort-by='.lastTimestamp' | grep -i pull
# Should show "Successfully pulled" not "401 Unauthorized"

# Check the chart/operator mirror on the compute cluster
kubectl --context <compute-plane-context> get secret nvcr-creds \
  -n nvca-system
```

## When Pull Secrets Are Not Needed

- **AWS ECR with IAM node roles**: If your nodes have `AmazonEC2ContainerRegistryReadOnly` IAM policy, Kubernetes can pull from ECR without explicit secrets.
- **Public registries**: No pull secrets needed.
- **CSP built-in credential helpers**: GKE Artifact Registry, Azure ACR with managed identity, and similar services.

## Troubleshooting

### Pods still in ImagePullBackOff after applying policy

The Kyverno policy only affects pods created **after** the policy is applied. Delete stuck pods to trigger recreation:

```bash
kubectl --context <target-context> delete pods -n <namespace> --all
```

### Kyverno admission controller not running

```bash
kubectl --context <control-plane-context> get pods -n kyverno
# All pods should be Running
```

### Policy not matching

```bash
kubectl --context <control-plane-context> get clusterpolicy nvcf-add-imagepullsecrets
# Should show READY: True
```

### Wrong secret name

The policy references `nvcr-creds`. Verify the secret exists with that exact name:

```bash
kubectl --context <target-context> get secret nvcr-creds -n <namespace>
```
