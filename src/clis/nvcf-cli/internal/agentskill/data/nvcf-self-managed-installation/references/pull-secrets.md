# Image Pull Secrets Reference

## Overview

When pulling NVCF images from a private registry (e.g., NGC `nvcr.io`), Kubernetes needs `imagePullSecrets` on every pod. The helmfile stack supports this through `global.imagePullSecrets` in the environment values.

This eliminates per-chart configuration and does not require an admission controller.

## Helmfile Approach

### 1. Create pull secrets

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

For non-NGC registries, replace `--docker-server`, `--docker-username`, and `--docker-password`.

### 2. Reference the secret

```yaml
global:
  imagePullSecrets:
    - name: nvcr-pull-secret
```

### 3. Deploy normally

```bash
HELMFILE_ENV=<env> helmfile sync
```

The local `nvcf-cli self-hosted up` workflow creates or updates these secrets automatically when `NGC_API_KEY` is set.

## Verification

```bash
# Check that a pod has the configured secret
kubectl get pod -n <namespace> <pod-name> -o jsonpath='{.spec.imagePullSecrets}'
# Expected: [{"name":"nvcr-pull-secret"}]

# Check pull events
kubectl get events -n <namespace> --sort-by='.lastTimestamp' | grep -i pull
# Should show "Successfully pulled" not "401 Unauthorized"
```

## When Pull Secrets Are Not Needed

- **AWS ECR with IAM node roles**: If your nodes have `AmazonEC2ContainerRegistryReadOnly` IAM policy, Kubernetes can pull from ECR without explicit secrets.
- **Public registries**: No pull secrets needed.
- **CSP built-in credential helpers**: GKE Artifact Registry, Azure ACR with managed identity, etc.

## Troubleshooting

### Pods still in ImagePullBackOff after configuring the environment

The helmfile value only affects pods created after the value is applied. Delete stuck pods to trigger recreation:

```bash
kubectl delete pods -n <namespace> --all
```

### Wrong secret name

The environment references `nvcr-pull-secret`. Verify the secret exists with that exact name:

```bash
kubectl get secret nvcr-pull-secret -n <namespace>
```
