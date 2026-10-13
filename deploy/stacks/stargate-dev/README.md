# Stargate development deployment (example)

This directory is an example of a multi-region Stargate development
environment on AWS. It runs Stargate against mock inference backends
(MockDynamo with a Pylon sidecar) so routing policies can be benchmarked
across regions without GPUs.

It is not a supported production deployment. Every AWS account ID, ARN,
hostname, image reference, and network range checked in here is a
placeholder. A deployment supplies its own values in a protected file outside
the repository.

## What it deploys

For each region in `environments/`:

- One hub EKS cluster with Stargate, the backend router, the dev-only auth
  fixture, and a Grafana Alloy metrics collector.
- Two MockDC EKS clusters. Each runs two backends, and each backend is one
  MockDynamo container with a Pylon sidecar.

The example environments define five regions (`us-west-2`, `us-east-1`,
`eu-west-1`, `ap-northeast-1`, `ap-southeast-2`) with 20 backends in total.
Each Pylon registers with its own region's Stargate. A hub republishes the
peer hub URLs in `router.remoteWatchUrls` to its Pylons, and each Pylon also
registers with those peer hubs. In a connected topology every hub sees all
20 backends.

Connected regions share one credential bundle, one `routingKey`, and one
router CA. Each hub authenticates every registering Pylon with its own dev
auth fixture, and each Pylon trusts only the CA in its local
`stargate-router-ca` Secret.

## Prerequisites

Tools on `PATH`: the AWS CLI, `kubectl`, `helm` with the `helm-diff` plugin,
`helmfile`, and `python3` with PyYAML.

AWS resources you provide:

- An AWS account. Export its ID as `STARGATE_DEV_AWS_ACCOUNT_ID`.
- Per region, three EKS clusters whose names, kube contexts, and Kubernetes
  version match `environments/<region>.yaml`. Deployment preflight checks
  each value and requires cluster-admin access.
- At least two schedulable nodes in each MockDC cluster. The cluster's two
  backends use required Pod anti-affinity on `kubernetes.io/hostname`, so a
  single-node cluster leaves one backend Pending.
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
for Grafana. `scripts/observability.py` creates both roles.

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
`loadtest/reduced.yaml` defines the suites and their workloads. Without
`--suite-file`, the CLI uses the embedded `loadtest/suite.yaml`.

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
it does not apply configuration. Put the complete selected JSON object in
`router.loadBalancerConfig` in each region's protected deployment values when
applying the policy.

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

## Layout

- `environments/`: one file per region with cluster names and per-backend
  MockDynamo settings.
- `values/versions.yaml`: image and chart version pins.
- `helmfile.yaml.gotmpl`: releases and install order.
- `charts/`: the dev auth fixture and MockDC charts.
- `scripts/`: deployment and observability wrappers.
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
