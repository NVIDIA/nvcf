# Security Policy: NVIDIA Cloud Functions UI (nvcf-ui)

NVIDIA is dedicated to the security and trust of our software products and
services, including all source code repositories managed through our
organization.

If you need to report a security issue, please use the appropriate contact
points outlined below. **Please do not report security vulnerabilities through
GitLab issues, merge requests, or public discussions.**

## Reporting a Vulnerability

If you discover a potential security vulnerability in this project, please
report it through one of the following channels. The first two are the
guaranteed NVIDIA disclosure channels and should be preferred:

* **Web (preferred):** [NVIDIA Vulnerability Disclosure Program](https://www.nvidia.com/en-us/security/)
  — the preferred method for reporting security concerns across all NVIDIA
  products.
* **E-Mail:** [psirt@nvidia.com](mailto:psirt@nvidia.com)
  — We encourage you to use the following PGP key for secure email
  communication: [NVIDIA public PGP Key](https://www.nvidia.com/en-us/security/pgp-key)
* **Repository private reporting (optional fallback, if available on this host):**
  if this repository's host has private vulnerability reporting enabled, you may
  use the repository **Security** tab > **Report a vulnerability**. Not all
  GitLab/GitHub hosts enable this; when it is unavailable, use the web portal or
  PSIRT email above.

If a security vulnerability is reported through public channels (issues, merge
requests, or discussions), maintainers may limit public discussion and redirect
the reporter to the private disclosure channels above.

### What to Include in Your Report

Detailed reports help NVIDIA evaluate and address issues faster. Please include:

- Product/project name and version or branch affected
- Type of vulnerability (e.g., authentication bypass, injection, information
  disclosure, privilege escalation)
- Step-by-step instructions to reproduce the issue
- Proof-of-concept code or exploit (if available)
- Potential impact assessment

### What to Expect

NVIDIA's Product Security Incident Response Team (PSIRT) will:
1. Acknowledge receipt of your report
2. Validate the vulnerability and assess severity
3. Develop and test a fix
4. Publish a security bulletin as appropriate

For ongoing security updates, subscribe to notifications at the
[NVIDIA Product Security](https://www.nvidia.com/en-us/security/) portal.

## Security Architecture & Context

`nvcf-ui` is the management web interface for **NVIDIA Cloud Functions (NVCF)**.
It is composed of two parts that ship together:

- A **React single-page application (SPA)** (`ui/`) built with Vite, TanStack
  Router/Query, Tailwind, and the KUI design system. It renders the management
  console and calls same-origin API paths.
- A **Go backend-for-frontend (BFF)** (`backend/cmd/server`) that serves the
  built SPA as static files and reverse-proxies the SPA's API calls to upstream
  NVCF control-plane services. The SPA never talks to upstream services
  directly.

A second, headless **control-plane health monitor** binary
(`backend/cmd/control-plane`) runs as its own Deployment. It periodically probes
the health of NVCF platform components and writes the results to a Kubernetes
ConfigMap; the BFF reads that ConfigMap to serve `GET /v1/control-plane`.

This software operates at the **Service (backend-for-frontend) / Application
(SPA)** level. Its primary security responsibility is to **broker read-only
access from the browser to upstream NVCF control-plane APIs by attaching
service credentials on behalf of the caller**, and to serve the SPA. It does not
store user data itself; its most sensitive asset is the set of upstream service
bearer tokens it holds in memory.

**Repository Exposure Classification (inferred; pending maintainer confirmation):**
Internal.
Basis: origin remote is an internal, self-managed NVIDIA GitLab host.

**Service Exposure Classification (inferred; pending maintainer confirmation):**
External / Regulated (medium confidence).
Basis: management console for the commercial, customer-facing NVIDIA Cloud
Functions product; runs as a production Kubernetes service, brokers access to
NVCF, NVCT, and cluster control-plane APIs, holds upstream service credentials, and is
packaged and published to the NGC registry with nSpect registration. The BFF's
own external reachability depends on the deploying cluster's gateway
configuration (the Gateway API `HTTPRoute` is off by default in the chart),
which is why confidence is medium rather than high.

### Key Security Boundaries

- **Browser ↔ BFF (untrusted → semi-trusted):** The BFF accepts HTTP requests
  from browsers. It enforces a **read-only surface**: a method-allowlist
  middleware (`middleware.AllowReadMethods`) rejects any method other than
  `GET`/`HEAD` with `405` before the request reaches any handler, and the static
  file handler likewise serves only `GET`/`HEAD`.
- **BFF ↔ upstream APIs (credential injection boundary):** For each proxied
  request the BFF sets `Authorization: Bearer <token>`, where the token is the
  service token for that upstream (NVCF, NVCT, or cluster). The BFF does **not**
  itself authenticate the browser caller; it relies on an upstream
  gateway/ingress to authenticate and authorize end users before requests reach
  it. Requests are proxied path-unchanged (`/v2/nvcf/…`, `/v1/nvct/…`,
  `/v1/si/…`) to fixed in-cluster upstream addresses supplied by configuration.
- **Token source (secret boundary):** Upstream tokens are read from a tokens
  file rendered by an OpenBao/Vault agent sidecar into an in-pod secret volume.
  A file watcher reloads them atomically on rotation and marks a token invalid
  shortly before its JWT `exp`; if a token is missing/invalid the BFF fails the
  request with `500` rather than forwarding an unauthenticated call.
- **Kubernetes control plane (least privilege):** The BFF's ServiceAccount has a
  **namespaced, read-only** Role (`get`/`list`/`watch` on ConfigMaps in its own
  namespace only) used to read the control-plane health ConfigMap. Containers
  run as non-root (UID/GID 65532) from a distroless image with all Linux
  capabilities dropped.

### Threat Model

The following scenarios represent the primary security concerns for this project
(including auxiliary/support code such as the health monitor and audit logging):

1. **Upstream credential exposure via the BFF:** The BFF holds NVCF, NVCT, and
   cluster bearer tokens in memory and injects them into every proxied request. A
   flaw that reflects request/response internals, a proxy misconfiguration, or a
   log statement that captured the `Authorization` header could leak a
   privileged upstream service token. Compromise of the tokens file or its
   OpenBao/Vault agent mount would expose all three tokens at once.
2. **Missing caller authentication → confused-deputy access:** The BFF does not
   authenticate browser callers; it attaches its own service token to every
   request. If the deployment exposes the BFF without an authenticating
   gateway/ingress in front of it, any party that can reach the BFF can drive
   upstream NVCF, NVCT, and cluster read APIs with the service's credentials.
3. **Server-side request forwarding to unintended upstreams:** The reverse proxy
   forwards requests path-unchanged to upstream hosts taken from the
   `NVCF_URL` / `NVCT_URL` / `SIS_URL` configuration. If those values are
   attacker-influenced (e.g., a tampered ConfigMap/env), requests carrying the
   injected bearer token could be directed to an unintended host.
4. **Internal topology disclosure via control-plane health:** `GET
   /v1/control-plane` returns component names, namespaces, and health status
   sourced from the health-monitor ConfigMap. Without an authenticating layer in
   front, this reveals internal platform topology that aids reconnaissance.
5. **SPA deep-link / static-serving abuse:** The BFF maps unknown paths to
   `index.html` to support client-side routing. Errors in static-file indexing
   or cache-control handling could serve stale/incorrect assets; the immutable
   long-cache policy on `assets/` assumes content-hashed filenames.
6. **Supply-chain / build integrity:** The SPA and Go binaries pull dependencies
   (pnpm/npm and Go modules) and are packaged into an OCI image and Helm chart
   published to the NGC registry. A compromised dependency, build step, or CI
   credential (`GL_TOKEN`, NGC push key, publish trigger token) could inject
   malicious code into a released artifact.
7. **Denial of service against the proxy / monitor:** Unauthenticated,
   high-volume GET traffic to the BFF (or to the always-running control-plane
   monitor's upstream probes) could exhaust resources, since the BFF applies no
   built-in rate limiting and relies on upstream/gateway controls.

### Critical Security Assumptions

The following are delegated to other layers; this software does **not** protect
against them on its own:

- **Assumes callers are authenticated upstream.** The BFF performs no user
  authentication or authorization of its own. It assumes an authenticating,
  authorizing gateway/ingress sits in front of it in any exposed deployment, and
  that end-user identity/authorization is enforced there (and/or by the upstream
  NVCF, NVCT, and cluster services).
- **Assumes the OpenBao/Vault token source is trusted.** The BFF treats the
  tokens file and the injected upstream JWTs as authoritative. It decodes the
  JWT payload only to read the `exp` claim for rotation timing; it does **not**
  verify token signatures. Token confidentiality, validity, and issuance are the
  responsibility of the OpenBao/Vault infrastructure and the secret mount.
- **Assumes trusted, network-isolated upstreams.** Requests are proxied over
  plaintext `http://` to in-cluster service addresses. Transport confidentiality
  between the BFF and upstreams is assumed to be provided by the cluster network
  / service mesh, and the configured upstream addresses are assumed to be
  correct and immutable by attackers.
- **Assumes the deployment/orchestration layer is trusted.** Configuration
  (`NVCF_URL`, `NVCT_URL`, `SIS_URL`, tokens path), the Helm chart values, RBAC
  bindings, and ConfigMaps are assumed to be provisioned and protected by the
  cluster operators. The control-plane monitor trusts the components list
  mounted from its ConfigMap.
- **Assumes the container/host platform enforces isolation.** The image runs as
  non-root with all capabilities dropped and relies on Kubernetes and the host
  OS to enforce process, filesystem, and network isolation.
- **Assumes build and release infrastructure is trusted.** Dependency
  resolution, the Bazel/CI build, image signing/publishing to the NGC registry,
  and the CI secrets used to do so are assumed to be operated securely.

## Security Update Process

Security fixes follow the standard NVIDIA PSIRT coordinated-disclosure process.
Releases are cut via the project's semantic-release pipeline and published as
versioned OCI images and Helm charts to the NGC registry. Consumers should track
released versions and apply security-relevant updates promptly.
