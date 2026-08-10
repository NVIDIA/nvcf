# LLM OpenBao Addon

This addon provisions the shared NVCF service-issuing PKI hierarchy that cert-manager uses to issue server certificates for the LLM stack (Stargate / LLM Request Router QUIC TLS).

## What it does

- Enables `pki` secrets engines at `services/all/pki/root` and `services/all/pki/nvcf-service-issuing` (idempotent — only creates each mount if absent).
- Generates the root CA at `services/all/pki/root` if no CA exists there.
- Generates a service-issuing intermediate at `services/all/pki/nvcf-service-issuing` and signs it from the root if no CA exists there.
- Creates signing role `nvcf-service-server` with `allow_bare_domains=false`, `allow_subdomains=true`, wildcards, RSA-2048, leaf max TTL 2160h (90 days).
- Writes a narrow cert-manager policy (`services-all-pki-nvcf-service-sign`) that grants `create,update` on the signing endpoint and `read` on the CA bundles.
- Writes a root-CA read policy (`services-all-pki-root-ca-ro`) for SIS/Spot trust-anchor rendering through Vault Agent.
- Binds the cert-manager Kubernetes ServiceAccount to its policy via JWT auth.
- Read-modify-writes the `sis-api` JWT auth role so it gains the root-CA read policy in addition to whatever earlier migrations (e.g. `05_setup_sis.sh`) attached.

## Failure semantics

Fail-hard when enabled. If the script aborts (missing required env, OpenBao error, etc.), the entrypoint records the failure in its `FAILED_MIGRATIONS` accumulator and exits non-zero — same contract as core migrations. Combined with the consumer chart's `restartPolicy: OnFailure` + `backoffLimit`, transient errors retry but deterministic failures (e.g., missing `NVCF_SERVICE_PKI_ALLOWED_DOMAINS`) surface as a Job-level failure that blocks the Helm hook. `MIGRATIONS_ALLOW_FAILURES=true` is the explicit operator escape hatch for emergency rollback.

## Environment

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `ADDONS_LLM_ENABLED` | yes (set by consumer chart) | `false` | Opt-in gate. When `true`, the entrypoint invokes this addon and propagates its exit code to the Job. Set by the consumer chart's dedicated Job (mirrors the SIS chart's `hook-lls-migrations.yaml`), typically together with `CORE_MIGRATIONS_ENABLED=false` so the Job runs the addon only. |
| `NVCF_SERVICE_PKI_ALLOWED_DOMAINS` | yes (when addon enabled) | — | Comma-separated DNS suffixes the role allows cert-manager to issue against. The consumer chart derives this from its own values (typically `<customer-domain>,cluster.local`); not a manually-set operator knob. Missing value → script aborts non-zero → entrypoint accumulator → Job fails. |
| `CERT_MANAGER_SERVICE_ACCOUNT_NAME` | no | `cert-manager` | Service account cert-manager uses to authenticate to OpenBao. |
| `CERT_MANAGER_SERVICE_ACCOUNT_NAMESPACE` | no | `cert-manager` | Namespace for the cert-manager service account. |
| `NVCF_SERVICE_PKI_PATH` | no | `services/all/pki/nvcf-service-issuing` | OpenBao mount path for the service-issuing intermediate. |
| `NVCF_SERVICE_PKI_ROLE_NAME` | no | `nvcf-service-server` | OpenBao PKI role name. |
| `NVCF_SERVICE_PKI_LEAF_MAX_TTL` | no | `2160h` | Leaf certificate max TTL (90 days). Must accept the consumer chart's Certificate `duration` (typically 2160h with `renewBefore: 720h`). |
| `NVCF_SERVICE_PKI_INTERMEDIATE_MAX_TTL` | no | `43800h` | Service-issuing intermediate max-lease TTL (5 years). Must exceed any leaf max TTL. |
| `NVCF_SERVICE_PKI_INTERMEDIATE_COMMON_NAME` | no | `NVCF Service Issuing Intermediate CA` | CN for the service-issuing intermediate. |
| `NVCF_SERVICE_PKI_ROOT_COMMON_NAME` | no | `NVCF Root CA` | CN for the root CA. |
| `NVCF_SERVICE_PKI_ROOT_TTL` | no | `87600h` | Root CA TTL (10 years). Rarely needed. |
| `NVCF_SERVICE_PKI_ROOT_MAX_TTL` | no | `87600h` | Root CA mount max-lease TTL. Rarely needed. |
| `SIS_SERVICE_ACCOUNT_NAME` | no | `sis-api` | Service account Spot/SIS uses to authenticate to OpenBao. |
| `SIS_SERVICE_ACCOUNT_NAMESPACE` | no | `sis` | Namespace for the Spot/SIS service account. |

## Idempotency

Re-running this addon against an already-provisioned OpenBao is safe: CA generation is guarded by `bao read .../cert/ca`, role and policy writes overwrite cleanly, and the SIS JWT role uses a read-modify-write that preserves existing policies. Enabling the addon after initial deployment provisions the PKI on the next Job run with no manual intervention.

## Usage

The LLM consumer chart owns invocation via a dedicated Helm hook Job (mirrors the SIS chart's `hook-lls-migrations.yaml`). The Job sets `CORE_MIGRATIONS_ENABLED=false`, `ADDONS_LLM_ENABLED=true`, and `NVCF_SERVICE_PKI_ALLOWED_DOMAINS=<derived-from-chart-values>`, then runs this image's entrypoint which dispatches to `setup_llm.sh`. See the main [README](../../README.md) for the entrypoint contract.
