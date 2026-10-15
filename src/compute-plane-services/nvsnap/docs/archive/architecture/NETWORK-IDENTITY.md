# NVSNAP Network Identity Architecture

## Problem Statement

When a Kubernetes pod is checkpointed and restored, it gets a **new IP address**. Applications that bind to or cache the original pod IP will fail:

- **Bind failures**: Workers that `bind(old_pod_ip:port)` get `EADDRNOTAVAIL`
- **Connection failures**: Processes that `connect(old_pod_ip:port)` can't reach peers
- **Stale state**: Cached peer addresses in application memory are now invalid

This affects:
- vLLM (multi-process inference server)
- Ray clusters (distributed computing)
- Dask clusters (parallel computing)
- TorchElastic (distributed training)
- Any multi-process application using TCP for IPC

## Solution Overview

NVSNAP provides a **Network Identity Layer** that preserves network connectivity across checkpoint/restore:

```
┌─────────────────────────────────────────────────────────────┐
│                   NVSNAP Network Identity                     │
├─────────────────────────────────────────────────────────────┤
│  Layer 1: Loopback Alias     - Local bind() to old IP      │
│  Layer 2: DNAT Redirection   - Cross-pod connectivity       │
│  Layer 3: Stable VIP         - Long-term identity (future)  │
└─────────────────────────────────────────────────────────────┘
```

## Architecture

### Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                      NVSNAP Controller                            │
│                                                                  │
│  ┌──────────────────┐  ┌──────────────────┐  ┌───────────────┐  │
│  │ Checkpoint       │  │ RestoreGroup     │  │ IP Mapping    │  │
│  │ Controller       │  │ Controller       │  │ Service       │  │
│  └──────────────────┘  └──────────────────┘  └───────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│   NVSNAP Agent   │ │   NVSNAP Agent   │ │   NVSNAP Agent   │
│   (Node 1)      │ │   (Node 2)      │ │   (Node 3)      │
│                 │ │                 │ │                 │
│ • Loopback mgmt │ │ • Loopback mgmt │ │ • Loopback mgmt │
│ • DNAT rules    │ │ • DNAT rules    │ │ • DNAT rules    │
│ • CRIU ops      │ │ • CRIU ops      │ │ • CRIU ops      │
└─────────────────┘ └─────────────────┘ └─────────────────┘
```

### Data Flow

```
CHECKPOINT PHASE:
┌──────────┐    ┌───────────┐    ┌──────────────────────┐
│ kubectl  │───>│ Controller│───>│ Agent                │
│ checkpoint│   │           │    │                      │
└──────────┘    └───────────┘    │ 1. Freeze process    │
                                 │ 2. Capture pod IP    │
                                 │ 3. Discover peers    │
                                 │ 4. CRIU dump         │
                                 │ 5. Store metadata    │
                                 └──────────────────────┘

RESTORE PHASE:
┌──────────┐    ┌───────────┐    ┌──────────────────────┐
│ kubectl  │───>│ Controller│───>│ Agent                │
│ restore  │    │           │    │                      │
└──────────┘    │ Coordinate│    │ 1. Read metadata     │
                │ IP mapping│    │ 2. Setup loopback    │
                └───────────┘    │ 3. Apply DNAT        │
                                 │ 4. CRIU restore      │
                                 │ 5. Resume process    │
                                 └──────────────────────┘
```

## Layer 1: Loopback Alias (Single-Pod)

### Purpose
Allow applications to `bind()` to their original pod IP after restore.

### Mechanism
```bash
# Add original pod IP to loopback interface
ip addr add <ORIGINAL_POD_IP>/32 dev lo

# Guardrails: Prevent source IP leakage
iptables -t mangle -A OUTPUT -s <ORIGINAL_POD_IP>/32 -d <ORIGINAL_POD_IP>/32 -j ACCEPT
iptables -t mangle -A OUTPUT -s <ORIGINAL_POD_IP>/32 -d 127.0.0.0/8 -j ACCEPT
iptables -t mangle -A OUTPUT -s <ORIGINAL_POD_IP>/32 -j DROP
```

### Why Loopback?
| Property | eth0 | lo |
|----------|------|-----|
| ARP responses | Yes (dangerous) | No |
| CNI conflicts | Possible | None |
| Cluster routing | Affected | Unaffected |
| Local-only traffic | No | Yes |

### Implementation

**Checkpoint (Agent)**:
```go
// internal/agent/checkpoint.go
type CheckpointMetadata struct {
    // ... existing fields ...
    SourcePodIP string `json:"source_pod_ip"`
}

