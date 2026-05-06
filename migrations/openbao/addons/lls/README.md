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

Invoked by the migrations framework when the LLS addon is enabled. See the main [README](../README.md) and environment configuration for enabling addons.
