# Benchmark Docs

> Type: Index. Benchmark workflows first; historical evidence is separate.

Use benchmark docs for measurement workflows and performance evidence. Benchmark
results are diagnostic unless the document says the run was designed as
release-quality evidence.

## Current Workflows

- [Local benchmark runner](../local-benchmark-runner.md): `stargate-bench`
  Kubernetes flow, transport microbenchmark, reliability controls, profiling,
  scenario config, outputs, and verification.

## Evidence Reports

- [Load-balancer E2E balance results, 2026-05-27](../benchmark-results/2026-05-27-load-balancer-e2e.md):
  historical local Kind/Tilt measurements and later corrected growing-prefix
  supplements.

## Related Context

- [Testing docs](../testing/README.md): gates required before presenting a
  branch as verified.
- [Tunnel transport selection](../tunnel-transports.md): transport benchmark
  interpretation and protocol-selection guidance.
