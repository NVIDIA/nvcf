# Multi-node Helm Chart

## Configuration Setup

Before running the test scripts, configure your self-hosted NVCF credentials:

1. Copy the sample configuration file:
```bash
cp config.env.sample config.env
```

2. Edit `config.env` and replace the placeholder values with your actual credentials:
   - `KEY`: A self-hosted NVCF invocation API key generated with `nvcf-cli api-key generate`
   - `FUNCTION_ID`: Your deployed function ID (single-node or multi-node)
   - `GATEWAY_ADDR`: The gateway address from `kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}'`

Example `config.env`:
```bash
KEY="<nvcf-cli-generated-api-key>"
FUNCTION_ID="ce460ed1-6f17-4bdc-ad6b-00a569fc780d"
GATEWAY_ADDR="a1b2c3d4.us-east-1.elb.amazonaws.com"
```

**Note:** The `config.env` file is gitignored to prevent accidentally committing sensitive API keys.

## Setup Override
1. Copy the sample AWS override file:
```bash
cp aws-gb200-override.yaml.sample aws-gb200-override.yaml
```

2. Edit `aws-gb200-override.yaml` to match your deployment requirements:
   - Update `nodesPerInstance` to match the number of nodes being tested
   - Modify the `image.repository` and `tag` to point to your container image


**Note:** This configuration is optimized for AWS GB200 instances with EFA networking and may need adjustment for other instance types.

## Deploy the Helm Chart
```bash
nvcf-cli function deploy create \
  --function-id <function-id> \
  --version-id <version-id> \
  --gpu <gpu-name> \
  --instance-type <instance-type> \
  --clusters <cluster> \
  --min-instances 1 \
  --max-instances 1 \
  --input-file aws-gb200-override.yaml
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
curl -X POST -H "Content-Type: application/json" -d '{"e":"128M", "g": 1}' localhost:8000/nccl-test
```

Sample `curl` command for multi node:

```bash
curl -X POST -H "Content-Type: application/json" -d '{"np": 2, "e":"128M", "g": 2}' localhost:8000/nccl-test
```

#### Self-hosted NVCF

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/nccl-test" \
  --header "Host: <function-id>.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${KEY}" \
  --header "NVCF-POLL-SECONDS: 300" \
  --header "Content-Type: application/json" \
  --data '{
  "np": 2, "g": 8
}'
```

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

#### Self-hosted NVCF

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/bandwidth-test" \
  --header "Host: <function-id>.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${KEY}" \
  --header "NVCF-POLL-SECONDS: 300" \
  --header "Content-Type: application/json" \
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

- NCCL tests come from here: https://github.com/NVIDIA/nccl-tests/tree/v2.17.2
- Bandwidth tests come from here: https://github.com/NVIDIA/nvbandwidth
- Kubernetes 1.28 or newer is required due to Service using
