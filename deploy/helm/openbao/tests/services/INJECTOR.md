# OpenBao K8s Injector Test

This guide provides instructions for testing the OpenBao agent injector on a local 
Kubernetes cluster using Colima.

## Overview

The `nginx.yaml` manifest deploys:
- ServiceAccount `nvcf-api` in namespace `nvcf`
- Pod `nvcf-api` with OpenBao agent injection annotations
- ConfigMap with Vault Agent configuration for JWT auth and secret templates

## Prerequisites

- OpenBao cluster running (`make install additional_values=values.local.yaml`)
- Colima profile running (e.g., `amd64`)
- Migrations completed (creates `nvcf-api` JWT role and secrets)

## Test Injector

Deploy a test pod with OpenBao annotations to verify secret injection:

```bash
./tests/services/test.sh <COLIMA_PROFILE>
# Example: ./tests/services/test.sh amd64
```

**What it does**:
1. Creates `nvcf` namespace
2. Deploys `nvcf-api` pod with OpenBao annotations
3. Waits for pod to be ready
4. Verifies injector injected the agent sidecar
5. Shows injected agent image version
6. Checks if secrets were rendered

**Expected**: Pod shows `2/2` containers (nginx + openbao-agent)

## Re-deploy Pod

```bash
kubectl delete pod nvcf-api -n nvcf
./tests/services/test.sh <COLIMA_PROFILE>
```

## Cleanup

```bash
./tests/services/test.sh <COLIMA_PROFILE> --cleanup
```

## Troubleshooting

**Agent logs**:
```bash
kubectl logs nvcf-api -n nvcf -c openbao-agent
```

**Pod events**:
```bash
kubectl describe pod nvcf-api -n nvcf
```

**Check injected agent version**:
```bash
kubectl get pod nvcf-api -n nvcf -o jsonpath='{.spec.containers[?(@.name=="openbao-agent")].image}'
```
