# NVSNAP Test Manifests

Test manifests for GPU checkpoint/restore testing.

## Prerequisites

1. **CRIU Bundle deployed on GPU nodes:**
   ```bash
   # Build the bundle
   ./scripts/build-criu-bundle.sh
   
   # Deploy to nodes (uses scripts/install-criu.sh)
   ./scripts/install-criu.sh
   ```

2. **NVSNAP Agent running on GPU nodes:**
   ```bash
   # The agent provides the checkpoint API
   curl http://<node-ip>:8081/health
   ```

## Test Files

| File | Description |
|------|-------------|
| `01-pytorch-test.yaml` | Simple PyTorch GPU test pod |
| `02-restore-pod.yaml` | Generic restore pod template |
| `03-vllm-test.yaml` | vLLM with small model (opt-125m) |
| `04-vllm-restore.yaml` | vLLM restore pod template |

## Quick Test: PyTorch

```bash
# 1. Start test pod
kubectl apply -f 01-pytorch-test.yaml

# 2. Wait for "ALLOCATED" message
kubectl logs -f gpu-test

# 3. Checkpoint (replace NODE_IP with your GPU node IP)
curl -X POST http://<NODE_IP>:8081/v1/checkpoint \
  -H "Content-Type: application/json" \
  -d '{"namespace":"default","podName":"gpu-test","containerName":"gpu"}'

# 4. Note the checkpointId from response

# 5. Delete original pod
kubectl delete pod gpu-test

# 6. Update 02-restore-pod.yaml with checkpoint path and apply
kubectl apply -f 02-restore-pod.yaml

# 7. Verify restore
kubectl logs gpu-restore
```

## Quick Test: vLLM

```bash
# 1. Start vLLM (model download takes a few minutes)
kubectl apply -f 03-vllm-test.yaml

# 2. Wait for "Uvicorn running on http://0.0.0.0:8000"
kubectl logs -f vllm-test

# 3. Test the API
curl http://<NODE_IP>:8000/v1/models

# 4. Checkpoint
curl -X POST http://<NODE_IP>:8081/v1/checkpoint \
  -H "Content-Type: application/json" \
  -d '{"namespace":"default","podName":"vllm-test","containerName":"vllm"}'

# 5. Delete and restore
kubectl delete pod vllm-test
# Update 04-vllm-restore.yaml with checkpoint path
kubectl apply -f 04-vllm-restore.yaml

# 6. Test restored vLLM
curl http://<NODE_IP>:8000/v1/models
```

## Customizing for Your Cluster

Before applying, update these fields in the YAML files:

1. **nodeName**: Set to your GPU node hostname
   ```yaml
   spec:
     nodeName: your-gpu-node-name
   ```

2. **Checkpoint path** (restore pods only):
   ```yaml
   volumes:
   - name: checkpoint
     hostPath:
       path: /var/lib/nvsnap/checkpoints/YOUR_CHECKPOINT_ID
   ```

## Troubleshooting

### CRIU Restore Fails

Check the restore log:
```bash
kubectl exec gpu-restore -- cat /tmp/restore.log | grep -i error
```

Common issues:
- **LSM mismatch**: Add `--skip-lsm` (already included in templates)
- **File not found**: The checkpoint may reference files not in the container
- **Network errors**: Using `--empty-ns net` to create fresh network namespace

### GPU Restore Fails

Check cuda-checkpoint state:
```bash
kubectl exec gpu-restore -- /criu-bundle/cuda-checkpoint --get-state --pid <PID>
```

States: `running`, `checkpointed`, `locked`

### Process Exits Immediately

The restored process may exit if:
- stdout/stderr pipes are broken (process should handle SIGPIPE)
- Required files are missing in the container filesystem
