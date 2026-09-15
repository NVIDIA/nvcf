# Example: checkpoint and restore via the REST API

A minimal, dependency-free Go client that drives the full checkpoint/restore
lifecycle against `nvsnap-server`. Read [`main.go`](main.go) as a reference
for integrating nvsnap into your own tooling — it talks only to the
documented API ([`internal/server/openapi.yaml`](../../internal/server/openapi.yaml))
and imports nothing from nvsnap internals.

## What it does

1. `GET /api/v1/pods` — find a Running GPU pod
2. `POST /api/v1/checkpoints` — checkpoint it (async, returns `202` + id)
3. `GET /api/v1/checkpoints/{id}` — poll until `Completed`/`Failed`
4. `GET /api/v1/checkpoints?source=db` — list the durable catalog
5. `POST /api/v1/restores` — *(with `-restore`)* restore from the checkpoint
6. `DELETE /api/v1/checkpoints/{id}` — *(with `-delete`)* clean up

Single-GPU pods are captured with CRIU + cuda-checkpoint; multi-GPU pods
auto-route to the rootfs path. The API contract is identical either way.

## Run it

```bash
# Point at nvsnap-server (port-forward it first if it's in-cluster):
kubectl -n nvsnap-system port-forward svc/nvsnap-server 8080:8080 &

# Checkpoint the first Running GPU pod in the default namespace:
go run ./examples/checkpoint-restore -server http://localhost:8080 -namespace default

# Checkpoint a specific pod, then restore and delete it:
go run ./examples/checkpoint-restore \
    -server http://localhost:8080 \
    -namespace default \
    -pod my-vllm-pod \
    -restore -delete
```

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-server` | `http://localhost:8080` | nvsnap-server base URL |
| `-namespace` | `default` | namespace to operate in |
| `-pod` | *(auto)* | pod to checkpoint; defaults to the first Running GPU pod |
| `-leave-running` | `false` | keep the source process running after checkpoint |
| `-restore` | `false` | restore from the new checkpoint |
| `-delete` | `false` | delete the checkpoint at the end |
| `-timeout` | `15m` | max wait for an async op |
