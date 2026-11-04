# Security Policy: Admin Issuer Proxy

## Reporting a Vulnerability

If you discover a potential security vulnerability, **do not open a public issue or merge request**.

- Report it through the [NVIDIA Vulnerability Disclosure Program](https://www.nvidia.com/en-us/security/) (preferred).
- Email [psirt@nvidia.com](mailto:psirt@nvidia.com). Use the [NVIDIA public PGP key](https://www.nvidia.com/en-us/security/pgp-key) for sensitive details.

Include the affected project version or branch, vulnerability type, reproduction steps, proof-of-concept code when available, and an impact assessment. NVIDIA PSIRT will acknowledge the report, validate its severity, coordinate and test a fix, and publish a security bulletin when appropriate.

## Security Architecture & Context

Admin Issuer Proxy is a Go HTTP service that exposes `POST /v1/admin/keys`, reads a Vault Agent token from `VAULT_TOKEN_FILE`, calls the configured Vault/OpenBao signing path, and returns an admin-scoped JWT in the api-keys response format. At startup, `internal/servicecache` fetches and caches service identity and audience metadata from the configured api-keys endpoint. The chart under `deploy/helm/admin-token-issuer-proxy` places the service behind a gateway and injects its Vault identity.

This software operates at the **Service** level. Its primary security responsibility is to ensure that only trusted callers can cause `internal/handlers.Keys` to mint admin credentials and that the Vault token, minted JWT, authorization scopes, and cached service metadata retain their confidentiality and integrity.

**Repository Exposure Classification:** Public.
Basis: the source is maintained in the public `NVIDIA/nvcf` repository.

**Service Security Classification:** Privileged (high confidence).
Basis: this service handles Vault credentials, mints admin-scoped JWTs, publishes a container through CI, and participates in privileged token-issuance workflows.

The main trust boundaries are the gateway-to-service HTTP route, the Vault Agent token file, the service-to-Vault signing call, the service-to-api-keys metadata call, and the response carrying the minted JWT. Environment variables select the listener, metadata URL, Vault address, signing path, and role. The Kubernetes workload runs as a non-root user with a read-only root filesystem, dropped capabilities, and a runtime-default seccomp profile.

### Threat Model

1. **Unauthorized admin token minting:** `internal/handlers.Keys` checks only the HTTP method before reading the Vault token and invoking `SignToken`; if the gateway route exposes `/v1/admin/keys` without effective caller authentication and authorization, an untrusted caller can obtain an admin-scoped JWT.
2. **Credential disclosure on unprotected HTTP links:** `VAULT_ADDR` and `SERVICE_METADATA_URL` can be configured to use plain HTTP, and `Keys` returns the minted bearer token in its JSON response. A compromised network path, proxy, or adjacent workload could observe or alter these sensitive exchanges when transport protection is not provided externally.
3. **Signing target redirection through deployment configuration:** `VAULT_ADDR`, `SIGN_PATH`, `ROLE`, and `VAULT_TOKEN_FILE` directly control where the Vault token is sent and which signing endpoint is invoked. Unauthorized modification of the Deployment or its environment can redirect credentials or mint tokens under an unintended role.
4. **Unverified JWT claims shaping authorization output:** `vault.DecodeJWTClaims` base64-decodes the JWT payload without verifying its signature, then `handlers.Keys` uses `sub`, `scopes`, `iat`, and `exp` in the response. A compromised or misconfigured signing backend can therefore inject misleading ownership, scope, or lifetime fields.
5. **Poisoned or stale service metadata:** `servicecache.Fetch` trusts the first object returned by `SERVICE_METADATA_URL` and never refreshes it. A compromised api-keys response, incorrect service ordering, or stale startup data can cause the proxy to emit an incorrect issuer or audience for every subsequently minted token response.
6. **Request-driven resource exhaustion:** every valid POST reads a token file, creates a Vault client, and performs a signing request. Unbounded or slow requests can consume connections and signing capacity unless the gateway enforces limits.
7. **Build-chain compromise:** a compromised Go dependency, Bazel rule, toolchain, or pinned container base can alter the released proxy container.

### Critical Security Assumptions

- The gateway authenticates and authorizes callers before forwarding `POST /v1/admin/keys`, and applies appropriate request limits.
- The cluster network or a service-mesh layer provides authenticated, confidential transport for HTTP connections to Vault/OpenBao and api-keys.
- Vault Agent protects and rotates `VAULT_TOKEN_FILE`, while Vault policy restricts that token to the intended signing role and path.
- Vault/OpenBao is trusted to return a correctly signed JWT with authoritative claims; this service does not verify the JWT signature.
- The configured api-keys endpoint is trusted to return the intended service as the first item, and its metadata remains valid for the process lifetime.
- Kubernetes admission and deployment controls preserve the non-root, read-only filesystem, dropped-capability, seccomp, service-account, and projected-token settings in the Helm chart.
- CI runners, pinned base images, module proxies, and release registries are trusted to supply reviewed build inputs and protect publishing credentials.
