# Operations Docs

> Type: Index. Operator-facing contracts and runbooks.

Use these docs for externally consumed contracts and operator workflows.

## Current Contracts

- [API gateway integration contract](../api-gateway-contract.md): gateway-owned
  auth, routing headers, model discovery, proxy request/response behavior,
  retry guidance, and security requirements.
- [Deployment shape](deployment-shape.md): Kubernetes services, pods, router
  role, and validation commands.
- [Pylon onboarding](pylon-onboarding.md): runtime requirements, stats surface,
  pylon flags, and network shape.
- [Observability](observability.md): metrics endpoints, request correlation,
  and first checks.
- [Tunnel transport selection](../tunnel-transports.md): deployment-level
  backend tunnel choice and load-balancer requirements.
- [Release Please layout](../release-please.md): repo-wide release track,
  generated files, GitHub workflow, and Buildkite tag behavior.

## Runbooks

- [Troubleshooting](troubleshooting.md): symptom-to-layer map for proxy,
  routing, tunnel, calibration, and stats failures.
- [StatefulSet pod management policy migration](change-control/statefulset-pod-management-policy.md):
  one-time migration runbook for immutable Kubernetes StatefulSet policy
  changes.

## Related Context

- [Architecture docs](../architecture/README.md): deeper operational behavior
  and invariants.
- [Testing docs](../testing/README.md): required validation after deployment,
  Kubernetes, or behavior changes.
