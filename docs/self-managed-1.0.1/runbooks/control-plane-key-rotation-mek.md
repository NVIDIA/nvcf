# MEK (Master Encryption Key) Rotation

## Overview

The Master Encryption Key (MEK) is an AES-256-GCM key stored in OpenBao that
wraps Namespace Encryption Keys (NEKs). ESS uses NEKs to encrypt user secrets.
The same OpenBao record also contains a separate payload-key JWKS used by API
Keys. The MEK and payload key share a key ID, but their key material must remain
different.

Rotate the MEK on the schedule required by your security policy. Use a
maintenance window because key propagation can briefly affect availability.

## Prerequisites

- `kubectl` access to the NVCF control plane cluster
- Permission to exec into OpenBao pods in the `vault-system` namespace
- `python3` for key generation and JSON processing
- Approved encrypted storage for the backup and temporary payload files
- A documented retention and secure-deletion policy for key backups

## Stored Fields

The record is in the `services/all/kv/` KV v2 secret engine at
`encryption/keys/stored_data`.

| Field | Description |
| --- | --- |
| `keys` | Base64-encoded JWKS containing MEKs. |
| `current_kid` | Key ID of the active MEK. It matches the first entry in `keys`. |
| `jwe_mapping` | JSON mapping whose `payload_jwe_kid` selects the active payload key. |
| `private_jwks` | Base64-encoded JWKS containing payload keys. These keys are independent from the MEKs even though they use the same key IDs. |

Do not remove an older entry from either key set until every dependent value
has been re-encrypted and the supported key-retirement procedure confirms that
the entry is no longer needed.

## Prepare Protected Storage and Helpers

Set `MEK_WORKDIR` to an approved encrypted location. The directory and files in
it contain recoverable encryption key material.

```bash
umask 077
export MEK_WORKDIR="<absolute-path-on-approved-encrypted-storage>"
install -d -m 700 "$MEK_WORKDIR"

export MEK_BACKUP="$MEK_WORKDIR/mek_backup.json"
export MEK_PAYLOAD="$MEK_WORKDIR/mek_updated.json"
export MEK_METADATA="$MEK_WORKDIR/mek_metadata.json"
export MEK_VERIFY="$MEK_WORKDIR/mek_verify.json"
```

The OpenBao pod already mounts its root token. These helpers read the token
inside the pod, so the token and write payload do not appear in local or remote
process arguments.

```bash
bao_root() {
  kubectl exec -i openbao-server-0 -c openbao -n vault-system -- \
    sh -c 'export BAO_TOKEN="$(cat /home/openbao/unseal/root_token)"; exec bao "$@"' \
    sh "$@"
}

bao_put_stored_data() {
  local payload_file="$1"
  kubectl exec -i openbao-server-0 -c openbao -n vault-system -- \
    sh -c 'export BAO_TOKEN="$(cat /home/openbao/unseal/root_token)"; \
      exec bao kv put services/all/kv/encryption/keys/stored_data -' \
    < "$payload_file"
}
```

## Rotation Procedure

1. Back up the current record with restrictive permissions:

   ```bash
   bao_root kv get -format=json \
     services/all/kv/encryption/keys/stored_data > "$MEK_BACKUP"
   chmod 600 "$MEK_BACKUP"
   ```

   Keep this backup only in the approved encrypted location. Do not copy it to
   chat, tickets, source control, or an unencrypted workstation directory.

2. Generate a new MEK and a separate payload key. The keys use the same new
   key ID and retain every existing key needed for decryption:

   ```bash
   python3 - "$MEK_BACKUP" "$MEK_PAYLOAD" "$MEK_METADATA" <<'PY'
   import base64
   import json
   import secrets
   import sys
   import uuid

   backup_path, payload_path, metadata_path = sys.argv[1:]
   with open(backup_path, encoding="utf-8") as stream:
       stored = json.load(stream)["data"]["data"]

   def decode_jwks(field):
       return json.loads(base64.b64decode(stored[field]).decode("utf-8"))

   def encode_jwks(value):
       raw = json.dumps(value, separators=(",", ":")).encode("utf-8")
       return base64.b64encode(raw).decode("ascii")

   def new_key(kid):
       material = base64.urlsafe_b64encode(secrets.token_bytes(32))
       return {
           "kty": "oct",
           "use": "enc",
           "kid": kid,
           "k": material.rstrip(b"=").decode("ascii"),
           "alg": "A256GCM",
       }

   mek_jwks = decode_jwks("keys")
   payload_jwks = decode_jwks("private_jwks")
   new_kid = str(uuid.uuid4())
   new_mek = new_key(new_kid)
   new_payload_key = new_key(new_kid)
   if new_mek["k"] == new_payload_key["k"]:
       raise RuntimeError("generated key material unexpectedly matches")

   mek_jwks["keys"].insert(0, new_mek)
   payload_jwks["keys"].insert(0, new_payload_key)
   updated = {
       "keys": encode_jwks(mek_jwks),
       "current_kid": new_kid,
       "jwe_mapping": json.dumps(
           {"payload_jwe_kid": new_kid}, separators=(",", ":")
       ),
       "private_jwks": encode_jwks(payload_jwks),
   }

   with open(payload_path, "w", encoding="utf-8") as stream:
       json.dump(updated, stream, separators=(",", ":"))
   with open(metadata_path, "w", encoding="utf-8") as stream:
       json.dump(
           {"new_kid": new_kid, "old_kid": stored["current_kid"]}, stream
       )
   print(f"Prepared independent MEK and payload keys for kid {new_kid}")
   PY

   chmod 600 "$MEK_PAYLOAD" "$MEK_METADATA"
   ```

