# MEK (Master Encryption Key) Rotation

## Overview

The **Master Encryption Key (MEK)** is an AES-256-GCM key stored in OpenBAO that wraps all Namespace Encryption Keys (NEKs). The MEK is shared across NVCF services in the control plane -- ESS is its primary consumer.

Rotate the MEK on a regular schedule (for example, every 90 days) or when required by your security policy.

## Prerequisites

- `kubectl` configured for your NVCF control plane cluster
- Access to OpenBAO pods in the `vault-system` namespace
- The OpenBAO root token (stored in the `openbao-server-root-token` secret in `vault-system`)
- A tool to generate UUIDs (`uuidgen` or equivalent)
- `base64`, `python3`, or `jq` for JSON manipulation
- A maintenance window; MEK rotation can briefly affect availability

## Where the MEK is Stored

The MEK is stored in the `services/all/kv/` KV v2 secret engine in OpenBAO, at the path:

`encryption/keys/stored_data`

This path contains four fields:

| Field | Description |
| --- | --- |
| `keys` | Base64-encoded JSON object containing an array of MEK keys. Each key is a JWK with fields: `kty` (`oct`), `use` (`enc`), `kid` (UUID), `k` (base64url-encoded 256-bit key), `alg` (`A256GCM`). |
| `current_kid` | The `kid` of the active MEK used for encryption. Must match the first key in the `keys` array. |
| `jwe_mapping` | JSON string mapping the active key ID, e.g. `{"payload_jwe_kid":"<kid>"}`. |
| `private_jwks` | A separate key set used by other NVCF services. Update this field alongside `keys` during rotation. |

## Inspecting Current State

**Retrieve the OpenBAO root token:**

```bash
VAULT_TOKEN=$(kubectl get secret -n vault-system openbao-server-root-token \
  -o jsonpath='{.data.root_token}' | base64 -d)
```

**Read the current MEK data:**

```bash
kubectl exec -n vault-system openbao-server-0 -c openbao -- \
  sh -c "VAULT_TOKEN=$VAULT_TOKEN bao kv get \
    services/all/kv/encryption/keys/stored_data"
```

**Decode the keys array to see the current MEK(s):**

```bash
kubectl exec -n vault-system openbao-server-0 -c openbao -- \
  sh -c "VAULT_TOKEN=$VAULT_TOKEN bao kv get \
    -field=keys services/all/kv/encryption/keys/stored_data" \
  | base64 -d | python3 -m json.tool
```

You should see output like:

```json
{
    "keys": [
        {
            "kty": "oct",
            "use": "enc",
            "kid": "fa85bab2-fccd-11f0-b875-82c3b2df389e",
            "k": "<base64url-encoded-256-bit-key>",
            "alg": "A256GCM"
        }
    ]
}
```

## MEK Rotation Procedure

<Info>
Do not remove the old MEK from the keys array. ESS needs the old MEK to decrypt existing NEKs during the transition.

</Info>

1. **Read the current stored_data** and save it as a backup:

   ```bash
   VAULT_TOKEN=$(kubectl get secret -n vault-system openbao-server-root-token \
     -o jsonpath='{.data.root_token}' | base64 -d)

   kubectl exec -n vault-system openbao-server-0 -c openbao -- \
     sh -c "VAULT_TOKEN=$VAULT_TOKEN bao kv get -format=json \
       services/all/kv/encryption/keys/stored_data" > mek_backup.json
   ```

2. **Generate a new MEK key ID and key material:**

   ```bash
   NEW_KID=$(python3 -c "from uuid import uuid1; kid = str(uuid1()); print(kid)")
   NEW_KEY=$(python3 -c "import secrets, base64; \
     key = secrets.token_bytes(32); \
     print(base64.urlsafe_b64encode(key).rstrip(b'=').decode())")
   echo "New kid: $NEW_KID"
   echo "New key: $NEW_KEY"
   ```

3. **Build the updated keys JSON** with the new key as the first element:

   ```bash
   # Extract current keys
   CURRENT_KEYS_B64=$(kubectl exec -n vault-system openbao-server-0 -c openbao -- \
     sh -c "VAULT_TOKEN=$VAULT_TOKEN bao kv get \
       -field=keys services/all/kv/encryption/keys/stored_data")

   # Build new keys array (new key first, then existing keys)
   UPDATED_KEYS_B64=$(echo "$CURRENT_KEYS_B64" | base64 -d | python3 -c "
   import sys, json, base64
   data = json.load(sys.stdin)
   new_key = {
       'kty': 'oct',
       'use': 'enc',
       'kid': '$NEW_KID',
       'k': '$NEW_KEY',
       'alg': 'A256GCM'
   }
   data['keys'].insert(0, new_key)
   print(base64.b64encode(json.dumps(data).encode()).decode())
   ")
   ```

