# Stargate agent guide

This guidance covers the Rust workspace, including `stargate-k8s-router` and
the shared `stargate-forwarding` crate.

## Assumed deployment invariants

- `stargate-k8s-router` supports `raw-quic` and `webtransport` tunnel traffic.
  Plain `http3` tunnel traffic uses an L4 path and does not pass through this router.
- Raw QUIC request streams are bidirectional. During drain, prioritizing
  bidirectional acceptance over unidirectional acceptance is intentional.
- WebTransport uses a separate session bridge. Do not infer a plain HTTP/3
  stream fairness requirement for the raw QUIC relay from that implementation.

For router transport changes, inspect `crates/stargate-k8s-router/src/quic.rs`
and `crates/stargate-forwarding/src/lib.rs`. The
[transport guide](docs/tunnel-transports.md) describes available protocol
implementations and their routing paths.

## Build and verification

Run commands from this directory. For router and relay Rust changes:

```sh
cargo build --locked -p stargate-k8s-router
cargo test --locked -p stargate-k8s-router -p stargate-forwarding -- --test-threads=1
cargo clippy --locked -p stargate-k8s-router -p stargate-forwarding --all-targets -- -D warnings
cargo fmt --all -- --check
```

For other Rust changes, select the affected workspace packages with `-p`.
For documentation changes, check whitespace and link targets.

## Mandatory Pull Request validation

Before marking a Pull Request ready or merging, run at least one local, AWS,
or CI mock integration test for every meaningful Stargate change. This covers
Stargate, router/relay, Pylon, mock engines, authentication, dependencies, image
pins, related charts, deployment settings, benchmark logic, and CI changes
that affect how these components build or run. Pure documentation and formatting
changes are exempt; explain the exemption in the Pull Request.

- Build the affected binaries or images from the candidate commit. Send real
  requests through the changed path with mock inference backends. Check
  registration/readiness, response correctness, completion, and streaming when
  applicable. Exercise the affected transport and routing policies.
- Choose a relevant loopback integration test such as
  `scripts/e2e-last-cluster.sh`, a local k3d deployment, an isolated AWS mock
  deployment, or a CI job that runs equivalent request traffic. See the
  [development stack](../../../../deploy/stacks/stargate-dev/README.md) for
  deployment, streaming checks, and benchmark campaigns.
- Unit tests, chart rendering, mocked kubectl, microbenchmarks, and readiness
  checks alone do not satisfy this integration gate. A CI job must exercise
  the affected runtime path; a green build alone does not count.
- Follow the root local QA safety rules. Give runs unique resources and
  artifacts. Serialize campaigns that share backends or mutate deployment
  state; separate output directories do not isolate caches, queues, or resets.
- Record the tested commit, commands, environment, image digests, policies,
  workload, acceptance results, and retained artifact links in the Pull Request.
  Retest after changes that alter the validated behavior. Blocked or failed
  validation keeps the Pull Request unready and blocks merge.

## Performance regression gate

For changes that can affect request handling, routing, transport, caching,
concurrency, scheduling, resource limits, or performance measurements, compare
the merge-base baseline and candidate on the same test environment before
marking the Pull Request ready or merging.

Scope the comparison before running it:

- Compare baseline and candidate image digests and runtime settings for each
  component on the measured path. When both match, record them and skip the
  comparison for that component; identical builds measure only noise.
- Benchmark only the components that differ. For example, a change limited to
  charts and the auth fixture needs the integration gate plus an auth RPC
  benchmark, not a routing comparison.
- Select the policies and workload profiles that exercise the changed path.
  Do not run the full policy and profile matrix by default.

Run the comparison:

- Use release builds and identical hardware, resource allocation, topology,
  mock-engine settings, policy, load-generator version, workload/seed, offered
  load, concurrency, cache state, warmup, and run duration. Change only the
  component or setting under test. Exclude unrelated traffic and overlapping
  campaigns. Local and AWS measurements must be compared within their own
  environments.
- Run at least three measured repetitions per revision for each selected
  profile, alternate revision order, and report individual results and
  variation. Use representative sustained load. A short smoke run alone is
  insufficient.
- Compare successful throughput, p95/p99 time to first token and end-to-end
  latency, failures/timeouts/retries, and CPU/memory where relevant. Include
  admission failures and cancellations, not only completed-request latency.
- Retain raw baseline and candidate reports and a comparison table. Algorithm
  comparisons within one build do not establish revision-level performance.
  Explain unavailable metrics and collect another observable for that risk.

Judge the results:

- Treat a candidate metric as worse only when it falls outside the baseline
  range across repetitions and moves in the same direction in every pair.
  Report smaller or mixed differences as within run-to-run variation.
- Policies that sample backends without a fixed seed, such as PowerOfN, choose
  different backends for the same workload on each run. Note this in the
  report; their tail variation alone does not establish a regression.
- Investigate any result that meets the worse-result rule. Any reproducible
  degradation blocks merge. For an inconclusive profile, run at most one
  additional reversed-order pair, then report the result and ask the user to
  decide instead of collecting more runs.
- An intentional tradeoff requires explicit user approval with the measured
  impact before merge. Mock results support the tested workload, not
  production capacity claims.

## Routing methods

`routing_methods.json` lists every load-balancing algorithm name, alias, and
routing-expression parameter. When you add, rename, or remove one, update that
file and run `cargo test --locked -p stargate --lib routing_method`. A failure
names the other code, docs, and configs to update.

The function API in `src/control-plane-services/cloud-functions/nvcf-core`
stores `routingMethod` after a format check only. Its
`LlmRoutingMethodValidatorTest` reads the manifest and fails if that check
would reject a listed name, alias, or parameter. Run it from the repository
root:

```sh
bazel test //src/control-plane-services/cloud-functions/nvcf-core:tests \
  --test_arg=--exclude-classname='^(?!com\.nvidia\.nvcf\.rest\.function\.management\.dto\.LlmRoutingMethodValidatorTest$).*$'
```

## LLM routing BDD before push

For LLM routing or request-handling changes, run the single-cluster Helmfile
suite before pushing. Also run the multi-cluster Helmfile suite when changing
cross-cluster routing or transport. Their `@llm-function-type` scenarios cover
chat streaming, Responses, classic invocation rejection, and unlisted models.
The scenarios depend on the earlier install steps; run the whole feature.

Read [BDD prerequisites](../../../../tests/bdd/README.md) and follow the root
local QA safety rules before a live run. From the repository root:

```sh
cd tests/bdd
go test -short ./...
golangci-lint run --config .golangci.yml ./...
go test -run '^TestSingleClusterHelmfile$' -timeout 90m -v
go test -run '^TestMultiClusterHelmfile$' -timeout 90m -v
```

Run the live suites one at a time with an approved environment. Anchors exclude
the fake-runner wiring siblings. A wiring pass does not validate a deployment.
If a required live run fails or is blocked, stop before pushing and report the
unvalidated behavior.

## Code style

Use Rust 2024 and the workspace formatter and Clippy rules. Match existing
crate structure and use `tracing` for structured logs. Keep deployment
assumptions explicit when reviewing transport behavior.
