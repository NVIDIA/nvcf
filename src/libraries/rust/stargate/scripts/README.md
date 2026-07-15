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
