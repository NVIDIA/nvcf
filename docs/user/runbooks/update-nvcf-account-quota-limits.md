# Update NVCF Account Quota Limits

This runbook updates the account quota limits for the `nvcf-default` account
post installation. Use this when an operation fails with an error like:

```json
{
  "detail": "Account 'nvcf-default': Reached or exceeded the max limit for the number of functions allowed: 10."
}
```

The following limits can be updated using the same method:

| Field | Description | Max |
|-------|-------------|-----|
| `maxFunctionsAllowed` | Maximum number of functions | unlimited |
| `maxTasksAllowed` | Maximum number of tasks | unlimited |
| `maxTelemetriesAllowed` | Maximum number of telemetries | 50 |
| `maxRegistryCredentialsAllowed` | Maximum number of registry credentials | 50 |

## Prerequisites

To use `nvcf-cli` (the simpler path), you need:

- `nvcf-cli` installed and authenticated against the target cluster

To use `curl` directly, you need:

- `kubectl`
- `curl`
- `jq`
- Cluster access with permission to create service account tokens
- Access to the `nvcf` and `vault-system` namespaces

The NVCF API and OpenBao services must be reachable through `kubectl port-forward`.

## Update Runtime Account Limits Using nvcf-cli

If `nvcf-cli` is available and configured, use the `admin accounts update`
subcommand:

```bash
nvcf-cli admin accounts update --nca-id nvcf-default --max-functions 50
```

Sibling flags for other limits:

| Flag | Limit |
|------|-------|
| `--max-functions` | Maximum number of functions |
| `--max-tasks` | Maximum number of tasks |
| `--max-telemetries` | Maximum number of telemetries (cap 50) |
| `--max-registry-credentials` | Maximum number of registry credentials (cap 50) |

Run `nvcf-cli admin accounts update --help` for the full flag list.

Retry the operation that previously failed.

## Update Runtime Account Limits Using curl

Port-forward the NVCF API:

```bash
kubectl -n nvcf port-forward svc/api 18080:8080
```

In another terminal, port-forward OpenBao:

```bash
kubectl -n vault-system port-forward svc/openbao-server 18200:8200
```

Create an audience-bound service account token for the bootstrap service account:

```bash
SA_TOKEN="$(kubectl -n nvcf create token nvcf-api-account-bootstrap \
  --audience=http://openbao-server.vault-system.svc.cluster.local:8200 \
  --duration=3600s)"
```

Log in to OpenBao using that service account token:

```bash
VAULT_TOKEN="$(curl -s \
  -X POST \
  -H "Content-Type: application/json" \
  -d "{\"role\":\"nvcf-api-account-bootstrap\",\"jwt\":\"${SA_TOKEN}\"}" \
  "http://127.0.0.1:18200/v1/auth/jwt/login" \
  | jq -r '.auth.client_token')"
```

Generate an NVCF admin JWT using the OpenBao signing role:

```bash
ADMIN_TOKEN="$(curl -s \
  -X PUT \
  -H "X-Vault-Token: ${VAULT_TOKEN}" \
  -d '{}' \
  "http://127.0.0.1:18200/v1/services/nvcf-api/jwt/sign/nvcf-api-admin" \
  | jq -r '.data.token')"
```

Patch one or more account limits in a single request. Include only the fields
you want to change:

```bash
curl -i -w "\nHTTP_STATUS=%{http_code}\n" \
  -X PATCH \
  "http://127.0.0.1:18080/v2/nvcf/accounts/nvcf-default" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  --data '{"maxFunctionsAllowed":50}'
```

To update multiple limits at once:

```bash
curl -i -w "\nHTTP_STATUS=%{http_code}\n" \
  -X PATCH \
  "http://127.0.0.1:18080/v2/nvcf/accounts/nvcf-default" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  --data '{"maxFunctionsAllowed":50,"maxTasksAllowed":50,"maxTelemetriesAllowed":20}'
```

Expected success response:

```json
{
  "account": {
    "ncaId": "nvcf-default",
    "adminClientIds": [
      "ncp"
    ],
    "name": "nvcf-default",
    "maxFunctionsAllowed": 50,
    "maxTasksAllowed": 50,
    "maxTelemetriesAllowed": 20,
    "maxRegistryCredentialsAllowed": 10
  }
}
```

Verify the updated limits:

```bash
curl -s \
  "http://127.0.0.1:18080/v2/nvcf/accounts/nvcf-default" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  | jq .
```

Retry the operation that previously failed after confirming the new limits are reflected.

## Persist Future Bootstrap Limits

The runtime fix is the `PATCH` above. To prevent future installs or account
bootstrap runs from using the old defaults, also update the Helm values:

```yaml
api:
  accountBootstrap:
    limits:
      maxFunctions: 50
      maxTasks: 50
      maxTelemetries: 20
      maxRegistryCredentials: 10
```

If upgrading the existing Helm release directly, use the same chart source that
was used for installation:

```bash
helm upgrade -n nvcf api <same-helm-nvcf-api-chart> \
  --reuse-values \
  --set api.accountBootstrap.limits.maxFunctions=50
```

Note: the account bootstrap job creates the account and treats an existing
account as success. Updating Helm values alone may not change an already-created
`nvcf-default` account, so apply the runtime `PATCH` when changing an existing
deployment.

## Troubleshooting

If the account patch returns `401`, the JWT is expired. Regenerate `ADMIN_TOKEN`.

If the account patch returns `403`, the token is valid but does not have enough
privilege. Use the OpenBao-signed `nvcf-api-admin` token, not a normal function
management token.

If `VAULT_TOKEN` is `null`, confirm `SA_TOKEN` was created in the same terminal:

```bash
echo "SA_TOKEN length: ${#SA_TOKEN}"
```

If the length is `0`, recreate `SA_TOKEN` and retry the OpenBao login.
