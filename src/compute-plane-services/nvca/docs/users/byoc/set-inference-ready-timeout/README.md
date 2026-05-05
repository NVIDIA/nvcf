# NVCF Inference Ready Timeout Policy

> **TEMPORARY WORKAROUND**: This policy is a temporary workaround for [NVBug 5171112](https://nvbugspro.nvidia.com/bug/5171112). Once the fix is released in a future version of NVCA, this policy should be removed.

This document explains how to install and manage a Kyverno policy that sets the `INFERENCE_READY_TIMEOUT` environment variable for NVCF worker pods.

## Prerequisites

- Kubernetes cluster with administrative access
- `kubectl` command-line tool installed
- Helm (optional, for Kyverno installation)

## Configuring the Timeout Duration

The policy sets `INFERENCE_READY_TIMEOUT` to "90m" (90 minutes) by default. You can modify this duration by editing the policy file. The duration should be specified using Go's time.Duration string format:

- Valid time units are "ns", "us" (or "µs"), "ms", "s", "m", "h"
- Examples:
  - "90m" (90 minutes)
  - "2h" (2 hours)
  - "30m" (30 minutes)
  - "1h30m" (1 hour and 30 minutes)

To modify the timeout:

1. Edit the `nvcf-set-inference-ready-timeout-policy.yaml` file
2. Locate the `value: "90m"` line under the environment variable definition
3. Replace "90m" with your desired duration
4. Reapply the policy

Example modification:

```yaml
env:
  - name: INFERENCE_READY_TIMEOUT
    value: "2h" # Changed from "90m" to "2h"
```

## Installing Kyverno

You can install Kyverno using either Helm or kubectl:

### Using Helm (Recommended)

```bash
# Add the Kyverno Helm repository
helm repo add kyverno https://kyverno.github.io/kyverno/
helm repo update

# Install Kyverno
helm install kyverno kyverno/kyverno --namespace kyverno --create-namespace
```

### Using kubectl

```bash
kubectl create -f https://raw.githubusercontent.com/kyverno/kyverno/main/config/install.yaml
```

## Applying the Policy

1. Save the policy file as `nvcf-set-inference-ready-timeout-policy.yaml`
2. Apply the policy using kubectl:

```bash
kubectl apply -f nvcf-set-inference-ready-timeout-policy.yaml
```

3. Verify the policy is installed:

```bash
kubectl get clusterpolicy nvcf-set-inference-ready-timeout
```

## How it Works

This policy automatically adds the `INFERENCE_READY_TIMEOUT` environment variable set to "90m" to any NVCF worker pods that match either of these conditions:

- Pods in namespaces with the label `nvca.nvcf.nvidia.io/workload-instance-type: pod_spec`
- Pods named "utils" in namespaces with the label `nvca.nvcf.nvidia.io/workload-instance-type: helm_chart`

## Removing the Policy

**Important**: Once the fix for [NVBug 5171112](https://nvbugspro.nvidia.com/bug/5171112) is released in NVCA, you should remove this policy using:

```bash
kubectl delete clusterpolicy nvcf-set-inference-ready-timeout
```

Note: Removing the policy will not affect existing pods. Only new pods or updated pods will no longer receive the environment variable.

## Troubleshooting

To check if the policy is working:

1. View policy status:

```bash
kubectl describe clusterpolicy nvcf-set-inference-ready-timeout
```

2. Check Kyverno logs:

```bash
kubectl logs -n kyverno -l app.kubernetes.io/name=kyverno
```

3. Verify the environment variable on a pod:

```bash
kubectl get pod <pod-name> -o jsonpath='{.spec.containers[?(@.name=="utils")].env}'
```

## Additional Resources

- [Kyverno Documentation](https://kyverno.io/docs/)
- [Kyverno Policy Examples](https://kyverno.io/policies/)
- [Kyverno Slack Community](https://kubernetes.slack.com/messages/kyverno)

For additional support, please contact your NVIDIA support representative or reference [NVBug 5171112](https://nvbugspro.nvidia.com/bug/5171112) for more information about this workaround.
