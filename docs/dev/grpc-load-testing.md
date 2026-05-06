# Self-Managed NVCF gRPC Load Test

## Prerequisites

### Self-hosted CLI

You need a working `nvcf-cli` configured against your self-managed cluster.
If you have not set this up yet, follow the [self-hosted-cli](./cli) guide to
install the binary and the [cli-configuration](./cli) section to point it at your
gateway.

Verify the CLI can reach the cluster before continuing:

```bash
./nvcf-cli init
./nvcf-cli api-key generate
```

### Deploy the load test function

Use the **load_tester_supreme** container for load testing. It is purpose-built
for high-throughput benchmarking and includes:

- **gRPC + HTTP + SSE** endpoints in a single image
- **500 gRPC worker threads** by default (configurable via `WORKER_COUNT`),
  compared to 10 in the simpler `grpc_echo_sample`
- Tunable `repeats`, `delay`, and `size` fields to shape request/response
  profiles
- Built-in OpenTelemetry tracing

<Note>
The `grpc_echo_sample` from the same repository shares the
`Echo/EchoMessage` proto and will work for a quick smoke test, but its
10-thread pool will saturate under moderate concurrency. Always use
`load_tester_supreme` for real load tests.

</Note>

The source, build instructions, and registry push examples are in the
[nv-cloud-function-helpers](https://github.com/NVIDIA/nv-cloud-function-helpers/tree/main/examples/function_samples/load_tester_supreme)
repository. Build and push the image to whichever container registry your
cluster has credentials for:

```bash
git clone https://github.com/NVIDIA/nv-cloud-function-helpers.git
cd nv-cloud-function-helpers/examples/function_samples/load_tester_supreme

# Build
docker build --platform linux/amd64 -t load_tester_supreme .

# Tag and push (replace with your registry -- NGC, ECR, etc.)
docker tag load_tester_supreme nvcr.io/<your-org>/load_tester_supreme:latest
docker push nvcr.io/<your-org>/load_tester_supreme:latest
```

<Tip>
To check which registries your cluster recognises, run
`./nvcf-cli registry list`.

</Tip>

Then create the function and deploy it using the CLI:

```bash
# Create the function (gRPC -- set inference-url to a placeholder)
./nvcf-cli function create \
  --name "load-tester-supreme" \
  --image "nvcr.io/<your-org>/load_tester_supreme:latest" \
  --inference-url "/grpc" \
  --inference-port 8001 \
  --health-protocol GRPC \
  --health-uri "/" \
  --health-port 8001 \
  --health-timeout PT30S \

# Deploy (adjust GPU type and instance type for your cluster)
./nvcf-cli function deploy create \
  --gpu H100 \
  --instance-type NCP.GPU.H100_1x \
  --min-instances 1 \
  --max-instances 1 \
  --max-request-concurrency 1000 \
  --function-id <function id> \
  --version-id <version id>

# Generate an API key for invocations
./nvcf-cli api-key generate
```

Once deployed, note the following -- you will need them for the run script:

- **Function ID** -- the UUID returned by `function create`
- **Function Version ID** -- the UUID of the specific deployed version
- **gRPC endpoint** -- your gateway address on port `10081` (see below)
- **API key** -- the key from `api-key generate` (begins with `nvapi-`)

Your gateway address is the external address of the Envoy Gateway deployed with
the control plane. To retrieve it:

```bash
kubectl get gateway nvcf-gateway -n envoy-gateway \
  -o jsonpath='{.status.addresses[0].value}'
```

On **AWS EKS** this is an ELB hostname (e.g.
`a1b2c3d4.us-east-1.elb.amazonaws.com`). For a **local** deployment (Kind,
k3d, Docker Desktop) it is typically `localhost` or `127.0.0.1`.

<Tip>
The CLI saves the function and version IDs automatically. Run
`./nvcf-cli status` to view them at any time.

</Tip>

## Clone the load test scripts

```bash
git clone https://github.com/NVIDIA/nv-cloud-function-helpers.git
cd nv-cloud-function-helpers/examples/load-tests
```

## Install k6

Install [k6](https://grafana.com/docs/k6/latest/set-up/install-k6/) if you
don't have it:

```bash
# macOS
brew install k6
```

```bash
# Linux (Debian/Ubuntu)
sudo gpg -k
sudo gpg --no-default-keyring --keyring /usr/share/keyrings/k6-archive-keyring.gpg \
  --keyserver hkp://keyserver.ubuntu.com:80 \
  --recv-keys C5AD17C747E3415A3642D57D77C6C491D6AC1D69
echo "deb [signed-by=/usr/share/keyrings/k6-archive-keyring.gpg] https://dl.k6.io/deb stable main" \
  | sudo tee /etc/apt/sources.list.d/k6.list
sudo apt-get update && sudo apt-get install k6
```

## Create your run script

The `run*.sh` scripts are gitignored, so each user creates their own locally.
Create `run_grpc_self_managed_test.sh` in the `load-tests` directory:

```bash
#!/bin/bash

set -e

# From "nvcf-cli api-key show"
export TOKEN=<your-nvapi-token>

# Gateway address + gRPC port (see "kubectl get gateway" above)
# Examples:
#   AWS EKS:  a1b2c3d4.us-east-1.elb.amazonaws.com:10081
#   Local:    localhost:10081
export NVCF_GRPC_URL=<your-gateway-address>:10081
# From "nvcf-cli status" or the function create output
export GRPC_SUPREME_FUNCTION_ID=<your-function-id>
export GRPC_SUPREME_FUNCTION_VERSION_ID=<your-function-version-id>

export GRPC_PLAINTEXT=true

export SENT_MESSAGE_SIZE=2048
export RESPONSE_COUNT=1

k6 run functions/supreme_grpc_test.js \
  --vus 10 --duration 60s \
  --env TOKEN=${TOKEN} \
  --env NVCF_GRPC_URL=${NVCF_GRPC_URL} \
  --env GRPC_SUPREME_FUNCTION_ID=${GRPC_SUPREME_FUNCTION_ID} \
  --env GRPC_SUPREME_FUNCTION_VERSION_ID=${GRPC_SUPREME_FUNCTION_VERSION_ID} \
  --env GRPC_PLAINTEXT=${GRPC_PLAINTEXT} \
  --env SENT_MESSAGE_SIZE=${SENT_MESSAGE_SIZE} \
  --env RESPONSE_COUNT=${RESPONSE_COUNT}
```

Make it executable and run:

```bash
chmod +x run_grpc_self_managed_test.sh
./run_grpc_self_managed_test.sh
```

## Tune the load

### Virtual users (VUs)

Each VU simulates a single concurrent client holding an open gRPC connection and
sending requests in a loop. The number of VUs directly controls the concurrency
hitting your endpoint.

| VUs | Simulates |
| --- | --- |
| 1--5 | Smoke test -- verify the endpoint works under minimal load |
| 10--50 | Light load -- a small team or service calling the function |
| 100--500 | Moderate load -- multiple services or a rollout with real traffic |
| 1000+ | Stress test -- find the breaking point or max throughput |

<Note>
**Default control plane sizing:** The default resource sizing that ships
with the self-managed stack is designed to handle roughly 100 concurrent users. If you
need to test beyond that, you will need to scale the control plane components
first. Starting with `--vus 100` or the scratch config is a good baseline
for validating a default self-managed deployment.

</Note>

Start low and increase gradually. If you see rising error rates or latency, you
have found the saturation point.

**Fixed VUs for a set duration (simplest approach):**

```bash
# 10 concurrent users for 1 minute
k6 run functions/supreme_grpc_test.js --vus 10 --duration 60s ...

# 200 concurrent users for 10 minutes
k6 run functions/supreme_grpc_test.js --vus 200 --duration 10m ...
```

**Ramping VUs with a config file (recommended for real load tests):**

Config files let you gradually ramp users up, hold steady, and ramp down. This
avoids slamming the endpoint all at once and gives more realistic results.

The `k6_long_scaling_test_config.json` ramps to 100 VUs over 5 minutes, holds
for 15 minutes, then steps through higher concurrency levels:

```bash
k6 run functions/supreme_grpc_test.js \
  --config functions/test_configs/k6_long_scaling_test_config.json \
  --env TOKEN=${TOKEN} \
  --env NVCF_GRPC_URL=${NVCF_GRPC_URL} \
  --env GRPC_SUPREME_FUNCTION_ID=${GRPC_SUPREME_FUNCTION_ID} \
  --env GRPC_SUPREME_FUNCTION_VERSION_ID=${GRPC_SUPREME_FUNCTION_VERSION_ID} \
  --env GRPC_PLAINTEXT=${GRPC_PLAINTEXT} \
  --env SENT_MESSAGE_SIZE=${SENT_MESSAGE_SIZE} \
  --env RESPONSE_COUNT=${RESPONSE_COUNT}
```

The `k6_hammer_test_config.json` uses a `ramping-arrival-rate` executor that
ramps up to 100,000 requests/second -- use this only when you want to push the
endpoint to its limit:

```bash
k6 run functions/supreme_grpc_test.js \
  --config functions/test_configs/k6_hammer_test_config.json \
  --env TOKEN=${TOKEN} \
  --env NVCF_GRPC_URL=${NVCF_GRPC_URL} \
  --env GRPC_SUPREME_FUNCTION_ID=${GRPC_SUPREME_FUNCTION_ID} \
  --env GRPC_SUPREME_FUNCTION_VERSION_ID=${GRPC_SUPREME_FUNCTION_VERSION_ID} \
  --env GRPC_PLAINTEXT=${GRPC_PLAINTEXT} \
  --env SENT_MESSAGE_SIZE=${SENT_MESSAGE_SIZE} \
  --env RESPONSE_COUNT=${RESPONSE_COUNT}
```

### Other tuning parameters

| Parameter | Purpose |
| --- | --- |
| `SENT_MESSAGE_SIZE` | Payload size in bytes (e.g. `2048` for 2 KB) |
| `RESPONSE_COUNT` | Number of echoed responses per request |

## Environment variables reference

| Variable | Purpose |
| --- | --- |
| `TOKEN` | Your `nvapi-*` bearer token |
| `NVCF_GRPC_URL` | gRPC endpoint -- your gateway address with port `10081` (e.g. `a1b2c3d4.us-east-1.elb.amazonaws.com:10081` or `localhost:10081`) |
| `GRPC_SUPREME_FUNCTION_ID` | Function ID from NVCF |
| `GRPC_SUPREME_FUNCTION_VERSION_ID` | Function version ID (required for self-managed deployments) |
| `GRPC_PLAINTEXT` | Set to `true` for non-TLS connections |
| `SENT_MESSAGE_SIZE` | Size of the test payload in bytes |
| `RESPONSE_COUNT` | Number of responses the server should return |

## Verifying your endpoint manually

Before running a load test, you can verify the endpoint works with `grpcurl`:

```bash
grpcurl -plaintext \
  -H "function-id: <your-function-id>" \
  -H "function-version-id: <your-function-version-id>" \
  -H "authorization: Bearer <your-api-key>" \
  -d '{"message": "hello from grpc"}' \
  <your-gateway-address>:10081 Echo/EchoMessage
```
