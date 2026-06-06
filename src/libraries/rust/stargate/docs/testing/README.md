# Testing Docs

> Type: Index. Verification entrypoint for behavior, coverage, and CI work.

Use these docs to choose the smallest verification set that proves a change.

## Current Contracts

- [Feature and behavior test matrix](../feature-behavior-test-matrix.md):
  externally visible Stargate/Pylon behavior and the tests that cover it.
- [Lifecycle integration test coverage](../lifecycle-test-coverage.md):
  registration, bringup, active canary, routing lifecycle, model discovery,
  and Kubernetes lifecycle coverage.
- [Code coverage and test quality](../code-coverage.md): local and CI
  coverage gates, patch coverage, CRAP baselines, standalone mutation testing,
  and report interpretation.
- [Docs health check](../reference/README.md): docs references are validated by
  `scripts/check_docs.sh`.

## Related Context

- [Architecture docs](../architecture/README.md): behavior contracts that tests
  should prove.
- [Local benchmark runner](../local-benchmark-runner.md): benchmark checks and
  benchmark-specific validation.