3. Write the complete updated record to OpenBao through standard input:

   ```bash
   bao_put_stored_data "$MEK_PAYLOAD"
   ```

4. Verify the active selectors, retained old keys, and distinct new key
   material without printing key values:

   ```bash
   bao_root kv get -format=json \
     services/all/kv/encryption/keys/stored_data > "$MEK_VERIFY"
   chmod 600 "$MEK_VERIFY"

   python3 - "$MEK_VERIFY" "$MEK_METADATA" <<'PY'
   import base64
   import json
   import sys

   verify_path, metadata_path = sys.argv[1:]
   with open(verify_path, encoding="utf-8") as stream:
       stored = json.load(stream)["data"]["data"]
   with open(metadata_path, encoding="utf-8") as stream:
       metadata = json.load(stream)

   def keys(field):
       value = json.loads(base64.b64decode(stored[field]).decode("utf-8"))
       return value["keys"]

   mek_keys = keys("keys")
   payload_keys = keys("private_jwks")
   new_kid = metadata["new_kid"]
   old_kid = metadata["old_kid"]
   mapping = json.loads(stored["jwe_mapping"])

   assert stored["current_kid"] == new_kid
   assert mapping["payload_jwe_kid"] == new_kid
   assert mek_keys[0]["kid"] == new_kid
   assert payload_keys[0]["kid"] == new_kid
   assert mek_keys[0]["k"] != payload_keys[0]["k"]
   assert any(key["kid"] == old_kid for key in mek_keys)
   assert any(key["kid"] == old_kid for key in payload_keys)
   print(f"Verified rotation metadata for kid {new_kid}")
   PY

   rm -f "$MEK_PAYLOAD" "$MEK_VERIFY"
   ```

5. Verify service health and read and write a test secret through the NVCF API:

   ```bash
   kubectl get pods -n ess
   kubectl get pods -n api-keys
   kubectl logs -n ess -l app.kubernetes.io/name=helm-nvcf-ess-api \
     -c helm-nvcf-ess-api --tail=200 | grep -i error
   ```

## Propagation Grace Period

ESS does not start using the new MEK immediately. Each ESS pod refreshes its
OpenBao material through the vault-agent sidecar roughly every 24 hours.
Different pods can refresh at different times, so allow the default 48-hour
grace period before treating the new key as fully active.

Keep both old and new entries in `keys` and `private_jwks` throughout the grace
period and afterward. The old entries remain necessary to decrypt values
written before the rotation. The new entries may become necessary as soon as
any consumer refreshes, so rollback must preserve them.

## Rollback

Rollback changes the active selectors back to the previous key ID but retains
all currently published MEK and payload-key entries. Never restore the backup
as a complete replacement after any service might have used the new key.

1. Read the current record and build a rollback payload that moves the previous
   keys to the front without removing the new keys:

   ```bash
   export MEK_CURRENT="$MEK_WORKDIR/mek_current.json"
   export MEK_ROLLBACK="$MEK_WORKDIR/mek_rollback.json"

   bao_root kv get -format=json \
     services/all/kv/encryption/keys/stored_data > "$MEK_CURRENT"
   chmod 600 "$MEK_CURRENT"

   python3 - "$MEK_CURRENT" "$MEK_BACKUP" "$MEK_ROLLBACK" <<'PY'
   import base64
   import json
   import sys

   current_path, backup_path, rollback_path = sys.argv[1:]
   with open(current_path, encoding="utf-8") as stream:
       current = json.load(stream)["data"]["data"]
   with open(backup_path, encoding="utf-8") as stream:
       backup = json.load(stream)["data"]["data"]

   old_kid = backup["current_kid"]

   def reorder(field):
       jwks = json.loads(base64.b64decode(current[field]).decode("utf-8"))
       matching = [key for key in jwks["keys"] if key["kid"] == old_kid]
       if not matching:
           raise RuntimeError(f"previous kid {old_kid} is missing from {field}")
       remaining = [key for key in jwks["keys"] if key["kid"] != old_kid]
       jwks["keys"] = matching + remaining
       raw = json.dumps(jwks, separators=(",", ":")).encode("utf-8")
       return base64.b64encode(raw).decode("ascii")

   rollback = {
       "keys": reorder("keys"),
       "current_kid": old_kid,
       "jwe_mapping": backup["jwe_mapping"],
       "private_jwks": reorder("private_jwks"),
   }
   with open(rollback_path, "w", encoding="utf-8") as stream:
       json.dump(rollback, stream, separators=(",", ":"))
   print(f"Prepared rollback selectors for kid {old_kid}")
   PY

   chmod 600 "$MEK_ROLLBACK"
   bao_put_stored_data "$MEK_ROLLBACK"
   ```

2. Restart both consumers so they load the rollback selectors:

   ```bash
   kubectl rollout restart \
     deployment/ess-api-helm-nvcf-ess-api-deployment -n ess
   kubectl rollout restart deployment/api-keys -n api-keys
   kubectl rollout status \
     deployment/ess-api-helm-nvcf-ess-api-deployment -n ess
   kubectl rollout status deployment/api-keys -n api-keys
   ```

3. Verify existing secrets and API keys, then test new writes. Retain the new
   entries until a supported re-encryption and key-retirement procedure confirms
   that no data depends on them.

4. Remove temporary rollback files from the encrypted workspace:

   ```bash
   rm -f "$MEK_CURRENT" "$MEK_ROLLBACK"
   ```

Retain `mek_backup.json` and `mek_metadata.json` only for the approved rollback
window. At the end of that window, securely delete the files and workspace
according to your encrypted-storage and media-sanitization policy.
