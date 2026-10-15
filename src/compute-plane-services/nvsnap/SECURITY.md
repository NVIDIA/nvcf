# Security Policy

## Reporting a vulnerability

If you believe you've found a security vulnerability in NvSnap, please **do not** open a public issue. Instead, email the maintainers directly:

**you@example.com**

Include:
- Affected NvSnap component (nvsnap-agent, nvsnap-server, nvsnap-blobstore, libnvsnap, restore-entrypoint, etc.)
- NvSnap image tag where the vulnerability was observed
- Steps to reproduce, including a minimal manifest if applicable
- The impact you've assessed (e.g. RCE, privilege escalation, information disclosure, denial of service)
- Whether the issue is exploitable without an authenticated K8s API connection
- Any suggested mitigation

We aim to acknowledge new reports within 3 business days and to triage them within 10 business days. Critical issues affecting deployed clusters get prioritized.

## What's in scope

- The NvSnap agent (privileged DaemonSet running on GPU nodes — high-impact surface)
- The mutating admission webhook (if deployed)
- The CRIU + cuda-checkpoint integration paths
- The restore-entrypoint binary executed inside workload pods
- The NvSnap HTTP APIs (nvsnap-server, nvsnap-blobstore peer endpoints)
- The libnvsnap intercept library (LD_PRELOAD'd into workloads)
- Container images published to `nvcr.io/0651155215864979/ncp-dev/`

## What's out of scope

- Vulnerabilities in upstream CRIU itself (report to <https://github.com/checkpoint-restore/criu/security>)
- Vulnerabilities in NVIDIA cuda-checkpoint (report through NVIDIA's PSIRT)
- Vulnerabilities in the Kubernetes API or container runtime (containerd/CRI-O)
- Issues that require root on the host *outside* the nvsnap-agent's threat model (nvsnap-agent already runs privileged)

## Disclosure timeline

For confirmed vulnerabilities, the typical timeline is:

| Day | Action |
|---|---|
| 0 | Report received |
| ≤3 | Acknowledgement to reporter |
| ≤10 | Initial triage and severity assessment |
| ≤30 | Fix developed, patch image tag minted (vX.Y.Z+1) |
| ≤90 | Public disclosure (CVE if applicable, advisory + patched release) |

We coordinate with the reporter on disclosure timing. Embargo extensions are negotiable for critical issues that require downstream rollout.

## Supported versions

Pre-OSS, the project is at `v0.0.1`. After OSS release, we'll commit to security backports for the most recent minor version + one prior minor version.
