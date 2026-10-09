# Pylon Operator end-to-end test on k3d

This directory contains an end-to-end test of Pylon Operator with the LLM
gateway stack on a local k3d cluster. `run.sh` installs the
`llm-gateway-stack` and `pylon-operator` charts from this checkout, publishes
an OpenAI-compatible sample backend through an `InferenceEndpoint`, and checks
status, the gateway API, failure paths, and restarts of the router, gateway,
operator and Pylon pods. The
[Inference Endpoints quickstart](../../../docs/dev/inference-endpoints-quickstart.md)
walks through the same setup step by step.

## Prerequisites

- A running k3d cluster. `run.sh` does not create or delete clusters. The
  default kube context is `k3d-pylon-op-e2e`, for example from
  `k3d cluster create pylon-op-e2e --agents 1` or `make cluster`.
- `kubectl`, `helm` 3.x, `curl`, and `openssl` or `/dev/urandom`.
- `bash` 3.2 or later. `run.sh` runs with the macOS default bash.
- These images imported into the cluster with `k3d image import -c <cluster>`.
  The test runs every pod with `imagePullPolicy: IfNotPresent`.

  | Image | Built from |
  | --- | --- |
  | `docker.io/src/libraries/rust/stargate/crates/stargate:latest` | `bazel run //src/libraries/rust/stargate/crates/stargate:image_load` |
  | `docker.io/src/invocation-plane-services/llm-api-gateway:latest` | `bazel run //src/invocation-plane-services/llm-api-gateway:image_load` |
  | `docker.io/src/compute-plane-services/pylon-operator:latest` | `bazel run //src/compute-plane-services/pylon-operator:image_load` |
  | `docker.io/src/libraries/rust/stargate/crates/pylon:latest` | `bazel run //src/libraries/rust/stargate/crates/pylon:image_load` |
  | `docker.io/library/openai-compatible-sample:e2e` | the OpenAI-compatible sample server from `examples/function-samples/openai-compatible-sample`; it serves `GET /health`, `GET /v1/models` with model `test-model`, and `POST /v1/chat/completions` on port 8000. Build and import it with `make sample-image` |

  For example:

  ```bash
  bazel run //src/compute-plane-services/pylon-operator:image_load
  k3d image import -c pylon-op-e2e src/compute-plane-services/pylon-operator:latest
  ```

  `make images import` builds and imports all five. The Rust images need
  `capnp` in `/usr/local/bin` or `/usr/bin`; see the quickstart.

## Run

From the repository root:

```bash
make -C tests/e2e/pylon-operator-k3d test
```

Or directly:

```bash
tests/e2e/pylon-operator-k3d/run.sh
```

To create the cluster, build and import the images and deploy in one go:

```bash
make -C tests/e2e/pylon-operator-k3d cluster images import deploy
```

| Target | What it does |
| --- | --- |
| `cluster` | Creates k3d cluster `pylon-op-e2e` with one agent when it does not exist, which also switches the current context to it, then checks that the current context is `E2E_KUBE_CONTEXT`. For an existing cluster it only checks the context. |
| `images` | Runs the four Bazel `image_load` targets and builds the sample image. `BAZEL_FLAGS` adds Bazel flags. `DOCKER_CONFIG_DIR=<absolute path of an empty directory>` adds `--repo_env=DOCKER_CONFIG=...`, so the base images are pulled anonymously when the local `nvcr.io` credential is stale. |
| `import` | Imports the five images with `k3d image import`. |
| `deploy` | Runs `run.sh` with `E2E_DEPLOY_ONLY=1`: setup and the steady-state checks, then prints the port-forward and `curl` commands and leaves everything installed. |
| `test` | Runs `run.sh`. |
| `test-cleanup` | Runs `run.sh` with `E2E_CLEANUP=1`. |
| `test-unit` | `bazel test` of the operator, gateway and Stargate subtrees, with `BAZEL_FLAGS` and `DOCKER_CONFIG_DIR`. |
| `test-charts` | `make test` of the `llm-gateway-stack`, `pylon-operator`, `llm-request-router` and `llm-api-gateway` charts, then `tools/ci/check-helm-charts`. |
| `all` | `images`, `import`, `deploy` and `test`. |
| `destroy` | Prints `k3d cluster delete pylon-op-e2e` and runs it only with `CONFIRM=1`. |
| `sample-image` | Builds the sample image and imports it. |
| `check` | `bash -n`, and `shellcheck` when installed. |

