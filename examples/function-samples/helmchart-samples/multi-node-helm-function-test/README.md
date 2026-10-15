# Multi-node Helm Chart

This sample supports three networking environments for multi-node GPU testing:

- AWS GB200 / EFA: Uses [Elastic Fabric Adapter](https://aws.amazon.com/hpc/efa/) for inter-node communication. The chart profile configures EFA-specific resources and annotations.
- AWS GB300 / DRA RoCE: Uses AWS network DRA with `roce.networking.k8s.aws` claims for GB300 clusters.
- NCP / Mellanox (mlx5): Uses Mellanox ConnectX NICs (mlx5 driver) for RDMA networking. This is the default chart profile and can be reused for any cluster with Mellanox/InfiniBand networking.

## Configuration Setup

Before running the test scripts, you need to configure your NVCF credentials:

1. Copy the sample configuration file:
```bash
cp config.env.sample config.env
```

2. Edit `config.env` and replace the placeholder values with your actual credentials:
   - `KEY`: Your NVIDIA Cloud Functions API key (get it from https://org.ngc.nvidia.com/setup/api-keys)
   - `FUNCTION_ID`: Your deployed function ID (single-node or multi-node)

Example `config.env`:
```bash
KEY="nvapi-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
FUNCTION_ID="ce460ed1-6f17-4bdc-ad6b-00a569fc780d"
```

**Note:** The `config.env` file is gitignored to prevent accidentally committing sensitive API keys.

## Building the Container

The container base image is configurable via the `BASE_IMAGE` build argument. Each environment requires a base image with the appropriate networking stack pre-installed.

**AWS GB200 / EFA** (default):
```bash
docker build -t multi-node-test container/
```

The default base image includes the AWS EFA libraries and NCCL aws-ofi plugin needed for EFA communication.

**NCP / Mellanox mlx5**:
```bash
docker build \
  --build-arg BASE_IMAGE=ghcr.io/coreweave/nccl-tests:13.0.2-devel-ubuntu22.04-nccl2.29.2-1-d73ec07 \
  -t multi-node-test container/
```

This base image includes Mellanox OFED drivers for RDMA/InfiniBand networking.

| Environment | Networking | Base Image |
|-------------|-----------|-----------|
| AWS GB200 | EFA (`vpc.amazonaws.com/efa`) | `public.ecr.aws/hpc-cloud/nccl-tests:latest` |
| AWS GB300 | DRA RoCE (`roce.networking.k8s.aws`) | `public.ecr.aws/hpc-cloud/nccl-tests:latest` |
| NCP / Mellanox | mlx5 (`nvidia.com/mlnxnics`) | `ghcr.io/coreweave/nccl-tests:13.0.2-devel-ubuntu22.04-nccl2.29.2-1-d73ec07` |

## Setup Override

Copy the single sample override file:

```bash
cp override.yaml.sample override.yaml
```

The sample sets `clusterProfile: auto`. In auto mode, Helm looks at the target Kubernetes cluster during rendering and selects one of the built-in profiles:

- `aws-gb300` when `DeviceClass/roce.networking.k8s.aws` exists in `resource.k8s.io/v1`
- `aws-gb200` when any node advertises allocatable `vpc.amazonaws.com/efa`
- `ncp-gb200` when any node advertises allocatable `nvidia.com/mlnxnics`

Auto mode needs render-time permission to read `DeviceClass` resources and list `Node` resources. If the renderer cannot use live lookup, set the profile explicitly:

```bash
helm template test multi-node-test --set clusterProfile=aws-gb200
helm template test multi-node-test --set clusterProfile=aws-gb300
helm template test multi-node-test --set clusterProfile=ncp-gb200
```

You can also set the same fallback in `override.yaml`:

```yaml
clusterProfile: aws-gb200
```

Top-level overrides remain supported for `nodesPerInstance`, `image`, `resources`, `podAnnotations`, `securityContext`, and `resourceClaimTemplate`. Use those only when the built-in profile needs local tuning.

## Deploy the Helm Chart

```bash
ngc cf function deploy create --org <org> --deployment-specification <cluster>:<gpu-name>:<instance>:1:1 <function-id>:<version-id> --configuration-file override.yaml
```

## Run test against endpoint

### Using Test Scripts

The repository includes test scripts that automatically use your configured credentials from `config.env`:

**NCCL Test:**
```bash
./test_nccl.sh
```

**Bandwidth Test:**
```bash
./test_bandwidth.sh
```

These scripts will:
- Automatically load your API key and function ID from `config.env`
- Validate that the configuration is set correctly
- Run the tests against your deployed NVCF function

If you haven't set up `config.env` yet, the scripts will display an error message with setup instructions.

### NCCL Tests

#### Local

Sample `curl` command for single node:

```bash
curl -X POST -H "Content-Type: application/json" -d '{"e":"128M", "g": 1, "cluster_type": "ncp-mlx5"}' localhost:8000/nccl-test
```

Sample `curl` command for multi node on NCP/Mellanox clusters (the default):

```bash
curl -X POST -H "Content-Type: application/json" -d '{"np": 2, "e":"128M", "g": 2, "cluster_type": "ncp-mlx5"}' localhost:8000/nccl-test
```

Sample `curl` command for multi node on AWS GB200/EFA clusters:

```bash
curl -X POST -H "Content-Type: application/json" -d '{"np": 2, "e":"128M", "g": 2, "cluster_type": "aws-gb200"}' localhost:8000/nccl-test
```

Sample `curl` command for multi node on AWS GB300/DRA RoCE clusters:

```bash
curl -X POST -H "Content-Type: application/json" -d '{"np": 8, "e":"16G", "npernode": 4, "cluster_type": "aws-gb300"}' localhost:8000/nccl-test
```

The `cluster_type` parameter controls which networking environment variables are set for MPI. Use `"ncp-mlx5"` for Mellanox RDMA, `"aws-gb200"` for AWS EFA, and `"aws-gb300"` for AWS network DRA with RoCE.

#### NVCF

```bash
curl --request POST \
  --url https://<function-id>.invocation.api.nvcf.nvidia.com/nccl-test \
  --header 'Authorization: Bearer <token>' \
  --header 'NVCF-POLL-SECONDS: 300' \
  --header 'Content-Type: application/json' \
  --data '{
  "np": 2, "g": 8, "cluster_type": "ncp-mlx5"
}'
```

#### NCCL Test Parameters

- `np` (int, default: 0): Number of MPI processes (0 runs locally without MPI)
- `b` (str, default: "8"): Minimum message size
- `e` (str, default: "128M"): Maximum message size
- `f` (str, default: "2"): Message size step factor
- `g` (str, default: "1"): Number of GPUs per thread
- `n` (str, default: "20"): Number of iterations
- `npernode` (int, default: 1): Number of MPI processes per node
- `mnnvl` (bool, default: false): Enable NCCL MNNVL mode
- `debug` (bool, default: false): Enable NCCL debug logging
- `cluster_type` (str, required): Network fabric type, `"ncp-mlx5"` for clusters with Mellanox/InfiniBand NICs, `"aws-gb200"` for AWS clusters with EFA, or `"aws-gb300"` for AWS GB300 clusters with DRA RoCE

### Bandwidth Tests

The bandwidth test endpoint uses [nvbandwidth](https://github.com/NVIDIA/nvbandwidth) to measure GPU bandwidth.

#### Local

Run all bandwidth tests:

```bash
curl -X POST -H "Content-Type: application/json" \
  -d '{"bufferSize": 512, "testSamples": 3, "json": true}' \
  localhost:8000/bandwidth-test
```

Run specific testcase:

```bash
curl -X POST -H "Content-Type: application/json" \
  -d '{"testcase": "device_to_device_memcpy_read_ce", "bufferSize": 256, "json": true}' \
  localhost:8000/bandwidth-test
```

Run tests by prefix:

```bash
curl -X POST -H "Content-Type: application/json" \
  -d '{"testcasePrefix": "host_to_device", "bufferSize": 128, "json": true}' \
  localhost:8000/bandwidth-test
```

#### NVCF

```bash
curl --request POST \
  --url https://<function-id>.invocation.api.nvcf.nvidia.com/bandwidth-test \
  --header 'Authorization: Bearer <token>' \
  --header 'NVCF-POLL-SECONDS: 300' \
  --header 'Content-Type: application/json' \
  --data '{
  "bufferSize": 512,
  "testcase": "device_to_device_memcpy_read_ce",
  "testSamples": 3,
  "json": true
}'
```

#### Available Parameters

- `bufferSize` (int, default: 512): Memory copy buffer size in MiB
- `testcase` (str, optional): Specific testcase to run (e.g., "device_to_device_memcpy_read_ce")
- `testcasePrefix` (str, optional): Run all tests matching prefix (e.g., "host_to_device", "multinode")
- `testSamples` (int, default: 3): Number of test iterations
- `useMean` (bool, default: false): Use mean instead of median for results
- `skipVerification` (bool, default: false): Skip data verification after copy
- `disableAffinity` (bool, default: false): Disable automatic CPU affinity control
- `json` (bool, default: true): Return results in JSON format
- `multinode` (bool, default: false): Run multinode tests (requires MPI)
- `np` (int, default: 0): Number of MPI processes for multinode tests

To list available testcases, you can run `nvbandwidth -l` in the container.

## Notes

- NCCL tests come from here: https://github.com/NVIDIA/nccl-tests
- Bandwidth tests come from here: https://github.com/NVIDIA/nvbandwidth
- Kubernetes 1.28 or newer is required due to Service using `apps.kubernetes.io/pod-index` label selector
- Kubernetes 1.32 or newer is required for the AWS GB300 DRA path because the sample renders `ResourceClaimTemplate` with `resource.k8s.io/v1`
- `clusterProfile: auto` requires Helm rendering against a live cluster. Offline rendering should use the default `ncp-gb200` profile or set `clusterProfile` explicitly.
- The `cluster_type` parameter controls which networking environment variables are set for MPI. Use `"aws-gb200"` for the EFA fabric provider, `"aws-gb300"` for AWS network DRA with RoCE, or `"ncp-mlx5"` for InfiniBand with `NCCL_IB_DISABLE=0`, `NCCL_NVLS_DISABLE=1`, `NCCL_IB_GID_INDEX=3`, and `NCCL_NET_GDR_LEVEL=PHB`.