4. **Write the updated values back to OpenBAO:**

   ```bash
   NEW_JWE_MAPPING="{\"payload_jwe_kid\":\"$NEW_KID\"}"

   kubectl exec -n vault-system openbao-server-0 -c openbao -- \
     sh -c "VAULT_TOKEN=$VAULT_TOKEN bao kv put \
       services/all/kv/encryption/keys/stored_data \
       keys='$UPDATED_KEYS_B64' \
       current_kid='$NEW_KID' \
       jwe_mapping='$NEW_JWE_MAPPING' \
       private_jwks='$UPDATED_KEYS_B64'"
   ```

5. **Verify the update:**

   ```bash
   kubectl exec -n vault-system openbao-server-0 -c openbao -- \
     sh -c "VAULT_TOKEN=$VAULT_TOKEN bao kv get \
       -field=current_kid services/all/kv/encryption/keys/stored_data"
   ```

   The output should show your new key ID (`$NEW_KID`).

## Verification

After completing MEK rotation:

- OpenBAO shows the updated `current_kid`:

  ```bash
  kubectl exec -n vault-system openbao-server-0 -c openbao -- \
    sh -c "VAULT_TOKEN=$VAULT_TOKEN bao kv get \
      -field=current_kid services/all/kv/encryption/keys/stored_data"
  ```

- ESS pods are `Running` and `Ready` with no MEK/decryption errors:

  ```bash
  kubectl get pods -n ess
  kubectl logs -n ess -l app.kubernetes.io/name=helm-nvcf-ess-api \
    -c helm-nvcf-ess-api --tail=200 | grep -i error
  ```

- Secrets can still be read and written through the NVCF API.

## MEK Propagation Grace Period

After writing the new MEK to OpenBAO, ESS does **not** start using it immediately. Each ESS pod refreshes its secrets from OpenBAO via the `vault-agent` sidecar container roughly every **24 hours**. Because different pods refresh at different times, there is a default grace period of **48 hours** before the new MEK is actively used for encryption.

This grace period prevents a race condition: if pod A picks up the new MEK and uses it to write encrypted data to the database before pod B has refreshed, pod B would be unable to decrypt that data because it still only has the old MEK loaded.

<Info>
- Do **not** remove the old MEK from the `keys` array as ESS needs the old MEK to decrypt existing NEKs during the transition.
- During the grace period the old MEK remains the active encryption key; the new MEK is present in the key set but not yet used for writes.
- After the grace period, ESS switches to the new MEK for all new encryption operations while retaining the old MEK for decrypting previously encrypted data.

</Info>

## Rollback

If the rotation causes issues:

1. Restore the previous `keys`, `current_kid`, `jwe_mapping`, and `private_jwks` from the backup (`mek_backup.json`) taken in Step 1:

   ```bash
   # Extract original values from backup
   ORIG_KEYS=$(python3 -c "import json; d=json.load(open('mek_backup.json')); print(d['data']['data']['keys'])")
   ORIG_KID=$(python3 -c "import json; d=json.load(open('mek_backup.json')); print(d['data']['data']['current_kid'])")
   ORIG_JWE=$(python3 -c "import json; d=json.load(open('mek_backup.json')); print(d['data']['data']['jwe_mapping'])")
   ORIG_PRIV=$(python3 -c "import json; d=json.load(open('mek_backup.json')); print(d['data']['data']['private_jwks'])")

   kubectl exec -n vault-system openbao-server-0 -c openbao -- \
     sh -c "VAULT_TOKEN=$VAULT_TOKEN bao kv put \
       services/all/kv/encryption/keys/stored_data \
       keys='$ORIG_KEYS' \
       current_kid='$ORIG_KID' \
       jwe_mapping='$ORIG_JWE' \
       private_jwks='$ORIG_PRIV'"
   ```

2. Restart ESS to pick up the restored MEK:

   ```bash
   kubectl rollout restart deployment -n ess ess-api-helm-nvcf-ess-api-deployment
   ```

3. Do not remove the old key from the keys array until the new key has been verified and ESS is healthy.