func (a *Agent) getPodIP(pid int) (string, error) {
    // Read from /proc/<pid>/net/tcp
    // Find non-loopback, non-0.0.0.0 address
    // Return first match
}
```

**Restore (Entrypoint)**:
```go
// cmd/restore-entrypoint/main.go
func setupLoopbackAlias(originalIP string) error {
    // 1. Add IP to lo
    exec.Command("ip", "addr", "add", originalIP+"/32", "dev", "lo").Run()
    
    // 2. Guardrails
    exec.Command("iptables", "-t", "mangle", "-A", "OUTPUT",
        "-s", originalIP+"/32", "-d", originalIP+"/32", "-j", "ACCEPT").Run()
    exec.Command("iptables", "-t", "mangle", "-A", "OUTPUT",
        "-s", originalIP+"/32", "-d", "127.0.0.0/8", "-j", "ACCEPT").Run()
    exec.Command("iptables", "-t", "mangle", "-A", "OUTPUT",
        "-s", originalIP+"/32", "-j", "DROP").Run()
    
    return nil
}
```

### Behavior

```
Before Restore:
  Pod IP: 192.168.67.200 (new)
  Worker tries: bind("192.168.67.131", 29500)  ← FAILS

After Loopback Alias:
  Pod IP: 192.168.67.200 (new)
  lo has: 192.168.67.131/32 (alias)
  Worker tries: bind("192.168.67.131", 29500)  ← SUCCEEDS (on lo)
  
  Main connects: connect("192.168.67.131", 29500)  ← SUCCEEDS (routes to lo)
```

## Layer 2: DNAT Redirection (Multi-Pod)

### Purpose
Allow pods to connect to peers using their original IPs.

### Mechanism
```bash
# For each peer: redirect old IP to new IP
iptables -t nat -A OUTPUT -d <PEER_OLD_IP> -j DNAT --to-destination <PEER_NEW_IP>
```

### When Needed
| Scenario | Loopback | DNAT |
|----------|----------|------|
| Single-pod (vLLM) | ✅ | ❌ |
| Multi-pod same node | ✅ | ✅ |
| Multi-pod multi-node (Ray) | ✅ | ✅ |

### Implementation

**Checkpoint (Agent)**:
```go
// internal/agent/checkpoint.go
type PeerPodInfo struct {
    PodName   string `json:"pod_name"`
    Namespace string `json:"namespace"`
    IP        string `json:"ip"`
    Role      string `json:"role"`  // "head", "worker", etc.
}

type CheckpointMetadata struct {
    // ... existing fields ...
    CheckpointGroup string        `json:"checkpoint_group,omitempty"`
    PeerPods        []PeerPodInfo `json:"peer_pods,omitempty"`
}

func (a *Agent) discoverPeers(pid int) ([]PeerPodInfo, error) {
    // 1. Read /proc/<pid>/net/tcp for ESTABLISHED connections
    // 2. Filter to cluster IP range
    // 3. Resolve IPs to pod names via K8s API
    // 4. Return peer list
}
```

**Coordination (Controller)**:
```go
// controllers/restoregroup_controller.go
type IPMapping struct {
    OldIP   string
    NewIP   string
    PodName string
}

func (r *RestoreGroupReconciler) computeMappings(group *GPURestoreGroup) []IPMapping {
    // 1. For each pod in group, get old IP from checkpoint metadata
    // 2. Get new IP from restored pod status
    // 3. Return mapping list
}

