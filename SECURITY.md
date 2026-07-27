# Security Policy

## Reporting a Vulnerability

Do not report security vulnerabilities through a public GitHub issue, pull
request, discussion, or other public channel.

Use one of these private reporting channels:

- Web: [NVIDIA Vulnerability Disclosure Program](https://www.nvidia.com/en-us/security/)
  (preferred)
- Email: [psirt@nvidia.com](mailto:psirt@nvidia.com). Use the
  [NVIDIA public PGP key](https://www.nvidia.com/en-us/security/pgp-key)
  for encrypted communication.
- GitHub: Use the repository Security tab and select Report a vulnerability.

Include:

- The affected NVCF version, release, branch, or commit
- The affected component and vulnerability type
- Reproduction steps and required configuration
- Proof-of-concept code, if available
- The expected and observed behavior
- An impact assessment, including affected security boundaries

NVIDIA Product Security Incident Response Team (PSIRT) will acknowledge the
report, validate the issue and severity, coordinate a fix, and publish a
security bulletin when appropriate.

## Security Architecture & Context

NVIDIA Cloud Functions (NVCF) is a Kubernetes-based platform for deploying,
managing, and invoking GPU workloads. This repository contains service code,
CLIs, shared libraries, deployment charts, migrations, examples, and validation
tooling.

NVCF has three primary planes:

- The control plane manages function, task, deployment, secret, and cluster
  state.
- The invocation plane authenticates and routes HTTP, streaming, and gRPC
  requests to function workloads and applies rate limits.
- The compute plane connects GPU clusters to the platform. NVIDIA Cluster Agent
  (NVCA) turns platform requests into Kubernetes resources and manages workload
  lifecycle.

NATS JetStream connects these planes and buffers work. Kubernetes, artifact
registries, object storage, secret stores, telemetry backends, and platform data
stores are external security dependencies.

Repository Exposure Classification: Public.
Basis: `NVIDIA/nvcf` is a public repository on GitHub. This document uses
public-safe detail.

Service Exposure Classification: External / Regulated (high confidence).
Basis: NVCF exposes customer-facing APIs, runs production GPU workloads, and
handles credentials, workload artifacts, request and response data, secrets,
and telemetry.

### Security Boundaries

1. External callers cross an ingress boundary into the control-plane and
   invocation APIs.
2. Invocation services cross an authorization boundary when they validate a
   caller and resolve the function versions that caller may invoke.
3. Control-plane and invocation services cross a messaging boundary when they
   publish work to NATS JetStream.
4. NVCA crosses a cluster-administration boundary when it creates and manages
   pods, Helm releases, custom resources, service accounts, and related
   Kubernetes objects.
5. User-supplied function and task containers run inside the compute plane but
   are not trusted as platform components.
6. Artifact, secret, registry, object-storage, and telemetry integrations cross
   deployment-specific network and credential boundaries.

## Threat Model

This threat model incorporates the repository architecture, current service
implementations, and an existing STRIDE assessment. Threats are ordered by
expected impact and exposure.

1. Authorization and tenant-routing confusion

   The HTTP invocation service protects legacy invocation routes retained only
   for backward compatibility, transparent load-balancer routes, and worker
   attach routes in
   `src/invocation-plane-services/http-invocation/crates/server/src/app.rs`.
   The legacy routes are deprecated and must not be used for new integrations.
   It delegates function authorization through
   `nvcf_api/mod.rs`. The gRPC proxy independently uses bearer tokens,
   `function-id`, function-version metadata, request IDs, and connection state
   in `src/invocation-plane-services/grpc-proxy/proxy/director.go`. Incorrectly
   binding any token, function identifier, cached authorization result, request
   ID, or worker connection could let one caller invoke or reconnect to another
   tenant's workload.

2. Privileged workload orchestration

   NVCA consumes platform work messages and manages `ICMSRequest`,
   `MiniService`, pod, and Helm workload state in GPU clusters. A forged,
   replayed, or incorrectly authorized launch specification could cause
   unintended images, charts, service accounts, volumes, or Kubernetes
   resources to run with compute-plane privileges. Queue authentication,
   namespace isolation, admission policy, and least-privilege RBAC are critical
   controls.

3. Secret and credential disclosure

   Invocation bearer tokens, NATS and OAuth credentials, rate-limiter tokens,
   registry credentials, object-storage URLs, and workload secrets pass through
   multiple services. Relevant paths include
   `src/invocation-plane-services/http-invocation/crates/server/src/middleware/auth.rs`,
   `src/invocation-plane-services/grpc-proxy/proxy/credentials/bearer.go`,
   `src/control-plane-services/nats-auth-callout`, the OpenBao deployment
   assets, and `src/compute-plane-services/image-credential-helper`. Logging,
   tracing, error handling, configuration output, or telemetry export that
   records these values could expose access to platform or tenant resources.

4. Request, response, and artifact data exposure or tampering

   The invocation service can buffer or stream request bodies, forward headers
   through NATS, issue worker attach tokens, and use object storage for assets
   and large responses.
   `src/compute-plane-services/worker-init/internal/downloader/downloader.go`
   downloads workload artifacts from credential-bearing URLs. A stolen or
   misbound attach token or signed URL could expose customer data. A tampered
   request, response, model, container, or Helm artifact could alter workload
   behavior or execute attacker-controlled code.

5. Resource exhaustion and rate-limit bypass

   Public HTTP, streaming, gRPC CONNECT, status, and worker attach paths can
   hold connections, buffer data, enqueue work, trigger autoscaling, and consume
   GPU capacity. Disabled, unavailable, incorrectly scoped, or bypassed rate
   limiting could permit denial of service or unexpected cost. Request-size,
   connection, queue, timeout, and per-tenant workload limits must remain
   effective together.

6. Telemetry data exposure

   `src/compute-plane-services/byoo-otel-collector` processes workload logs,
   metrics, and traces, including log bodies and Kubernetes metadata.
   Misconfigured exporters, overly broad metadata collection, unsafe log
   content, or weak backend access control could disclose tenant data,
   credentials, model details, or operational topology.

7. Deployment and software supply-chain tampering

   Bazel rules, container image definitions, Helm and Helmfile configuration,
   migrations, generated manifests, and release automation determine what runs
   in control-plane and compute-plane clusters. A compromised dependency,
   registry credential, image tag, chart source, migration, or release workflow
   could introduce code or configuration across many NVCF deployments.

## Critical Security Assumptions

- The configured identity provider issues valid, short-lived credentials, and
  authorization services correctly bind callers to organizations, functions,
  function versions, and administrative actions.
- Production ingress terminates TLS with current protocols and certificates,
  filters abusive traffic, and exposes only intended service routes.
- NATS JetStream, platform data stores, object storage, registries, secret
  stores, and telemetry backends authenticate peers, encrypt traffic, enforce
  least privilege, and protect data at rest.
- Kubernetes control planes, nodes, container runtimes, RBAC, admission
  controls, network policies, and namespace boundaries isolate platform
  components and mutually untrusted workloads.
- User-provided container images and Helm charts are untrusted relative to the
  platform. Deployments apply the policies needed to prevent workload access to
  host resources, other tenants, and platform credentials.
- Images, charts, binaries, models, and other artifacts come from approved
  sources and retain their expected integrity between publication and use.
- Worker attach tokens, signed object-storage URLs, session identifiers, and
  cached authorization decisions remain short-lived and bound to the intended
  request, caller, function, and deployment.
- Operators enable and correctly scope rate limits, timeouts, quotas, and queue
  limits for production traffic.
- Health, metrics, profiling, debugging, and administrative endpoints are
  restricted to authorized operator networks and are disabled when unnecessary.
- Applications avoid placing secrets or sensitive request data in logs, traces,
  metrics, error messages, or other telemetry.

## Trust Model

- NVCF maintainers, signed release automation, and authorized deployment
  operators are trusted to publish and configure platform components.
- Authenticated callers are trusted only for the organizations, functions, and
  actions granted by the authorization service.
- Function and task images, charts, request bodies, headers, model artifacts,
  and workload-generated telemetry are untrusted inputs.
- NVCA and other cluster controllers are privileged components. Their NATS
  identities, Kubernetes service accounts, and reconciliation inputs must be
  tightly scoped.
- Cloud services, Kubernetes, NATS, registries, secret stores, object storage,
  and telemetry systems are conditionally trusted dependencies. NVCF relies on
  their configured identity, encryption, isolation, durability, and audit
  controls.

## Deployment Assumptions

Self-managed operators are responsible for:

- Configuring TLS, ingress filtering, network policy, and private access for
  internal service endpoints
- Supplying least-privilege identities for NATS, Kubernetes, registries,
  object storage, data stores, and secret stores
- Enabling authentication, authorization, rate limiting, quotas, and audit
  forwarding for production environments
- Pinning and verifying approved image, chart, and dependency versions
- Rotating credentials and limiting access to secret-bearing files and
  Kubernetes Secrets
- Restricting workload egress and preventing access to node, metadata-service,
  and platform control credentials
- Protecting telemetry pipelines and applying retention, redaction, and access
  policies appropriate for customer data

## Repository Scope Notes

These notes help distinguish deployable security impact from repository-only
test and documentation content. They do not suppress findings by themselves.

- Files under `examples/`, test directories, and documentation may contain
  placeholder credentials, sample payloads, local endpoints, or intentionally
  insecure configurations used to demonstrate a boundary. Confirm a production
  path before treating them as deployed secrets or controls.
- Files under `vendor/` are third-party source snapshots. Assess findings
  against the version used by a shipped component and the reachable call path.
- Local-development configuration may disable TLS, authentication, or rate
  limiting to support isolated testing. Such settings are unsafe if promoted to
  a production deployment.
- Security issues in user-provided workload code are normally owned by that
  workload. They are NVCF issues when the platform breaks an isolation,
  authorization, confidentiality, integrity, or availability boundary.

## Operational Guidance

- Use short-lived credentials and least-privilege service accounts.
- Keep authorization, rate limiting, and worker-connection checks fail-safe for
  protected operations.
- Pin production artifacts by immutable version or digest and scan dependencies,
  images, and charts before release.
- Restrict NATS subjects, Kubernetes namespaces, secret paths, registry scopes,
  and object-storage objects to the smallest required audience.
- Redact authorization headers, signed URLs, request bodies, model data, and
  secrets from logs and telemetry.
- Monitor authentication failures, denied rate-limit checks, queue growth,
  unusual autoscaling, secret access, and privileged Kubernetes changes.
- Re-run the repository security review after material architecture, deployment,
  authentication, data-flow, or trust-boundary changes.