`K3D_CLUSTER` defaults to `E2E_KUBE_CONTEXT` without the `k3d-` prefix.

`run.sh` refuses to run when `kubectl config current-context` differs from
`E2E_KUBE_CONTEXT`, and it passes that context explicitly to every `kubectl`
and `helm` call.

The script prints one `PASS`, `FAIL` or `SKIP` line per assertion and a final
`RESULT: PASS` or `RESULT: FAIL` line. It exits 0 only when every assertion
passed. On a failure it prints the `InferenceEndpoint` YAML, pods, recent
Events and the last 100 log lines of the operator, the Pylon transport pods,
the router and the gateway. A failed setup step or a steady-state condition
that never converges stops the run, and the remaining groups are reported as
`SKIP`.

## What it does

Setup:

1. Generates a cluster token and a caller API key, and computes their SHA-256
   digests. A re-run reuses the token in the credential Secret and the key in
   the work directory; set `E2E_ROTATE_CREDENTIALS=1` for new ones.
2. Creates namespaces `llm-stack`, `pylon-operator` and `models`.
3. Creates Secret `e2e-cluster-credential` (key `cluster-token`) in
   `pylon-operator`.
4. Installs `llm-gateway-stack` in `llm-stack` with `clusterId: spark-e2e`, the
   token digest, one API key digest (id `e2e`), one router and one gateway
   replica.
5. Copies ConfigMap `llm-gateway-stack-ca` into `pylon-operator` and points the
   operator's `trustBundle.configMap` at it.
6. Installs `pylon-operator` in `pylon-operator`, watching `models`, with
   `router.grpcAddress` set to
   `http://llm-request-router.llm-stack.svc.cluster.local:50071`. The router
   serves plaintext gRPC on that port; the QUIC tunnel is verified against the
   stack CA.
7. Applies `manifests/sample-backend.yaml` and
   `manifests/inference-endpoint.yaml` in `models`.

Assertions, each polled for up to `E2E_TIMEOUT` seconds:

- Conditions `Ready=True/HealthProbeSucceeded`,
  `TransportReady=True/PylonConnected` and
  `Registered=True/RegisteredWithRouter`.
- `status.registration.routersConnected` is at least 1, `status.servers` has
  one entry, and `status.observedGeneration` matches the generation.
- `kubectl get inferenceendpoints` shows the columns `MODEL`, `GPU`, `READY`,
  `REGISTERED`, `SERVERS` and `AGE`.
- Through `kubectl port-forward` to the gateway, with TLS verified against the
  stack CA:
  - `GET /v1/models` without a key lists `test-model`.
  - `GET /v1/registry` without a key lists `test-model` as `Healthy`, with
    cluster `spark-e2e`, one registered server and one healthy server.
  - A streaming `POST /v1/chat/completions` with the API key returns 200 and
    Server-Sent Events `data:` lines ending in `data: [DONE]`.
  - The same request without a key returns 401.
- Backend scaled to zero: `Ready=False/NoReadyEndpoints` while `Registered`
  stays `True`. `GET /v1/registry` shows the model `Unhealthy` with one
  registered and no healthy server, `GET /v1/models` omits it, and a chat
  returns 503 from the router. Scaled back: `Ready`, `TransportReady` and
  `Registered` return to `True`.
- `spec.modelName` patched to `wrong-model`: `Ready=False/ModelNameMismatch`,
  the transport Deployment at 0 replicas, and `TransportReady` and
  `Registered` `False/ScaledToZero`. Patched back: the transport returns to one
  replica and every condition to `True`.
- Restart and failure cases, in `restart-cases.sh`, which `run.sh` sources.
  Each starts and ends in the steady state: every condition `True` and the
  registry `Healthy` with one registered and one healthy server. Assertion
  names start with the case ID; `stack` checks read the gateway API,
  `operator` checks read the `InferenceEndpoint`.
  - C7a, backend process killed with `kill 1`: the router logs a failed health
    probe for the endpoint's server id, the operator records a Warning Event
    for `Ready=False`, the container restarts in place, and `Registered`
    keeps its `lastTransitionTime`.
  - C1, router pod deleted: `Registered=False/RouterUnreachable` and
    `TransportReady=False/TunnelNotConnected`, and `GET /v1/registry` returns
    502 until a new router pod is Ready. The registry then shows the model
    healthy within 15 seconds, with the same server id. The log reports the
    time from the delete to the new router pod being Ready.
  - C2, gateway pod deleted: the new pod serves the same registry, and the
    `InferenceEndpoint` has no transition and no new Events.
  - C3, operator scaled to 0: a stream started before the outage stays open
    and ends with `data: [DONE]`, a new chat returns 200, the registry stays
    healthy, and the Pylon pod keeps its UID. C3b applies a second
    `InferenceEndpoint`, `test-model-b`, while the operator is down. It gets
    no status and no transport until the operator returns, and is then
    reconciled and deleted.
  - C5, Pylon pod deleted: `status.servers` lists only the new pod's server id,
    and the registry shows one registered server, not two.
