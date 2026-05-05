(self-managed-http-load-test)=

# Self-Managed NVCF HTTP Load Test

## Prerequisites

### Self-hosted CLI

You need a working `nvcf-cli` configured against your self-managed cluster.
If you have not set this up yet, follow the {ref}`self-hosted-cli` guide to
install the binary and the {ref}`cli-configuration` section to point it at your
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
- Tunable `repeats`, `delay`, and `size` fields to shape request/response
  profiles
- Built-in OpenTelemetry tracing

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

:::{tip}
To check which registries your cluster recognises, run
`./nvcf-cli registry list`.
:::

Then create the function and deploy it using the CLI:

```bash
# Create the function (HTTP)
./nvcf-cli function create \
  --name "load-tester-supreme" \
  --image "nvcr.io/<your-org>/load_tester_supreme:latest" \
  --inference-url "/echo" \
  --inference-port 8000 \
  --health-uri "/health" \
  --health-port 8000 \
  --health-timeout PT30S

# Deploy (adjust GPU type and instance type for your cluster)
./nvcf-cli function deploy create \
  --gpu H100 \
  --instance-type NCP.GPU.H100_1x \
  --min-instances 1 \
  --max-instances 1 \
  --function-id <function id> \
  --version-id <version id>

# Generate an API key for invocations
./nvcf-cli api-key generate
```

Once deployed, note the following -- you will need them for the run script:

- **Function ID** -- the UUID returned by `function create`
- **Function Version ID** -- the UUID of the specific deployed version
- **API key** -- from `./nvcf-cli api-key generate` (begins with `nvapi-`)

#### Obtain the gateway address

Your gateway address is the external address of the Envoy Gateway deployed with
the control plane. To retrieve it:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway \
  -o jsonpath='{.status.addresses[0].value}')
echo "Gateway Address: $GATEWAY_ADDR"
```

On **AWS EKS** this is an ELB hostname (e.g.
`a1b2c3d4.us-east-1.elb.amazonaws.com`). For a **local** deployment (Kind,
k3d, Docker Desktop) it is typically `localhost` or `127.0.0.1`.

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
Create `run_http_self_managed_test.sh` in the `load-tests` directory:

```bash
#!/bin/bash

set -e

export GATEWAY_ADDR=<your-gateway-address>
export TOKEN=<your-nvapi-key>

export HTTP_SUPREME_NVCF_URL="http://${GATEWAY_ADDR}/v2/nvcf/pexec/functions/<your-function-id>"
export INVOKE_HOST="invocation.${GATEWAY_ADDR}"
export SENT_MESSAGE_SIZE=32
export RESPONSE_COUNT=1

k6 run functions/supreme_http_test.js \
  --vus 10 --duration 60s \
  -e TOKEN=${TOKEN} \
  -e HTTP_SUPREME_NVCF_URL=${HTTP_SUPREME_NVCF_URL} \
  -e INVOKE_HOST=${INVOKE_HOST} \
  -e SENT_MESSAGE_SIZE=${SENT_MESSAGE_SIZE} \
  -e RESPONSE_COUNT=${RESPONSE_COUNT}
```

Make it executable and run:

```bash
chmod +x run_http_self_managed_test.sh
./run_http_self_managed_test.sh
```

## Tune the load

### Virtual users (VUs)

Each VU simulates a single concurrent HTTP client, sending requests in a loop
and holding the connection open while waiting for a response (long-polling). The
number of VUs directly controls the concurrency hitting your endpoint.

| VUs | Simulates |
| --- | --- |
| 1--5 | Smoke test -- verify the endpoint works under minimal load |
| 10--50 | Light load -- a small team or service calling the function |
| 100--500 | Moderate load -- multiple services or a rollout with real traffic |
| 1000+ | Stress test -- find the breaking point or max throughput |

**Fixed VUs for a set duration (simplest approach):**

```bash
# 10 concurrent users for 1 minute
k6 run functions/supreme_http_test.js --vus 10 --duration 60s ...

# 200 concurrent users for 10 minutes
k6 run functions/supreme_http_test.js --vus 200 --duration 10m ...
```

**Ramping VUs with a config file (recommended for real load tests):**

Example `k6_rampup_config.json`:

```json
{
  "cloud": {
    "projectID": 3695020
  },
  "scenarios": {
    "rampup_scenario": {
      "executor": "ramping-vus",
      "startVUs": 0,
      "gracefulRampDown": "30s",
      "gracefulStop": "30s",
      "stages": [
        { "duration": "1m", "target": 5 },
        { "duration": "2m", "target": 5 },
        { "duration": "1m", "target": 25 },
        { "duration": "2m", "target": 25 },
        { "duration": "1m", "target": 100 },
        { "duration": "2m", "target": 100 },
        { "duration": "1m", "target": 500 },
        { "duration": "2m", "target": 500 },
        { "duration": "1m", "target": 1000 },
        { "duration": "2m", "target": 1000 },
        { "duration": "1m", "target": 0 }
      ]
    }
  }
}
```

```bash
k6 run functions/supreme_http_test.js \
  --config k6_rampup_config.json \
  -e TOKEN=${TOKEN} \
  -e HTTP_SUPREME_NVCF_URL=${HTTP_SUPREME_NVCF_URL} \
  -e INVOKE_HOST=${INVOKE_HOST} \
  -e SENT_MESSAGE_SIZE=${SENT_MESSAGE_SIZE} \
  -e RESPONSE_COUNT=${RESPONSE_COUNT}
```

## Environment variables reference

| Variable | Purpose |
| --- | --- |
| `TOKEN` | Your `nvapi-*` bearer token from `./nvcf-cli api-key generate` |
| `HTTP_SUPREME_NVCF_URL` | HTTP URL: `http://<gateway-addr>/v2/nvcf/pexec/functions/<function-id>` |
| `INVOKE_HOST` | INVOKE_HOST : `invocation.<gateway-addr>` |
| `SENT_MESSAGE_SIZE` | Size of the test payload in bytes |
| `RESPONSE_COUNT` | Number of responses the server should return |

## Verifying your endpoint manually

Then verify the endpoint works with `curl`:

```bash
curl -v -X POST \
  http://$GATEWAY_ADDR/v2/nvcf/pexec/functions/<your-function-id> \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Host: invocation.$GATEWAY_ADDR" \
  -H "Nvcf-Poll-Seconds: 5" \
  -d '{"message": "hello", "repeats": 1}'
```

You should receive a `200 OK` response with the `Nvcf-Status: fulfilled`
header.