func (r *RestoreGroupReconciler) distributeMappings(mappings []IPMapping) {
    // Push mappings to all agents in the restore group
}
```

**Restore (Entrypoint)**:
```go
// cmd/restore-entrypoint/main.go
func applyPeerDNAT(mappings []IPMapping) error {
    for _, m := range mappings {
        if m.OldIP == m.NewIP {
            continue  // No rewrite needed
        }
        exec.Command("iptables", "-t", "nat", "-A", "OUTPUT",
            "-d", m.OldIP, "-j", "DNAT", "--to-destination", m.NewIP).Run()
    }
    return nil
}
```

### Coordination Flow

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│    User      │     │  Controller  │     │    Agents    │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       │ Create             │                    │
       │ GPURestoreGroup    │                    │
       │───────────────────>│                    │
       │                    │                    │
       │                    │ Phase 1: Create    │
       │                    │ placeholder pods   │
       │                    │───────────────────>│
       │                    │                    │
       │                    │<── New IPs ────────│
       │                    │                    │
       │                    │ Phase 2: Compute   │
       │                    │ old→new mappings   │
       │                    │                    │
       │                    │ Phase 3: Push      │
       │                    │ mappings to agents │
       │                    │───────────────────>│
       │                    │                    │
       │                    │ Phase 4: Agents    │
       │                    │ apply rules +      │
       │                    │ CRIU restore       │
       │                    │                    │
       │                    │<── All running ────│
       │                    │                    │
       │<── Success ────────│                    │
```

## Layer 3: Stable VIP (Future)

### Purpose
Eliminate the need for IP remapping by assigning stable virtual IPs.

### Mechanism
```bash
# At pod creation (before app starts)
ip addr add 10.200.0.42/32 dev lo

# App binds to VIP instead of pod IP
export RAY_NODE_IP=10.200.0.42
```

### VIP Allocation

```yaml
apiVersion: nvsnap.io/v1alpha1
kind: GPUWorkload
metadata:
  name: my-ray-cluster
spec:
  vipPool: "10.200.0.0/16"  # NVSNAP-managed pool
  pods:
    - name: head
      vip: 10.200.0.1  # Stable across restores
    - name: worker-0
      vip: 10.200.0.2
    - name: worker-1
      vip: 10.200.0.3
```

### Comparison

| Aspect | Loopback Alias | Stable VIP |
|--------|----------------|------------|
| App changes | None | Config/env var |
| Works for any app | Yes | Only if configurable |
| IP conflicts | Possible (old pod still exists) | Never |
| Multi-restore | Needs coordination | Just works |

## Metadata Schema

### CheckpointMetadata (Full)

```go
type CheckpointMetadata struct {
    // Identity
    Version       string `json:"version"`
    CheckpointID  string `json:"checkpoint_id"`
    
    // Source
    PodName       string `json:"pod_name"`
    Namespace     string `json:"namespace"`
    ContainerName string `json:"container_name"`
    ContainerID   string `json:"container_id"`
    
    // Network Identity (Layer 1)
    SourcePodIP   string `json:"source_pod_ip"`
    
    // Multi-Pod Coordination (Layer 2)
    CheckpointGroup string        `json:"checkpoint_group,omitempty"`
    PeerPods        []PeerPodInfo `json:"peer_pods,omitempty"`
    
    // Stable VIP (Layer 3 - Future)
    VIP             string `json:"vip,omitempty"`
    
    // GPU State
    GPUDevices    []GPUDevice `json:"gpu_devices,omitempty"`
    
    // Timestamps
    Timestamp     time.Time `json:"timestamp"`
    Duration      float64   `json:"duration_seconds"`
}
```

### GPURestoreGroup CRD

```yaml
apiVersion: nvsnap.io/v1alpha1
kind: GPURestoreGroup
metadata:
  name: ray-cluster-abc123-restore
spec:
  # Which checkpoints to restore together
  checkpointGroup: ray-cluster-abc123
  
  # Target placement
  pods:
    - checkpointId: ray-head-ckpt-001
      targetNode: node-1
      resources:
        nvidia.com/gpu: 1
    - checkpointId: ray-worker-0-ckpt-001
      targetNode: node-2
      resources:
        nvidia.com/gpu: 4

status:
  phase: Running  # Pending → Coordinating → Restoring → Running
  
  # Computed at restore time
  ipMappings:
    - podName: ray-head
      oldIP: 10.1.1.100
      newIP: 10.2.1.50
    - podName: ray-worker-0
      oldIP: 10.1.1.101
      newIP: 10.2.1.51
  
  # Per-pod status
  pods:
    - name: ray-head
      phase: Running
      ip: 10.2.1.50
    - name: ray-worker-0
      phase: Running
      ip: 10.2.1.51
```

