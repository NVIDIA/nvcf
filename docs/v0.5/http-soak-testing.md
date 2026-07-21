# Self-Managed NVCF HTTP Soak Test

This guide walks through running a sustained HTTP soak test against a
self-managed NVCF cluster using [k6](https://k6.io/). The test sends
a constant arrival-rate load to one or more deployed functions and
reports success rate, latency percentiles, and throughput over an
extended period (default 48 hours).

## Prerequisites

### Self-hosted CLI

You need a working `nvcf-cli` configured against your self-managed cluster.
If you have not set this up yet, follow the [self-hosted-cli](./cli.md) guide to
install the binary and the [cli-configuration](./cli.md) section to point it at your
gateway.

Verify the CLI can reach the cluster before continuing:

```bash
./nvcf-cli init
```

### Deploy the load test function

Use the **load_tester_supreme** container for soak testing. It is purpose-built
for high-throughput benchmarking and includes:

- **gRPC + HTTP + SSE** endpoints in a single image
- Tunable `repeats`, `delay`, and `size` fields to shape request/response
  profiles
- Built-in OpenTelemetry tracing

The source, build instructions, and registry push examples are in the
[nv-cloud-function-helpers](https://github.com/NVIDIA/nvcf/tree/main/examples/function-samples/load-tester-supreme)
repository. Build and push the image to whichever container registry your
cluster has credentials for:

```bash
git clone https://github.com/NVIDIA/nvcf/tree/main/examples.git
cd nv-cloud-function-helpers/examples/function_samples/load_tester_supreme

# Build (multi-arch)
docker buildx build --platform linux/amd64,linux/arm64 -t load_tester_supreme .

# Tag and push (replace with your registry -- ECR, NGC, Docker Hub, etc.)
docker tag load_tester_supreme <your-registry>/load_tester_supreme:latest
docker push <your-registry>/load_tester_supreme:latest
```

<Tip>
To check which registries your cluster recognises, run
`./nvcf-cli registry list`.

</Tip>

Then create the function and deploy it using the CLI. For HTTP soak testing
you can create multiple functions to simulate broader load:

```bash
# Create the function (HTTP)
./nvcf-cli function create \
  --name "load-tester-supreme" \
  --image "<your-registry>/load_tester_supreme:latest" \
  --inference-url "/echo" \
  --inference-port 8000 \
  --health-uri "/health" \
  --health-port 8000 \
  --health-timeout PT30S

# Deploy (adjust GPU type and instance type for your cluster)
./nvcf-cli function deploy create \
  --gpu L40S \
  --instance-type NCP.GPU.L40S_1x \
  --min-instances 1 \
  --max-instances 1

# Generate an API key for invocations (default expiry: 24h)
./nvcf-cli api-key generate

# For soak tests longer than 24 hours, set a longer expiry:
./nvcf-cli api-key generate --expires-in "7d"
```

Export the key so it can be passed to k6:

```bash
export API_KEY=<your-nvapi-key>
```

Repeat the `function create` and `function deploy create` steps to create
additional functions if you want to distribute load across multiple function
endpoints.

Once deployed, note the following -- you will need them for the k6 script:

- **Function ID** -- the UUID returned by `function create`
- **Function Version ID** -- the UUID of the specific deployed version
- **Invocation host** -- the `Host` header used for invocation routing
- **API key** -- from `./nvcf-cli api-key generate` (begins with `nvapi-`); export it as `$API_KEY`

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

The gateway uses `Host` header routing to direct traffic:

| Host prefix | Routes to |
| --- | --- |
| `api.$GATEWAY_ADDR` | NVCF management API (function CRUD) |
| `invocation.$GATEWAY_ADDR` | Invocation / inference service |
| `api-keys.$GATEWAY_ADDR` | API Keys service |

For the soak test, set:

- **BASE_URL** = `http://$GATEWAY_ADDR`
- **INVOKE_HOST** = `invocation.$GATEWAY_ADDR`

<Tip>
The CLI saves the function and version IDs automatically. Run
`./nvcf-cli status` to view them at any time.

</Tip>

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

## The k6 test script

Save the following script as `k6-nvcf-http-soak.js`. The script uses the
`constant-arrival-rate` executor to guarantee an exact request rate per
second regardless of response time, which is critical for soak testing where
you want a steady, predictable load.

<Note>
The latest version of this script is maintained in the
[nv-cloud-function-helpers](https://github.com/NVIDIA/nvcf/tree/main/examples)
repository.

</Note>

```javascript
/**
 * NVCF HTTP soak test script.
 *
 * Pass a FUNCTIONS JSON array with one entry per function you want to
 * load-test (one function per GPU node is typical). There is no limit
 * on the number of functions -- each iteration sends one request to
 * every function via http.batch().
 *
 * Total TPS = TPS_PER_FUNC × number of functions.
 *
 * Required env vars: BASE_URL, INVOKE_HOST, FUNCTIONS
 * Optional: API_KEY, TPS_PER_FUNC, PRE_VUS, MAX_VUS, REPEATS, DURATION
 *
 * Example:
 *   k6 run -e BASE_URL=http://$GATEWAY_ADDR \
 *          -e INVOKE_HOST=invocation.$GATEWAY_ADDR \
 *          -e 'FUNCTIONS=[{"funcId":"uuid1","verId":"uuid2"}]' \
 *          -e DURATION=48h \
 *          -e API_KEY=$API_KEY \
 *          k6-nvcf-http-soak.js
 */
import http from 'k6/http';
import { check } from 'k6';
import { Rate, Trend, Counter } from 'k6/metrics';

const invokeSuccess = new Rate('invoke_success');
const invokeLatency = new Trend('invoke_latency_ms');
const totalRequests = new Counter('total_requests');

// ---------- Required config ----------
const DURATION    = __ENV.DURATION    || '48h';
const BASE_URL    = __ENV.BASE_URL    || '';
const INVOKE_HOST = __ENV.INVOKE_HOST || '';

const FUNCTIONS = (() => {
  const raw = __ENV.FUNCTIONS || '';
  if (!raw) return [];
  try {
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr) || arr.length === 0) return [];
    return arr;
  } catch (e) {
    console.warn('FUNCTIONS JSON parse failed: ' + e.message);
    return [];
  }
})();

// ---------- Optional parameters ----------
const API_KEY      = __ENV.API_KEY      || '';
const REPEATS      = parseInt(__ENV.REPEATS      || '100', 10);
const TPS_PER_FUNC = parseInt(__ENV.TPS_PER_FUNC || '125', 10);
const PRE_VUS      = parseInt(__ENV.PRE_VUS      || '250', 10);
const MAX_VUS      = parseInt(__ENV.MAX_VUS      || '500', 10);

export const options = {
  scenarios: {
    invoke_all_functions: {
      executor: 'constant-arrival-rate',
      rate: TPS_PER_FUNC,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: PRE_VUS,
      maxVUs: MAX_VUS,
      exec: 'invoke_all_functions',
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<5000'],
    invoke_success:    ['rate>0.999'],
  },
};

export function invoke_all_functions() {
  const payload = JSON.stringify({ message: 'randomString', repeats: REPEATS });

  const requests = FUNCTIONS.map((f, i) => ({
    method: 'POST',
    url: `${BASE_URL}/echo`,
    body: payload,
    params: {
      headers: {
        'Host':                INVOKE_HOST,
        'Content-Type':        'application/json',
        'Authorization':       `Bearer ${API_KEY}`,
        'Function-Id':         f.funcId,
        'Function-Version-Id': f.verId,
        'Nvcf-Poll-Seconds':   '5',
      },
      tags: { func: `func_${i}` },
    },
  }));

  const responses = http.batch(requests);
  responses.forEach((res, i) => {
    totalRequests.add(1);
    invokeSuccess.add(res.status === 200);
    if (res.status === 200) invokeLatency.add(res.timings.duration);

    check(res, {
      [`func_${i} status 200`]:     (r) => r.status === 200,
      [`func_${i} fulfilled`]:      (r) => r.headers['Nvcf-Status'] === 'fulfilled',
      [`func_${i} body not empty`]: (r) => r.body && r.body.length > 0,
    });
  });
}
```

## Running the soak test

A typical soak test uses multiple functions at a sustained TPS for an extended
period. For example, multiple functions at 125 TPS each for 48 hours:

```bash
k6 run k6-nvcf-http-soak.js \
  -e BASE_URL=http://$GATEWAY_ADDR \
  -e INVOKE_HOST=invocation.$GATEWAY_ADDR \
  -e 'FUNCTIONS=[{"funcId":"<id-1>","verId":"<ver-1>"},{"funcId":"<id-2>","verId":"<ver-2>"},{"funcId":"<id-3>","verId":"<ver-3>"},{"funcId":"<id-4>","verId":"<ver-4>"}]' \
  -e DURATION=48h \
  -e TPS_PER_FUNC=125 \
  -e API_KEY=$API_KEY
```

<Tip>
Run the soak test inside `tmux` or `screen` so it survives SSH
disconnections:

```bash
tmux new -s soak
# run k6 command above
# Ctrl-B D to detach, tmux attach -t soak to re-attach
```

</Tip>

## Tune the load

### TPS per function

The `constant-arrival-rate` executor guarantees a fixed number of requests
per second. The total TPS is `TPS_PER_FUNC × number of functions`.

| TPS_PER_FUNC | Functions | Total TPS |
| --- | --- | --- |
| 1 | 1 | 1 (smoke test) |
| 25 | 4 | 100 (light load) |
| 125 | 4 | 500 (moderate soak) |

<Note>
**Default control plane sizing:** The default resource sizing that ships
with `nvcf-base` is designed to handle roughly 100 concurrent users. If you
need to test beyond that, you will need to scale the control plane components
first. Starting with 100 TPS total is a good baseline for validating a
default self-managed deployment.

</Note>

### VU allocation

Each VU is a virtual user that can hold one in-flight request. The executor
creates new VUs if existing ones are busy. If you see
`insufficient VUs, consider increasing maxVUs` warnings, increase
`PRE_VUS` and `MAX_VUS`:

```bash
-e PRE_VUS=500 -e MAX_VUS=1000
```

A good rule of thumb: `PRE_VUS ≈ TPS_PER_FUNC × avg_latency_seconds × 2`.

## Environment variables reference

| Variable | Description | Default |
| --- | --- | --- |
| `BASE_URL` | HTTP base URL of the gateway / load balancer (`http://$GATEWAY_ADDR`) | *(required)* |
| `INVOKE_HOST` | `Host` header for invocation routing (`invocation.$GATEWAY_ADDR`) | *(required)* |
| `FUNCTIONS` | JSON array of function objects: `[{"funcId":"…","verId":"…"}, ...]` | *(required)* |
| `DURATION` | Test duration (e.g. `30s`, `1h`, `48h`) | `48h` |
| `API_KEY` | `nvapi-*` bearer token (exported from `./nvcf-cli api-key generate`, see above) | *(required)* |
| `TPS_PER_FUNC` | Requests per second per function | `125` |
| `PRE_VUS` | Pre-allocated virtual users per function scenario | `250` |
| `MAX_VUS` | Maximum virtual users per function scenario | `500` |
| `REPEATS` | Number of times the echo endpoint repeats the payload string | `100` |

## Interpreting results

k6 prints a summary to stdout at the end of each run. Key metrics to monitor
during a soak test:

| Metric | What to look for |
| --- | --- |
| `invoke_success` | Should stay above 99.9 %. Drops indicate gateway or function errors. |
| `invoke_latency_ms` (p95) | Should stay below 5 000 ms. Rising latency over time can signal memory leaks, connection exhaustion, or pod restarts. |
| `http_req_duration` (p50 / p95 / p99) | Overall request round-trip time including network. Compare to `invoke_latency_ms` to isolate gateway overhead. |
| `total_requests` | Total requests completed. Divide by test duration to verify actual TPS. |
| `http_req_failed` | Percentage of non-2xx responses. Should be 0 % for a healthy cluster. |

To save results for offline analysis:

```bash
# Plain text log
k6 run k6-nvcf-http-soak.js ... 2>&1 | tee soak-run.log

# JSON output for Grafana / post-processing
k6 run --out json=soak-results.json k6-nvcf-http-soak.js ...
```

## Verifying the endpoint manually

Before running the soak test, verify the endpoint works with `curl`:

```bash
curl -X POST http://$GATEWAY_ADDR/echo \
  -H "Host: invocation.$GATEWAY_ADDR" \
  -H "Content-Type: application/json" \
  -H "Function-Id: <your-function-id>" \
  -H "Function-Version-Id: <your-function-version-id>" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"message": "hello", "repeats": 3}'
```

You should receive a `200 OK` response with the `Nvcf-Status: fulfilled`
header and the message repeated three times.
