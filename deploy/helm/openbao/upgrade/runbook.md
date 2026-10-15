# OpenBao Upgrade Runbook

**Upgrade Path**: `<BASELINE_VERSION>` → `<TARGET_VERSION>`  
**Estimated Time**: ~15-20 minutes  
**Difficulty**: Intermediate  

> **Note**: Replace `<BASELINE_VERSION>` (e.g., `2.2.2-nv-1`) and `<TARGET_VERSION>` (e.g., `2.4.4`) with your actual versions throughout this runbook.

---

## Prerequisites

- **Image Pull Secrets**: Configure NGC credentials as described in [README.md - Image Pull Secrets](../README.md#image-pull-secrets---ngcnvcrio)
- **Colima**: Running Kubernetes cluster (e.g., `colima start --profile amd64 --kubernetes`)
- **kubectl context**: Set to your target cluster before running commands
  ```bash
  kubectl config use-context colima-amd64  # or your cluster context
  ```
- **Baseline Deployment**: OpenBao v2.2.2 deployed via `make install additional_values=values.local.yaml`

---

## What You'll Learn

This runbook walks you through a **near zero-downtime upgrade** of OpenBao in Kubernetes. By the end, you'll understand:

- How **RollingUpdate** restarts pods automatically (no manual `kubectl delete` needed)
- How **PreStop hooks** gracefully hand off leadership before pod termination
- How to **measure HA availability** during rolling upgrades
- How to **validate** the upgrade with continuous testing

---

## Architecture Overview

We deploy a 3-node OpenBao cluster locally using Colima (Kubernetes on macOS):

```
┌─────────────────────────────────────────────────────────────────┐
│                      OpenBao HA Cluster                         │
│                                                                 │
│  ┌─────────────┐   ┌─────────────┐   ┌─────────────┐            │
│  │ openbao-0   │   │ openbao-1   │   │ openbao-2   │            │
│  │  (Leader)   │◄──│ (Follower)  │◄──│ (Follower)  │            │
│  │   v2.2.2    │   │   v2.2.2    │   │   v2.2.2    │            │
│  └─────────────┘   └─────────────┘   └─────────────┘            │
│         │                 │                 │                   │
│         └─────────────────┴─────────────────┘                   │
│                     Raft Consensus                              │
└─────────────────────────────────────────────────────────────────┘
```

### Testing Strategy: LoadBalancer

To accurately measure the impact of a rolling upgrade with HA enabled, we stand up a **LoadBalancer** in front of the cluster:

```
┌─────────────────────────────────────────────────────────────────┐
│  Test Traffic → LoadBalancer (MetalLB IP) → Healthy Pods        │
└─────────────────────────────────────────────────────────────────┘
```

> The LoadBalancer IP is assigned by MetalLB from the pool configured in `utils/lb.sh` 
> (default: `192.168.5.240-250`). The first available IP is typically `192.168.5.240`.

**Why LoadBalancer for testing?**
- Routes requests to healthy pods automatically
- Shows real availability during pod restarts
- Gives accurate picture of client-side impact during rollout

> 💡 **Note**: Production environments may use different ingress methods (Ingress Controller, Service Mesh, etc.). The LoadBalancer approach gives us the best estimate of HA behavior during upgrades, regardless of your production setup.

**During upgrade**, pods restart in reverse order (2 → 1 → 0). The LoadBalancer routes traffic to healthy pods, so we can measure actual downtime/errors.

---

## Prerequisites

### Tools Required

```bash
# Check these are installed
brew install yq        # YAML processor
brew install jq        # JSON processor
brew install kustomize # Kubernetes manifests
```

### Environment

| Component | Value |
|-----------|-------|
| Colima Profile | `<COLIMA_PROFILE>` (e.g., `amd64`) |
| Kubernetes | v1.32.1+k3s1 |
| Namespace | `vault-system` |

---

## Part 1: Reset to Baseline

> **Goal**: Start with a clean cluster running OpenBao 2.2.2-nv-1

### Step 1.1: Clean Up Existing Deployment

**What you're doing**: Removing any existing OpenBao installation to start fresh.

```bash
# Stop any running test scripts first (Ctrl+C in those terminals)

# Uninstall OpenBao
make uninstall
```

**What this does**:
- Runs `helm uninstall openbao-server`
- Deletes the `vault-system` namespace
- Removes all PVCs (Raft data is deleted!)

> ⚠️ **Warning**: This deletes all secrets stored in OpenBao. Only do this in test environments!

**If cluster-scoped resources remain** (webhooks, clusterroles):
```bash
kubectl delete mutatingwebhookconfiguration,clusterrole,clusterrolebinding \
  -l app.kubernetes.io/instance=openbao-server
```

### Step 1.2: Verify Cleanup

**What you're doing**: Confirming everything is gone before reinstalling.

```bash
# This should return "not found"
kubectl get ns vault-system
```

✅ **Expected**: `Error from server (NotFound): namespaces "vault-system" not found`

### Step 1.3: Install Baseline (2.2.2-nv-1)

**What you're doing**: Installing the "old" version that you'll upgrade from.

```bash
make install additional_values=values.local.yaml
```

⏱️ **Wait time**: ~2-3 minutes for pods to be ready

**Watch the installation**:
```bash
kubectl get pods -n vault-system -w
```

### Step 1.4: Verify Baseline Installation

**What you're doing**: Confirming all 3 pods are running the baseline version.

```bash
kubectl get pods -n vault-system
```

✅ **Expected output**:
```
NAME                                            READY   STATUS      RESTARTS   AGE
openbao-server-0                                2/2     Running     0          2m
openbao-server-1                                2/2     Running     0          2m
openbao-server-2                                2/2     Running     0          2m
openbao-server-agent-injector-xxx               1/1     Running     0          2m
openbao-server-initialize-cluster-xxx           0/1     Completed   0          2m
openbao-server-migrations-xxx                   0/1     Completed   0          1m
```

> 💡 **Tip**: `2/2` means both containers are running: the main `openbao` container and the `auto-unseal-sidecar`.

**Verify versions**:
```bash
for i in 0 1 2; do
  echo -n "openbao-server-$i: "
  kubectl exec openbao-server-$i -n vault-system -c openbao -- bao version | head -1
done
```

✅ **Expected**: All pods show `OpenBao v2.2.2`

### Step 1.5: Test Injector with Baseline Version (Optional)

**What you're doing**: Verifying the agent injector works before upgrading.

```bash
./tests/services/test.sh <COLIMA_PROFILE>
```

**What this does**:
1. Creates a `nvcf` namespace
2. Deploys an nginx pod with OpenBao annotations
3. Verifies the injector injects the agent sidecar
4. Shows if secrets are being rendered

✅ **Expected**: Pod shows `2/2` containers (nginx + openbao-agent)

**Cleanup** (or keep running to test post-upgrade):
```bash
./tests/services/test.sh <COLIMA_PROFILE> --cleanup
```

> 💡 You can leave this pod running during the upgrade to verify that existing injected pods 
> continue working after the server upgrade.

---

## Part 2: Setup LoadBalancer for Testing

> **Goal**: Stand up a LoadBalancer to measure real HA availability during the upgrade

### Why LoadBalancer for Testing?

Without a LoadBalancer, you'd connect directly to a specific pod. When that pod restarts during upgrade, your connection fails—but that doesn't tell you if the *cluster* is still available.

The LoadBalancer routes to healthy pods, giving you an accurate picture of client-side impact:

```
Direct pod:  Client → Pod-0 (restarting) → ❌ Connection Failed (pod down, but cluster OK)
Via LB:      Client → LoadBalancer → Pod-1 (healthy) → ✅ Request Succeeds (cluster available)
```

> 💡 This simulates what real clients experience in production, regardless of your actual ingress method.

### Step 2.1: Create LoadBalancer

**What you're doing**: Setting up MetalLB and creating a LoadBalancer service.

```bash
# Check if LoadBalancer already exists
kubectl get svc openbao-server-lb -n vault-system

# If not found, create it
./utils/lb.sh
```

### Step 2.2: Verify LoadBalancer

```bash
# Get the LoadBalancer IP
LB_IP=$(kubectl get svc openbao-server-lb -n vault-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
echo "LoadBalancer IP: $LB_IP"
```

✅ **Expected**: `192.168.5.240` (first IP from MetalLB pool configured in `utils/lb.sh`)

**Quick health check**:
```bash
./tests/openbao/loadbalancer_ha_seal_status_check.sh <COLIMA_PROFILE>
# Let it run for a few cycles, then Ctrl+C
```

✅ **Expected**: `Seal status = false` (unsealed and healthy)

---

## Part 3: Start Continuous Tests

> **Goal**: Run tests that will show cluster health during the upgrade

### Why Continuous Tests?

These tests run in a loop, constantly reading/writing to OpenBao. They'll show you:
- **Green**: Cluster is healthy, requests succeeding
- **Brief blips**: Normal during pod restarts (1-2 seconds)
- **Extended errors**: Something is wrong, investigate

### Step 3.1: Open Terminal Windows

You'll need:
| Terminal | Purpose |
|----------|---------|
| Terminal 1 | KV read/write test (keep running during upgrade) |
| Terminal 2 | Seal status test (keep running during upgrade) |

> 💡 The upgrade script can run in any terminal. It pauses to let you start the test 
> scripts, then handles the helm upgrade automatically when you press Enter.

### Step 3.2: Start KV Test (Terminal 1)

**What you're doing**: Continuously writing, reading, and deleting secrets.

```bash
# Terminal 1 - Keep this running during upgrade!
./tests/openbao/loadbalancer_ha_kv_rw_check.sh <COLIMA_PROFILE>
```

**What to watch for**:
- ✅ `SUCCESS` messages = cluster is healthy
- ⚠️ Occasional failures during upgrade = normal (LoadBalancer re-routing)
- ❌ Continuous failures = something is wrong

### Step 3.3: Start Seal Status Test (Terminal 2)

**What you're doing**: Continuously checking that pods stay unsealed.

```bash
# Terminal 2 - Keep this running during upgrade!
./tests/openbao/loadbalancer_ha_seal_status_check.sh <COLIMA_PROFILE>
```

**What to watch for**:
- ✅ `sealed: false` = pod is healthy and accepting requests
- ❌ `sealed: true` = pod needs manual unsealing (shouldn't happen with auto-unseal)

> 💡 **Let tests run for 1-2 minutes** to establish a baseline before starting the upgrade.

---

## Part 4: Execute the Upgrade

> **Goal**: Upgrade all pods from 2.2.2 to 2.4.4 with minimal downtime

### Step 4.1: (Optional) Watch Pods in Terminal 3

**What you're doing**: Observing the rolling restart in real-time.

```bash
# Terminal 3 (optional but recommended)
kubectl get pods -n vault-system -w
```

### Step 4.2: Run the Upgrade Script

**What you're doing**: Building the new image and triggering a rolling upgrade.

> ⚠️ **Important**: Keep Terminals 1 & 2 (the test scripts) running while you do this!

```bash
# In a new terminal (or the one you used for baseline verification)
./upgrade/upgrade-test.sh <COLIMA_PROFILE> 2.4.4
```

**The script will**:
1. Switch Docker context to your Colima profile
2. Build the 2.4.4 image (or skip if already built)
3. Show current pod versions
4. Prompt you to confirm before starting
5. **Automatically run** `helm upgrade` with RollingUpdate strategy
6. Verify all pods upgraded

> 💡 The script pauses and shows you what monitoring terminals to open. Once you confirm 
> Terminals 1 & 2 are running, press Enter and the script handles the rest.

### Step 4.3: What You'll See (Terminal 3 - Pod Watch)

```bash
kubectl get pods -n vault-system -w
```

**What you'll see**:

```
NAME               READY   STATUS        RESTARTS   AGE
openbao-server-2   2/2     Terminating   0          10m    ← Pod 2 shutting down
openbao-server-2   0/2     Pending       0          0s     ← New pod creating
openbao-server-2   0/2     Init:0/1      0          1s     ← Initializing
openbao-server-2   2/2     Running       0          30s    ← Back online with v2.4.4
openbao-server-1   2/2     Terminating   0          10m    ← Pod 1 next
... (repeats for pod 1, then pod 0)
```

**Restart order**: `2 → 1 → 0` (reverse ordinal order)

### What's Happening Behind the Scenes

```
┌──────────────────────────────────────────────────────────────────┐
│  Pod-2 Terminating                                               │
│  ┌─────────────────────────────────────────────────────────┐     │
│  │ 1. PreStop Hook Runs:                                   │     │
│  │    - Checks if this pod is the Raft leader              │     │
│  │    - If leader: runs "bao operator step-down"           │     │
│  │    - Waits 5s for new leader election                   │     │
│  │ 2. Container Terminates                                 │     │
│  │ 3. New Pod Created with v2.4.4 Image                    │     │
│  │ 4. Auto-Unseal Sidecar Unseals the Pod                  │     │
│  │ 5. Pod Joins Raft Cluster as Follower                   │     │
│  └─────────────────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────┘
```

---

## Part 5: Verify the Upgrade

> **Goal**: Confirm all pods are upgraded and the cluster is healthy

### Step 5.1: Check Pod Versions

```bash
for i in 0 1 2; do
  echo -n "openbao-server-$i: "
  kubectl exec openbao-server-$i -n vault-system -c openbao -- bao version | head -1
done
```

✅ **Expected**: All pods show `OpenBao v2.4.4`

### Step 5.2: Check Raft Cluster Health

```bash
./tools/raft-list-peers.sh
```

✅ **Expected**: 3 nodes (1 leader, 2 followers), all `voter: true`

### Step 5.3: Check Seal Status

```bash
for i in 0 1 2; do
  echo -n "openbao-server-$i: "
  kubectl exec openbao-server-$i -n vault-system -c openbao -- bao status 2>/dev/null | grep "Sealed"
done
```

✅ **Expected**: All pods show `Sealed: false`

### Step 5.4: Review Test Results

Go back to Terminals 1 and 2. Stop the tests with `Ctrl+C`.

**Analyze the output**:
- **Success rate >99%**: Upgrade successful ✅
- **Success rate 95-99%**: Acceptable, brief leader election delays
- **Success rate <95%**: Investigate what went wrong

---

## Part 6: Update Injector Agent Image

> **Goal**: Ensure newly injected pods use the matching OpenBao agent version

### Why This Matters

The agent injector injects a sidecar container into your application pods. If the agent version doesn't match the server, you'll see warnings:

```
OpenBao Agent version does not match OpenBao server version. 
Agent: 2.2.2, Server: 2.4.4
```

### Step 6.1: Verify Agent Image Updated

The `upgrade-test.sh` script includes the agent image in `/tmp/upgrade-values.yaml`, so this should already be done. Verify with:

```bash
kubectl get deploy openbao-server-agent-injector -n vault-system \
  -o jsonpath='{.spec.template.spec.containers[0].env}' | grep -i image
```

✅ **Expected**: Shows `2.4.4` tag

### Step 6.2: Re-test Injector with New Version

**What you're doing**: Delete the test pod and re-deploy to verify the upgraded injector 
injects the new agent version.

```bash
# Delete existing test pod (if running from Step 1.5)
./tests/services/test.sh <COLIMA_PROFILE> --cleanup

# Re-deploy test pod
./tests/services/test.sh <COLIMA_PROFILE>
```

**Verify the injected agent uses the new version**:
```bash
kubectl get pod -n nvcf -o jsonpath='{.items[0].spec.containers[?(@.name=="openbao-agent")].image}'
```

✅ **Expected**: Image tag shows `2.4.4`

**Cleanup when done**:
```bash
./tests/services/test.sh <COLIMA_PROFILE> --cleanup
```

> 💡 This confirms the injector is using the upgraded agent image for newly created pods.

---

## Quick Reference

### Commands Cheat Sheet

```bash
# Set context first
kubectl config use-context colima-amd64  # or your cluster context

# Full reset and reinstall
make uninstall
make install additional_values=values.local.yaml

# Test injector (optional)
./tests/services/test.sh <COLIMA_PROFILE>
./tests/services/test.sh <COLIMA_PROFILE> --cleanup

# Setup LoadBalancer
./utils/lb.sh

# Verify versions
for i in 0 1 2; do 
  kubectl exec openbao-server-$i -n vault-system -c openbao -- bao version | head -1
done

# Run upgrade
./upgrade/upgrade-test.sh <COLIMA_PROFILE> 2.4.4

# Verify versions
for i in 0 1 2; do 
  kubectl exec openbao-server-$i -n vault-system -c openbao -- bao version | head -1
done

# Check cluster health
./tools/raft-list-peers.sh

# Run HA tests
./tests/openbao/loadbalancer_ha_kv_rw_check.sh <COLIMA_PROFILE>
./tests/openbao/loadbalancer_ha_seal_status_check.sh <COLIMA_PROFILE>

# Cleanup LoadBalancer
./utils/lb.sh --cleanup
```
