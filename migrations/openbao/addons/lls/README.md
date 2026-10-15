# LLS (TURN) OpenBao Addon

This addon configures OpenBao for the TURN/LLS service: KV secrets engine, HMAC key for credential signing, optional public DNS certificate path, and JWT auth role for the `turn` ServiceAccount in `gdn-streaming`.

## What it does

- Enables KV v2 at `services/turn/kv`
- Creates `keys/hmac` with a generated key (id, timestamp, value); `max_versions=3`
- Creates `keys/public-dns-certificate` with a placeholder; `max_versions=2`. Replace with the real cert via `bao kv put` (see below)
- Writes policies for read and metadata access
- Creates JWT auth role `turn` bound to `gdn-streaming` namespace and the `turn` service account

## Public DNS certificate

TURN can read the public DNS cert from `kv.public_dns_certificate.current.value.cert` (base64-encoded PEM: cert chain + private key). This script creates the path with a placeholder so the key exists; it does not overwrite an existing value.

**Set the real certificate (after LLS setup has run):**

```bash
# Encode your PEM (cert + key concatenated) and put into OpenBao
CERT_B64=$(cat /path/to/fullchain-and-key.pem | base64 -w0)
bao kv put services/turn/kv/keys/public-dns-certificate cert="$CERT_B64"
```

Re-run of `setup_lls.sh` does not overwrite an existing cert (idempotent).

## Usage

Invoked by the migrations framework when the LLS addon is enabled (`ADDONS_LLS_ENABLED=true`). The SIS chart owns invocation via `sis/templates/hook-lls-migrations.yaml`, which sets `CORE_MIGRATIONS_ENABLED=false` + `ADDONS_LLS_ENABLED=true` so the Job runs the LLS addon only.

## Failure semantics

Fail-hard when enabled. If the script aborts, the entrypoint records the failure and exits non-zero — same contract as core migrations. Combined with the SIS Job's `restartPolicy: OnFailure` + `backoffLimit`, transient errors retry but deterministic failures surface as a Job-level failure that blocks the Helm hook. `MIGRATIONS_ALLOW_FAILURES=true` is the explicit operator escape hatch for emergency rollback.

**Note:** prior versions of the entrypoint logged LLS failures without propagating them — a green Job could mask a broken LLS setup. This version closes that gap; any deployment whose `setup_lls.sh` was silently failing will start surfacing the failure on upgrade.
