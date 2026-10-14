# Stargate development deployment (example)

This directory is an example of a multi-region Stargate development
environment on AWS. It runs Stargate against mock inference backends
(MockDynamo with a Pylon sidecar) so routing policies can be benchmarked
across regions without GPUs. To run a smaller mock topology on one Linux
workstation without AWS, see
[Run locally on a workstation](#run-locally-on-a-workstation).

It is not a supported production deployment. Every AWS account ID, ARN,
hostname, image reference, and network range checked in here is a
placeholder. A deployment supplies its own values in a protected file outside
the repository.

## What it deploys

For each AWS region in `environments/`:

- One hub EKS cluster with Stargate, the backend router, the dev-only auth
  fixture, and a Grafana Alloy metrics collector.
- Two MockDC EKS clusters. Each runs two backends, and each backend is one
  MockDynamo container with a Pylon sidecar.

The AWS example environments define five regions (`us-west-2`, `us-east-1`,
`eu-west-1`, `ap-northeast-1`, `ap-southeast-2`) with 20 backends in total.
`environments/` also holds two local k3d regions, `local-a` and `local-b`,
described in [Run locally on a workstation](#run-locally-on-a-workstation).
Each Pylon registers with its own region's Stargate. A hub republishes the
peer hub URLs in `router.remoteWatchUrls` to its Pylons, and each Pylon also
registers with those peer hubs. In a connected topology every hub sees all
20 backends.

Connected regions share one credential bundle, one `routingKey`, and one
router CA. Each hub authenticates every registering Pylon with its own dev
auth fixture, and each Pylon trusts only the CA in its local
`stargate-router-ca` Secret.

## Prerequisites

The prerequisites, placeholders, and deploy steps below are for AWS. For the
local workflow, see [Run locally on a workstation](#run-locally-on-a-workstation).

Tools on `PATH`: the AWS CLI, `kubectl`, `helm` with the `helm-diff` plugin,
`helmfile`, and `python3` with PyYAML.

AWS resources you provide:

- An AWS account. Export its ID as `STARGATE_DEV_AWS_ACCOUNT_ID`.
- Per region, three EKS clusters whose names and kube contexts match
  `environments/<region>.yaml`. Deployment preflight checks both and
  requires cluster-admin access. A values file cannot override `clusters`,
  `region`, `namespace`, `modelName`, `routingKey`, or `platform`.
- At least two schedulable nodes in each MockDC cluster. The cluster's two
  backends use required Pod anti-affinity on `kubernetes.io/hostname`, so a
  single-node cluster leaves one backend Pending. The MockDC phase preflight
  checks the count.
- NetworkPolicy enforcement on each hub cluster, for example the Amazon VPC
  CNI with `enableNetworkPolicy=true`. A NetworkPolicy limits the dev auth
  fixture's gRPC port to the router Pods. Without enforcement, any Pod in the
  hub cluster can reach it, and only the service token protects it.
- The AWS Load Balancer Controller on each hub cluster. The registration
  endpoint is a network load balancer with TLS on port 50071 and UDP on 50072.
- DNS records for `router.hostname` and `*.<router.hostname>` that point at
  that load balancer. Stargate advertises each backend-router Pod as
  `<pod name>.<router.hostname>`, so the ACM certificate and the router
  certificate below need both names as SANs.
- The public NAT egress IPs of the region's MockDC clusters, as /32 entries
  in `router.loadBalancerSourceRanges`. When regions are connected through
  `router.remoteWatchUrls` (`https://<peer router.hostname>:50071`), each hub's
  ranges must include the MockDC NAT egress IPs of every connected region,
  because peer-region Pylons register over gRPC on 50071 and tunnel over
  QUIC on 50072.
- A container registry with Stargate, dev auth, MockDynamo, and Pylon images,
  pinned by digest. Bazel builds them from
  `//src/libraries/rust/stargate/crates/stargate:image` (which includes the
  backend router), `//src/libraries/rust/stargate/crates/stargate:dev_auth_image`,
  `//src/libraries/rust/stargate/crates/mock-dynamo:image`, and
  `//src/libraries/rust/stargate/crates/pylon:image`.
- Optional observability: an Amazon Managed Service for Prometheus workspace,
  an IAM role for the Alloy writer, and an IAM role for Grafana.
  `scripts/observability.py` can create them.

Secrets you create before the first apply (preflight checks for them):

- In the hub cluster, in the `namespace` from the environment file
  (`stargate-dev`), the `stargate-router-tls` Secret with `tls.crt`,
  `tls.key`, and `ca.crt`. `ca.crt` is the CA that issued the
  router certificate. Connected regions issue their router certificates from
  the same CA.
- In each MockDC cluster, in the same namespace, the `stargate-router-ca`
  Secret with that CA as `ca.crt`.

Create the namespace in the hub and in each MockDC cluster first with
`kubectl --context <context> create namespace stargate-dev`.

## Placeholders

Replace these in your protected values file. Do not commit real values.

| Setting | Example placeholder |
|---|---|
| `router.hostname` | `router.usw2.stargate-dev.example.invalid` |
| `router.acmCertificateArn` | `arn:aws:acm:us-west-2:000000000000:certificate/00000000-...` |
| `router.loadBalancerSourceRanges` | `192.0.2.1/32` (documentation range) |
| `router.remoteWatchUrls` | `[]` (one entry per peer hub) |
| `observability.amp.workspaceId` | `ws-00000000-0000-0000-0000-000000000000` |
| `images.*` in `values/versions.yaml` | `example.invalid/<image>` with an all-zero digest |

The observability phase derives its IAM role ARNs from
`STARGATE_DEV_AWS_ACCOUNT_ID` and the region:
`stargate-dev-<region>-amp-writer` for Alloy and `stargate-dev-<region>-grafana`
for Grafana. `scripts/observability.py` creates both roles, and the phase
preflight checks that they and an active AMP workspace exist.

`deploymentReady` stays `false` in the checked-in files, and the apply wrapper
refuses to deploy until the protected values set it to `true`.

## Deploy

Run from this directory with credentials for your account. Apply the phases
in order: `stargate`, then `mockdc`, then optionally `observability`.

1. Create the protected credential bundle once. Every connected region uses
   the same bundle:

   ```sh
   python3 scripts/deploy.py init --credentials /secure/path/credentials.json
   ```

2. Write a protected values file for the region with your hostnames, ARNs,
   image digests, and `deploymentReady: true`.

3. Apply the Stargate and MockDC phases:

   ```sh
   export STARGATE_DEV_AWS_ACCOUNT_ID=<your account ID>
   python3 scripts/deploy.py apply --region us-west-2 --phase stargate --credentials /secure/path/credentials.json --values /secure/path/us-west-2/values.yaml
   python3 scripts/deploy.py apply --region us-west-2 --phase mockdc --credentials /secure/path/credentials.json --values /secure/path/us-west-2/values.yaml
   ```

4. Optionally provision observability. `scripts/observability.py` creates or
   updates the AMP workspace and IAM roles, then prints the
   `observability.amp.workspaceId` value to merge into the protected values
   file. Then apply the `observability` phase:

   ```sh
   python3 scripts/observability.py --region us-west-2 --values /secure/path/us-west-2/values.yaml
   python3 scripts/deploy.py apply --region us-west-2 --phase observability --values /secure/path/us-west-2/values.yaml
   ```

   Reach Grafana with
   `kubectl --context <hub context> -n stargate-dev-observability port-forward svc/stargate-dev-grafana 3000:80`.
   The admin password is in the `admin-password` key of the
   `stargate-dev-grafana` Secret in the same namespace.

## Teardown

Remove the releases in reverse phase order, with the same environment as the
apply:

```sh
export STARGATE_DEV_AWS_ACCOUNT_ID=<your account ID>
export STARGATE_DEV_VALUES_FILE=/secure/path/us-west-2/values.yaml
export STARGATE_DEV_CREDENTIALS_FILE=/secure/path/credentials.json
helmfile --environment us-west-2 --selector phase=observability destroy
helmfile --environment us-west-2 --selector phase=mockdc destroy
helmfile --environment us-west-2 --selector phase=stargate destroy
```

Destroying the `stargate` phase deletes the backend-router Service and its
network load balancer. Confirm the load balancer is gone before you delete
the hub EKS cluster; deleting the cluster first can leave it behind.

`scripts/observability.py` resources stay until you delete them. Each region
has an AMP workspace with the alias `stargate-dev` and two IAM roles, each
with one inline policy named after the role:

```sh
aws amp list-workspaces --region us-west-2 --alias stargate-dev
aws amp delete-workspace --region us-west-2 --workspace-id <workspace ID>
for role in stargate-dev-us-west-2-amp-writer stargate-dev-us-west-2-grafana; do
  aws iam delete-role-policy --role-name "$role" --policy-name "$role"
  aws iam delete-role --role-name "$role"
done
```

## Rust benchmark workflow

Verification and benchmark orchestration use the `stargate-dev-bench` CLI in
`benchmark/`. Spark is the load generator. It runs in an existing Pod in the
selected region's hub cluster, and the controller drives it through `kubectl`.

Run these commands from the repository root on Linux. `benchmark/rust-toolchain.toml`
pins Rust 1.97.0, but rustup selects a toolchain from the current directory,
not from `--manifest-path`. From the repository root, the default toolchain
builds the CLI and must be 1.97.0 or newer; run `cargo build` from `benchmark/`
to use the pinned toolchain instead. Keep a copy of the executable for the
campaign. Resume requires that exact executable as well as the same inputs and
deployment controls. Do not rebuild over the retained copy while a campaign
uses it.

```sh
cargo build --locked --release --manifest-path deploy/stacks/stargate-dev/benchmark/Cargo.toml
cp deploy/stacks/stargate-dev/benchmark/target/release/stargate-dev-bench /secure/path/stargate-dev-bench
export STARGATE_BENCH=/secure/path/stargate-dev-bench
```

List or preview the reduced suite without cluster access or traffic.
`loadtest/reduced.yaml` defines the `reduced` suite and its single-scenario
parts, `reduced-session-affinity`, `reduced-mixed-sessions`, and
`reduced-capacity`. Without `--suite-file`, the CLI uses the embedded
`loadtest/suite.yaml`, which defines `smoke` and `canonical`.
`list` prints each suite's scenarios and minimum measured time.

```sh
"$STARGATE_BENCH" --suite-file deploy/stacks/stargate-dev/loadtest/reduced.yaml list
"$STARGATE_BENCH" --suite-file deploy/stacks/stargate-dev/loadtest/reduced.yaml plan --suite reduced
```

Verify the west ingress hub against the connected 20-backend topology:

```sh
"$STARGATE_BENCH" verify --region us-west-2 --peer-region us-east-1 --peer-region eu-west-1 --peer-region ap-northeast-1 --peer-region ap-southeast-2
```

Run a campaign through an existing Pod with a container named `spark`:

```sh
"$STARGATE_BENCH" --suite-file deploy/stacks/stargate-dev/loadtest/reduced.yaml run --suite reduced --region us-west-2 --peer-region us-east-1 --peer-region eu-west-1 --peer-region ap-northeast-1 --peer-region ap-southeast-2 --spark-pod SPARK_POD --expected-config deploy/stacks/stargate-dev/loadtest/queue-bounds-none-max-queued-4.json --output RESULTS
```

The Pod must run in the `namespace` from the environment file on the
`--region` hub cluster. Spark sends traffic to the in-cluster
`http://llm-request-router:8000/v1` Service. The controller freezes the Spark
container's image, runtime image ID, Spark version, resources, Pod
specification hash, and open-file limit, and a change to any of them stops the
campaign. It raises the owned Spark process's soft open-file limit to the
container's hard limit and rejects worker counts without descriptor headroom.
The Spark image needs Bash, coreutils, `setsid`, `flock`, `grep`, and `tar`.
Stargate does not validate client API keys. The controller sets a fixed
placeholder `OPENAI_API_KEY` for the Spark process.

`--expected-config` checks the policy in every selected region before traffic;
it does not apply configuration. The stack deploys
`loadtest/queue-bounds-none-max-queued-4.json` as the router policy by default,
so the command above passes against a default deployment. To benchmark a
different policy, set the complete JSON object as `router.loadBalancerConfig`
in each region's protected deployment values and pass the same object to
`--expected-config`.

Each MockDC backend runs MockDynamo's batched engine with `numGpuWorkers`
workers, 1,000,000 KV tokens per worker, and the engine's default step costs.
Pylon advertises `numGpuWorkers` x 25 as its engine concurrency, calibrates at
concurrency 25 on startup, and runs no periodic canaries. Pylon's engine stats
stream is off, so it uses its fallback statistics.

Runs reset caches and restart participating backends and routers when the plan
requires clean caches. Readiness and control checks include peer regions;
benchmark ingress remains the selected `--region`. Include every connected
region as a peer so readiness checks use the complete backend count.

`--algorithm` selects the arms. The default, `both`, runs wait-and-widen and
power-of-n in alternating order. `pulsar-wait-and-widen` runs alone and needs a
suite file that defines it, such as `loadtest/session-ramp.yaml`. The summary
compares each algorithm with wait-and-widen in the same pair, so Pulsar rows
from a Pulsar-only campaign are listed as unpaired.

### Acceptance and recovery

```text
suite + controls -> immutable campaign + workloads -> Spark attempts
                                                        |
                                                   validation
                                                   /        \
                                             rejected      accepted receipt
                                             retained            |
                                                          offline rendering
```

An attempt is accepted only after report counts, cold-capacity cache observations,
checksums, Pod identity, deployment configuration, and cache evidence pass their
checks. Capacity arms use disjoint prompt IDs. Failed attempts remain available
for inspection and are not included in accepted-result summaries.

Repeat the original `run` command with `--resume` to continue a campaign.
The controller checks local input identity, reconciles owned unfinished work,
then checks current deployment controls before starting more traffic. A missing
launch acknowledgement never causes a second launch. Unconfirmed cancellation
blocks further traffic.

Reconcile a stopped controller's campaign explicitly, or render accepted
reports without a cluster or new traffic:

```sh
"$STARGATE_BENCH" reconcile --output RESULTS
"$STARGATE_BENCH" render --output RESULTS
```

Keep the output directory at its original path. `campaign.json` freezes inputs;
`attempts/` contains raw reports and evidence; `accepted/` contains completion
receipts. `summary.json` and `summary.txt` are derived outputs. Rendering
verifies recorded hashes and cannot turn an invalid attempt into an accepted
result.

Summaries report successful throughput (successful requests per second, not
goodput against an SLO), recorded failures, retries, cache hits, TTFT p99,
end-to-end p99, and the median and observed range of paired ratios against
wait-and-widen. Timed runs exclude window-end cancellations from recorded
request counts. Rate schedules divide by the scheduled window and exclude the
drain. Latency quantiles describe successful requests and use Spark's histogram
bucket lower bounds. Observed ranges are not confidence intervals. Production
recommendations remain pending real-engine QA.

## Run locally on a workstation

The `local-a` and `local-b` environments run the stack as a mock multi-cluster
topology on one Linux workstation. Every cluster is a k3d cluster in Docker.
No AWS account and no GPUs are needed. Use it to check deployment,
registration, cross-region membership, and the benchmark controller before
using AWS.

### Local topology

- Each region has a hub cluster `stargate-dev-<region>-hub` and two MockDC
  clusters, `stargate-dev-<region>-mockdc-1` and
  `stargate-dev-<region>-mockdc-2`. Kube contexts are `k3d-<cluster name>`,
  for example `k3d-stargate-dev-local-a-hub`.
- A hub cluster is one server node. Each MockDC cluster has two agent nodes,
  so the backend anti-affinity rule can schedule both backends. Backends use
  small `numGpuWorkers` values.
- All clusters join the Docker network `stargate-dev-local`. Pylons reach the
  hub registration Services directly on that network. The two hubs are
  connected, so each hub sees all 8 backends.
- The k3d registry `k3d-stargate-dev-registry` serves images to every
  cluster. Charts keep `repository@digest` image references.
- The environment files set `platform: local`. Without it, the platform is
  AWS. `deploy.py` rejects a values file that sets `platform`. In local mode
  the registration Service uses k3s ServiceLB instead of an AWS network load
  balancer, so it answers on the hub node's address on the shared network.
  That address is `router.address`. There is no ACM certificate, DNS record,
  or source range list, and `STARGATE_DEV_AWS_ACCOUNT_ID` is not required.
- On AWS the load balancer terminates TLS for registration gRPC on port
  50071. The router serves that port only as plaintext gRPC, so locally Pylons
  and peer hubs dial `http://<router.address>:50071`. Reverse QUIC on port
  50072 still uses TLS from the `stargate-router-tls` Secret. Its certificate
  names `router.hostname` (`router.<region>.stargate-dev.test`) and
  `*.router.hostname`. That name is only a TLS identity, so no DNS is
  needed.

### Local prerequisites

- Linux with Docker. The benchmark CLI needs Linux process APIs.
- Tools on `PATH`: `docker`, `k3d`, `kubectl`, `helm` with the `helm-diff`
  plugin, `helmfile`, `bazel` or `bazelisk`, `openssl`, `python3` with PyYAML,
  and Rust 1.97.0 for the benchmark CLI.
- The Cap'n Proto compiler `capnp` 0.5.2 or newer, for example from the
  Ubuntu `capnproto` package. A Bazel build script runs it with Bazel's fixed
  action `PATH`, so install it in `/usr/bin` or `/usr/local/bin`, or pass
  `--action_env=PATH=...` in a `user.bazelrc`.
- Resources for six clusters, the registry, and Bazel outputs. Plan for 8 CPU
  cores, 16 GiB of memory, and 20 GiB of free disk. The idle stack uses about
  5 GiB of memory, and the Bazel output tree about 5 GiB of disk.
- If Pods fail with `too many open files`, raise the host's
  `fs.inotify.max_user_instances` and `fs.inotify.max_user_watches`.

### Local deploy

Run from this directory:

```sh
python3 scripts/local.py up
python3 scripts/local.py images
```

`up` creates the Docker network, the registry, and the six clusters, and adds
their contexts to your kubeconfig without switching the current context. For
a cluster that already exists, `up` adds its context again. The
registry and each API server listen only on `127.0.0.1`. `up` also creates a
local CA and one router certificate per region with `openssl`, the
`stargate-router-tls` Secret in each hub, and the `stargate-router-ca` Secret
in each MockDC cluster, all in the `stargate-dev` namespace. It also creates
the shared credential bundle. `images` builds the Stargate, dev auth,
MockDynamo, and Pylon images with Bazel for the host's Linux platform, pushes
them to the registry, and writes one values file per region. Each file holds
the image digests, `deploymentReady: true`, the hub's `router.address`, and
the peer hub's URL in `router.remoteWatchUrls`.

Local state lives in `.local/` in this directory, which Git ignores. It holds
the CA, the router certificates, the credential bundle
(`.local/credentials.json`), and the values files
(`.local/values-local-a.yaml` and `.local/values-local-b.yaml`).

Apply both hubs first, then both MockDC phases, so every Pylon can reach its
peer hub:

```sh
python3 scripts/deploy.py apply --region local-a --phase stargate --credentials .local/credentials.json --values .local/values-local-a.yaml
python3 scripts/deploy.py apply --region local-b --phase stargate --credentials .local/credentials.json --values .local/values-local-b.yaml
python3 scripts/deploy.py apply --region local-a --phase mockdc --credentials .local/credentials.json --values .local/values-local-a.yaml
python3 scripts/deploy.py apply --region local-b --phase mockdc --credentials .local/credentials.json --values .local/values-local-b.yaml
```

In local mode, `deploy.py` skips the AWS CLI, EKS, and account checks. It
still checks that each kube context exists, that the router Secrets are
present, and, before the MockDC phase, that each MockDC cluster has two
schedulable nodes and that the registration Service is exposed on
`router.address`. The `observability` phase is not supported
locally, and `deploy.py` exits with an error if you request it.

`images` reads each hub's address from its single node's InternalIP through
the hub kube context. If a hub cluster is recreated or its node address
changes, run `images` again to refresh `router.address` and
`router.remoteWatchUrls`, then apply every phase again.

Each Pylon calibrates its backend for about a minute after the MockDC phase
before it registers. Until then `verify` fails with `membership is not ready`;
run it again.

### Local verify and traffic checks

Run from the repository root:

```sh
cargo +1.97.0 build --locked --release --manifest-path deploy/stacks/stargate-dev/benchmark/Cargo.toml
export STARGATE_BENCH=deploy/stacks/stargate-dev/benchmark/target/release/stargate-dev-bench
"$STARGATE_BENCH" verify --region local-a --peer-region local-b
```

`verify` checks the `local-a` hub's Stargate Deployments, registration
Service, and MockDC Pods, and expects that hub's routers to report all 8
backends across both regions. Run it with `--region local-b --peer-region
local-a` to check the other hub.

To send single requests without Spark, start a curl Pod in the `local-a` hub
and send streaming chat completions. The router requires the request ID,
model, input token, and routing key headers, and the default policy also
requires a cache affinity key. Pylon's tunnel accepts only streaming chat
requests (`"stream": true`). The `x-stargate-cluster-id` response header names
the backend, so repeated requests with different affinity keys show both
regions' backends.

```sh
kubectl --context k3d-stargate-dev-local-a-hub -n stargate-dev run curl --image=curlimages/curl:8.11.1 --restart=Never --command -- sleep 3600
kubectl --context k3d-stargate-dev-local-a-hub -n stargate-dev wait --for=condition=Ready pod/curl --timeout=120s
kubectl --context k3d-stargate-dev-local-a-hub -n stargate-dev exec curl -- curl -sS -D - http://llm-request-router:8000/v1/chat/completions -H 'content-type: application/json' -H 'x-request-id: smoke-1' -H 'x-model: stargate-dev-model' -H 'x-input-tokens: 8' -H 'x-routing-key: stargate-dev' -H 'x-cache-affinity-key: smoke-1' -d '{"model":"stargate-dev-model","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hello"}]}'
kubectl --context k3d-stargate-dev-local-a-hub -n stargate-dev delete pod curl
```

Benchmark campaigns need a Spark image, which this repository does not
provide, so campaigns are not validated on the local topology.

### Local teardown

```sh
python3 scripts/local.py down
```

`down` deletes the six clusters, the registry, and the Docker network by
their exact names, including any that `up` found and reused instead of
creating. `up` prints a line for each one it reuses. `down` keeps `.local/`;
delete that directory yourself to start again with a new CA and credentials.
The image tags that `images` adds to the local Docker image store also
remain.

### Local limitations

- No observability phase, AMP workspace, or Grafana.
- Mock backends only. MockDynamo and Pylon stand in for inference engines.
- All clusters share one host's CPU, memory, and network. Latency and
  throughput numbers are not comparable with AWS results. Use local runs to
  check behavior, not performance.

## Layout

- `environments/`: one file per region with cluster names and per-backend
  MockDynamo settings. `local-a.yaml` and `local-b.yaml` are the local k3d
  regions.
- `values/versions.yaml`: image and chart version pins.
- `helmfile.yaml.gotmpl`: releases and install order.
- `charts/`: the dev auth fixture and MockDC charts.
- `scripts/`: deployment and observability wrappers, and `local.py` for the
  local k3d topology.
- `benchmark/`: the `stargate-dev-bench` controller for verification and
  benchmark campaigns.
- `dashboards/`, `loadtest/`: Grafana dashboards and load profiles.
- `tests/`: rendering and invariant tests.

## Tests

```sh
python3 -m unittest discover -s tests
```

The rendering tests need `helm` and `helmfile` and use only the checked-in
placeholders.
