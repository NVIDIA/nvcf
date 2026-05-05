# Image Pull Secrets Reference

## Overview

When pulling NVCF images from a private registry (e.g., NGC `nvcr.io`), Kubernetes needs `imagePullSecrets` on every pod. The recommended approach uses Kyverno to automatically inject the secret into all pods in NVCF namespaces at admission time.

This eliminates per-chart configuration -- no helmfile modifications needed.

## Kyverno Approach (Recommended)

### 1. Install Kyverno

```bash
helm repo add kyverno https://kyverno.github.io/kyverno/
helm repo update
helm install kyverno kyverno/kyverno -n kyverno --create-namespace
```

### 2. Create pull secrets

```bash
export NGC_API_KEY="<your-ngc-api-key>"

for ns in cassandra-system nats-system nvcf api-keys ess sis nvca-operator vault-system; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done

for ns in cassandra-system nats-system nvcf api-keys ess sis nvca-operator vault-system; do
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
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
            - "nvca-operator"
            - "nats-system"
            - "cassandra-system"
            - "vault-system"
      mutate:
        patchStrategicMerge:
          metadata:
            annotations:
              nvcf.nvidia.com/imagepullsecret-injected-by: kyverno
          spec:
            imagePullSecrets:
            - name: nvcr-pull-secret
```

```bash
kubectl apply -f kyverno-imagepullsecret-policy.yaml
```

### 4. Deploy normally

```bash
HELMFILE_ENV=<env> helmfile sync
```

No helmfile modifications needed. Kyverno injects the pull secret into every pod automatically.

## Verification

```bash
# Check that a pod has the injected secret
kubectl get pod -n <namespace> <pod-name> -o jsonpath='{.spec.imagePullSecrets}'
# Expected: [{"name":"nvcr-pull-secret"}]

# Check the Kyverno annotation
kubectl get pod -n <namespace> <pod-name> -o jsonpath='{.metadata.annotations.nvcf\.nvidia\.com/imagepullsecret-injected-by}'
# Expected: kyverno

# Check pull events
kubectl get events -n <namespace> --sort-by='.lastTimestamp' | grep -i pull
# Should show "Successfully pulled" not "401 Unauthorized"
```

## When Pull Secrets Are Not Needed

- **AWS ECR with IAM node roles**: If your nodes have `AmazonEC2ContainerRegistryReadOnly` IAM policy, Kubernetes can pull from ECR without explicit secrets.
- **Public registries**: No pull secrets needed.
- **CSP built-in credential helpers**: GKE Artifact Registry, Azure ACR with managed identity, etc.

## Troubleshooting

### Pods still in ImagePullBackOff after applying policy

The Kyverno policy only affects pods created **after** the policy is applied. Delete stuck pods to trigger recreation:

```bash
kubectl delete pods -n <namespace> --all
```

### Kyverno admission controller not running

```bash
kubectl get pods -n kyverno
# All pods should be Running
```

### Policy not matching

```bash
kubectl get clusterpolicy nvcf-add-imagepullsecrets
# Should show READY: True
```

### Wrong secret name

The policy references `nvcr-pull-secret`. Verify the secret exists with that exact name:

```bash
kubectl get secret nvcr-pull-secret -n <namespace>
```
