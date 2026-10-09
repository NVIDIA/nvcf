# Repository Scripts

The `scripts/` directory contains supported repository checks, local workflow
helpers, and their Python unit tests.

## Manual Python Test Suite

Run all Python support-script tests with:

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
```

This suite is intentionally manual. Individual repository gates run the focused
test modules they own, such as the coverage-policy tests in
`scripts/check_coverage_quality.sh`.

Use the discovery command after changing Python support scripts, their tests,
Buildkite quality checks, Kubernetes integration helpers, benchmark profiling
helpers, or PR reporting utilities.

## Tilt Lifecycle

Use the Make targets rather than calling `tilt up` directly:

```bash
make tilt-up-kind
make tilt-up-docker-desktop
make tilt-up
```

The generic `make tilt-up` target snapshots the current kubectl context before
starting Tilt, so teardown remains pinned to the same cluster.

Run the CI integration suite through the same lifecycle wrapper:

```bash
python3 scripts/run_tilt.py ci --context kind-kind --timeout 30m
```

The runner supports `up` and `ci`. When Tilt exits successfully, fails, or is
interrupted, it invokes `tilt down --delete-namespaces`, preserving compatible
context, namespace, Tiltfile, and Tiltfile arguments, then removes the matching
CoreDNS rewrite. This removes the Kubernetes resources and cluster-wide DNS
side effect owned by that Tilt instance. The selected context is also applied
to kubectl calls made by the local integration and manifest-rendering helpers.

## PR Checks

The PR gate runs repository header and diff checks, then runs the code-change
gates only when code-impacting paths changed.

Run the support-script tests after changing repository tooling:

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
```

## Last-Cluster End-to-End Check

`scripts/e2e-last-cluster.sh` runs the LLM API gateway against a real
Stargate, two Pylons, and two mock-dynamo backends on loopback. It reproduces
the overflow cache-thrash scenario for `last_cluster_affinity`:

1. Two long requests fill the session's rank-1 cluster (A).
2. The session's first gateway request overflows to cluster B.
3. A drains.
4. The next 10 gateway requests with the same `prompt_cache_key` stay on B
   with the feature on, and return to A with it off.

```bash
scripts/e2e-last-cluster.sh
scripts/e2e-last-cluster.sh --algorithm pulsar-wait-and-widen --mode on
```

Mode `on` sets `STARGATE_LAST_CLUSTER_ENABLED=true` in the gateway and
`last_cluster_affinity: true` in Stargate. Mode `off` disables both. The
script builds release binaries and the gateway first unless `--no-build` is
set. It attributes each request to a cluster through the mock-dynamo
`/test-control` counters, checks
`stargate_routing_session_selections_total`, and exits non-zero on any
mismatch. It needs `cargo`, `go`, `curl`, and `jq`, uses 32 loopback ports
starting at `E2E_PORT_BASE` (default `27100`), and keeps logs on failure.