## Security Considerations

### Guardrails

1. **Source IP Leakage Prevention**
   ```bash
   # Block outbound packets with old IP as source (except local)
   iptables -t mangle -A OUTPUT -s <OLD_IP>/32 -j DROP
   ```

2. **Scope Limitation**
   - Loopback alias only affects local netns
   - DNAT only affects OUTPUT chain (not FORWARD)
   - No impact on other pods or cluster routing

3. **Audit Logging**
   ```go
   log.Warn("Added loopback alias for old pod IP %s; " +
            "connections to this IP inside the pod will loop back", originalIP)
   ```

### Limitations

| Scenario | Supported | Notes |
|----------|-----------|-------|
| Intra-pod IPC | ✅ | Loopback alias |
| Inter-pod (same restore group) | ✅ | DNAT with coordination |
| External clients | ⚠️ | Must use new IP or Service |
| Pod IP as distributed identity | ⚠️ | Can cause self-hijack |

## Implementation Phases

### Phase 1: Single-Pod ✅ (Current)
- [x] Capture `source_pod_ip` at checkpoint
- [x] Add loopback alias at restore
- [x] Add iptables guardrails
- [ ] Fix `/host/proc` path issue
- [ ] End-to-end test with vLLM

### Phase 2: Multi-Pod (Next)
- [ ] Peer discovery at checkpoint
- [ ] `checkpoint_group` tagging
- [ ] `GPURestoreGroup` CRD
- [ ] Controller coordination logic
- [ ] DNAT application at restore
- [ ] End-to-end test with Ray

### Phase 3: Stable VIP (Future)
- [ ] VIP pool management
- [ ] VIP allocation API
- [ ] Init container for VIP setup
- [ ] Documentation for app configuration

## Testing

### Single-Pod Test
```bash
# 1. Deploy vLLM
kubectl apply -f deploy/k8s/vllm-small.yaml

# 2. Checkpoint
curl -X POST http://<agent>:8081/v1/checkpoint \
  -d '{"namespace":"nvsnap-system","podName":"vllm-small","containerName":"vllm"}'

# 3. Verify metadata has source_pod_ip
cat /var/lib/nvsnap/checkpoints/<id>/metadata.json | jq .source_pod_ip

# 4. Restore to new pod
kubectl apply -f deploy/k8s/vllm-restore-pod.yaml

# 5. Verify loopback alias
kubectl exec vllm-restore -- ip addr show lo

# 6. Test inference
curl http://<new-pod-ip>:8000/v1/completions -d '{"prompt":"Hello"}'
```

### Multi-Pod Test (Future)
```bash
# 1. Deploy Ray cluster
kubectl apply -f deploy/k8s/ray-cluster.yaml

# 2. Checkpoint all pods together
kubectl create -f - <<EOF
apiVersion: nvsnap.io/v1alpha1
kind: GPUCheckpointGroup
metadata:
  name: ray-cluster-ckpt
spec:
  selector:
    matchLabels:
      ray.io/cluster: my-cluster
EOF

# 3. Restore group
kubectl create -f - <<EOF
apiVersion: nvsnap.io/v1alpha1
kind: GPURestoreGroup
metadata:
  name: ray-cluster-restore
spec:
  checkpointGroup: ray-cluster-ckpt
EOF

# 4. Verify DNAT rules
kubectl exec ray-head-restored -- iptables -t nat -L OUTPUT

# 5. Test Ray job
ray job submit --address http://<head-ip>:8265 -- python test.py
```

## References

- [CRIU TCP Repair Mode](https://criu.org/TCP_connection)
- [Linux Network Namespaces](https://man7.org/linux/man-pages/man7/network_namespaces.7.html)
- [iptables NAT Tutorial](https://www.netfilter.org/documentation/HOWTO/NAT-HOWTO.html)
- [Ray Cluster Networking](https://docs.ray.io/en/latest/cluster/configure-manage-dashboard.html)
