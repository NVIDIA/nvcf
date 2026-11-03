# Security Policy

## Reporting a vulnerability

Do not report security vulnerabilities through a public GitHub issue, pull
request, discussion, or other public channel.

Use one of these private reporting channels:

- Web: [NVIDIA Vulnerability Disclosure Program](https://www.nvidia.com/en-us/security/)
  (preferred)
- Email: [psirt@nvidia.com](mailto:psirt@nvidia.com). Use the
  [NVIDIA public PGP key](https://www.nvidia.com/en-us/security/pgp-key)
  for encrypted communication.
- GitHub: use the NVIDIA/nvcf repository's Security tab and select
  Report a vulnerability.

Include:

- The affected nvstor version (`nvstor-peer --version` prints the version
  and commit), release, branch or commit
- The affected component and vulnerability type
- Reproduction steps and required configuration
- Proof-of-concept code, if available
- The expected and observed behavior
- An impact assessment, including the affected security boundary

NVIDIA Product Security Incident Response Team (PSIRT) will acknowledge the
report, validate the issue and severity, coordinate a fix, and publish a
security bulletin when appropriate.

## Deployment assumptions

nvstor is storage infrastructure meant to run on a private network between
the nodes that use it. Which interfaces authenticate their clients, which
rely on the network or an address allow-list, and how to restrict each one
is described in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), section 10
("Security model"). Components marked experimental in the
[README](README.md) have not had the hardening a production deployment
needs.