- `InferenceEndpoint` deleted: the transport Deployment is garbage-collected
  and `GET /v1/models` no longer lists `test-model`.

At the end the script applies the `InferenceEndpoint` again, so the stack is
left in its steady state for inspection. With `E2E_CLEANUP=1` it instead
uninstalls both releases, deletes the three namespaces and deletes the
`InferenceEndpoint` CRD, which the chart keeps on uninstall.

## Settings

| Variable | Default | Description |
| --- | --- | --- |
| `E2E_KUBE_CONTEXT` | `k3d-pylon-op-e2e` | Kube context to use. The current context must match. |
| `E2E_CLEANUP` | `0` | `1` removes the releases, namespaces and CRD at the end. |
| `E2E_DEPLOY_ONLY` | `0` | `1` stops after setup and the steady-state checks, prints the port-forward and `curl` commands, and leaves everything installed. It skips the gateway checks and the failure paths, and cannot be combined with `E2E_CLEANUP=1`. |
| `E2E_TIMEOUT` | `180` | Seconds to poll each assertion. |
| `E2E_HELM_TIMEOUT` | `5m` | `helm --timeout` for each install. |
| `E2E_ROTATE_CREDENTIALS` | `0` | `1` generates a new cluster token and API key even when earlier ones exist. |
| `E2E_WORK_DIR` | `${TMPDIR:-/tmp}/pylon-operator-k3d-e2e` | Generated values files, the CA, the API key (mode 600) and response bodies. |
| `E2E_GATEWAY_LOCAL_PORT` | `18443` | Local port of the gateway port-forward. |
| `E2E_CLUSTER_ID` | `spark-e2e` | `clusterId` of both charts. |
| `E2E_IMAGE_REGISTRY` | `docker.io` | Registry of the four built images. |
| `E2E_IMAGE_TAG` | `latest` | Tag of the four built images. |
| `E2E_ROUTER_REPOSITORY`, `E2E_GATEWAY_REPOSITORY`, `E2E_OPERATOR_REPOSITORY`, `E2E_PYLON_REPOSITORY` | the Bazel image names | Image repositories. |
| `E2E_ROUTER_GRPC_ADDRESS` | `http://llm-request-router.llm-stack.svc.cluster.local:50071` | `router.grpcAddress` of the operator. |
| `E2E_DEV_INSECURE_TRANSPORT` | `0` | `1` sets the operator's `devInsecureTransport`, which skips QUIC certificate verification. |
| `E2E_STACK_NAMESPACE`, `E2E_OPERATOR_NAMESPACE`, `E2E_MODELS_NAMESPACE` | `llm-stack`, `pylon-operator`, `models` | Namespaces. |

## Inspect the cluster afterwards

```bash
kubectl --context k3d-pylon-op-e2e -n models get inferenceendpoints
kubectl --context k3d-pylon-op-e2e -n models describe inferenceendpoint test-model
kubectl --context k3d-pylon-op-e2e -n models logs deployment/pylon-test-model
kubectl --context k3d-pylon-op-e2e -n pylon-operator logs deployment/pylon-operator
kubectl --context k3d-pylon-op-e2e -n llm-stack logs deployment/llm-request-router
kubectl --context k3d-pylon-op-e2e -n llm-stack port-forward svc/llm-api-gateway 18443:8080
```

With the port-forward running, call the gateway with the key and CA from the
work directory:

```bash
WORK="${TMPDIR:-/tmp}/pylon-operator-k3d-e2e"
curl --cacert "${WORK}/ca.crt" https://127.0.0.1:18443/v1/registry
curl --cacert "${WORK}/ca.crt" https://127.0.0.1:18443/v1/chat/completions \
  -H "Authorization: Bearer $(cat "${WORK}/api-key")" \
  -H 'Content-Type: application/json' \
  -d '{"model": "test-model", "messages": [{"role": "user", "content": "hi"}]}'
```
