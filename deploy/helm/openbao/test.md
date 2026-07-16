# OpenBao Testing Guide

## Quick Reference

| Task | Command |
|------|---------|
| **Fresh Install (baseline)** | `make install additional_values=values.local.yaml` |
| **Fresh Install (new version)** | `make install additional_values=values.local.yaml,upgrade/values-upgrades.yaml` |
| **Uninstall** | `make uninstall` |
| **Setup LoadBalancer** | `./utils/lb.sh` |
| **Cleanup LoadBalancer** | `./utils/lb.sh --cleanup` |
| **HA Test: Seal Status** | `./tests/openbao/loadbalancer_ha_seal_status_check.sh <profile>` |
| **HA Test: KV Read/Write** | `./tests/openbao/loadbalancer_ha_kv_rw_check.sh <profile>` |
| **HA Test: Basic KV** | `./tests/openbao/kv_rw_test.sh <profile>` |
| **Upgrade** | See [upgrade runbook](./upgrade/runbook.md) |

> **Important**: Set your kubectl context before running install/uninstall commands.
> **Version config**: Baseline in `values.local.yaml`, new version in `upgrade/values-upgrades.yaml`

---

## Prerequisites

- **Image Pull Secrets**: Configure NGC credentials — see [Image Pull Secrets](README.md#image-pull-secrets---ngcnvcrio)
- **Kubernetes Cluster**: Running cluster (e.g., Colima, k3d, minikube)
  ```bash
  # Example with Colima (isolated setup for testing)
  colima start --profile amd64 --kubernetes
  ```
- **Set kubectl context**: Ensure you're targeting the correct cluster
  ```bash
  # Check current context
  kubectl config current-context

  # List context
  kubectl config get-contexts
  
  # Switch context if needed (example for Colima)
  kubectl config use-context colima-amd64
  ```

---

## 1. Deploy a Fresh Cluster

### Option A: Deploy with baseline version
```bash
make uninstall                                    # Clean previous deployment
make install additional_values=values.local.yaml  # Deploy baseline version
```

### Option B: Deploy with new version
```bash
# 1. Build the image locally (from repo root)
unset DOCKER_HOST
docker context use <context>

# if image not available in nvcr.io, then build it locally
VERSION=2.4.4
docker build -f upgrade/Dockerfile.upgrade \
  --build-arg OPENBAO_VERSION=${VERSION} \
  -t <your-registry>/<your-org>/nvcf-openbao:${VERSION} .

# 3. Deploy (ensure correct kubectl context first)
kubectl config use-context colima-amd64
make uninstall
make install additional_values=values.local.yaml,upgrade/values-upgrades.yaml
```

### Verify Deployment
```bash
kubectl get pods -n vault-system
./tools/raft-list-peers.sh
```

#### HA Tests

> **Setup**: Run `./utils/lb.sh` once before running HA tests. LoadBalancer persists across tests.

### Test 1: Seal Status Check
Continuously verifies seal status through LoadBalancer during pod failures.

```bash
# Terminal 1: Run test
./tests/openbao/loadbalancer_ha_seal_status_check.sh amd64

# Terminal 2: Simulate failures
kubectl delete pod openbao-server-0 -n vault-system --force --grace-period=0
```

**Expected**: Seal status remains `false`, LoadBalancer routes to healthy pods.

---

### Test 2: KV Read/Write/Delete
Continuous KV operations through LoadBalancer to verify data plane HA.

```bash
# Terminal 1: Run test
./tests/openbao/loadbalancer_ha_kv_rw_check.sh amd64

# Terminal 2: Simulate failures
./tools/operator-stepdown.sh
```

**Expected**: All operations succeed continuously, no interruption during failures.

---

### Test 3: Basic KV Test
Direct KV operations against individual pods.

```bash
# Terminal 1: Run test
./tests/openbao/kv_rw_test.sh amd64

# Terminal 2: Kill a pod
kubectl delete pod openbao-server-1 -n vault-system --force --grace-period=0
```

**Expected**: Operations continue unaffected.


## 2. Upgrade an Existing Cluster
For detailed walkthrough, see **[upgrade runbook](./upgrade/runbook.md)**.
