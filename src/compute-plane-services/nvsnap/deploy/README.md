# NVSNAP Deployment Guide

This directory contains everything needed to deploy NVSNAP to GPU clusters.

## Quick Start

### 1. Configure Credentials

```bash
mkdir -p ~/.nvsnap
cp config/cluster.yaml.example ~/.nvsnap/cluster.yaml
# Edit with your cluster details
chmod 600 ~/.nvsnap/cluster.yaml
```

### 2. Build Components

```bash
cd /path/to/nvsnap
make build
```

### 3. Deploy to Cluster

```bash
# Deploy to all nodes in cluster
./deploy/nvsnap-installer --cluster ~/.nvsnap/cluster.yaml

# Or deploy to a single node
./deploy/nvsnap-installer --node 10.34.5.64

# Verify installation
./deploy/nvsnap-installer --verify
```

## Components Installed

| Component | Location | Description |
|-----------|----------|-------------|
| CRIU (forked) | `/usr/local/sbin/criu` | Checkpoint/Restore with NVIDIA fixes |
| cuda-checkpoint | `/usr/local/bin/cuda-checkpoint` | NVIDIA GPU state management |
| nvsnap-agent | `/usr/local/bin/nvsnap-agent` | API service for checkpoint operations |
| nvsnap (CLI) | `/usr/local/bin/nvsnap` | Command-line client |

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Kubernetes Cluster                       │
│  ┌───────────────┐    ┌───────────────┐    ┌───────────────┐│
│  │   GPU Node 1  │    │   GPU Node 2  │    │   GPU Node N  ││
│  │ ┌───────────┐ │    │ ┌───────────┐ │    │ ┌───────────┐ ││
│  │ │nvsnap-agent│ │    │ │nvsnap-agent│ │    │ │nvsnap-agent│ ││
│  │ │  :8081    │ │    │ │  :8081    │ │    │ │  :8081    │ ││
│  │ └─────┬─────┘ │    │ └─────┬─────┘ │    │ └─────┬─────┘ ││
│  │       │       │    │       │       │    │       │       ││
│  │ ┌─────┴─────┐ │    │ ┌─────┴─────┐ │    │ ┌─────┴─────┐ ││
│  │ │CRIU + GPU │ │    │ │CRIU + GPU │ │    │ │CRIU + GPU │ ││
│  │ └───────────┘ │    │ └───────────┘ │    │ └───────────┘ ││
│  └───────────────┘    └───────────────┘    └───────────────┘│
│                              │                               │
│           ┌──────────────────┴──────────────────┐           │
│           │     Shared Storage (NFS/S3)         │           │
│           │    /var/lib/nvsnap/checkpoints       │           │
│           └─────────────────────────────────────┘           │
└─────────────────────────────────────────────────────────────┘
```

## Cluster Configuration

Create `~/.nvsnap/cluster.yaml`:

```yaml
cluster:
  name: my-gpu-cluster
  
ssh:
  user: your_username
  # password or key_file (key preferred)
  password: your_password
  # key_file: ~/.ssh/id_rsa

nodes:
  - ip: 10.34.5.64
    name: gpu-node-1
    roles: [gpu, master]
  - ip: 10.86.2.83
    name: gpu-node-2
    roles: [gpu]
  - ip: 10.86.6.104
    name: gpu-node-3
    roles: [gpu]

components:
  criu:
    source: fork  # 'fork' uses nvsnap forked CRIU, 'system' uses package manager
    fork_path: $HOME/personal/criu-orig/criu  # if source=fork
  
  agent:
    port: 8081
    checkpoint_dir: /var/lib/nvsnap/checkpoints
    log_level: info
```

## API Endpoints

Once deployed, each node exposes:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/processes` | GET | List GPU processes |
| `/checkpoint` | POST | Checkpoint a process |
| `/restore` | POST | Restore from checkpoint |
| `/checkpoints` | GET | List checkpoints |

### Example Usage

```bash
# Health check
curl http://10.34.5.64:8081/health

# List GPU processes
curl http://10.34.5.64:8081/processes

# Checkpoint a process
curl -X POST http://10.34.5.64:8081/checkpoint \
  -H 'Content-Type: application/json' \
  -d '{"pid": 12345, "checkpointId": "my-checkpoint"}'

# Restore a process
curl -X POST http://10.34.5.64:8081/restore \
  -H 'Content-Type: application/json' \
  -d '{"checkpointPath": "/var/lib/nvsnap/checkpoints/my-checkpoint"}'
```

## Troubleshooting

### Check Agent Status
```bash
ssh user@node "systemctl status nvsnap-agent"
ssh user@node "journalctl -u nvsnap-agent -f"
```

### Verify CRIU
```bash
ssh user@node "criu check"
ssh user@node "criu --version"
```

### Test cuda-checkpoint
```bash
ssh user@node "cuda-checkpoint --help"
```

## Uninstall

```bash
./deploy/nvsnap-installer --uninstall --cluster ~/.nvsnap/cluster.yaml
```
