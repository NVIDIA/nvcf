# Rotate a cluster's JWKS

User says NVCA stopped authenticating, or PSAT auth is 401-ing against ICMS. The K8s API server's JWT signing keys may have rotated and the JWKS stored in ICMS is now stale.

## When to rotate

- `kubectl logs -n nvca-system -l app.kubernetes.io/name=nvca` shows `HTTP 401` on `/v1/nvca/clusters/<id>/register` repeatedly
- ICMS logs show `No cluster found with valid JWKS for cluster ID: <id>` (caused by issuer mismatch, not JWKS, but rotate fixes both)
- After a control-plane K8s upgrade or compute-plane K8s upgrade

## Steps

1. Confirm the cluster ID. Run `nvcf-cli cluster list-registered --nca-id=$NCA_ID --icms-url=$ICMS`. Identify the row matching the user's compute plane.

2. **Rotate.** From a context that can reach the compute plane's K8s API (because rotation re-fetches the K8s API's `/openid/v1/jwks`):

   ```sh
   nvcf-cli cluster rotate --cluster-id=<id> --compute-plane-context=<ctx>
   ```

   This re-fetches JWKS from the compute plane's K8s API and PUTs it to ICMS. The cluster ID stays the same.

3. **Verify.** `nvcf-cli cluster get --cluster-id=<id>` — confirm `updatedAt` advanced. Then `kubectl logs -n nvca-system -l app.kubernetes.io/name=nvca --tail=20` — register attempts should succeed (200, not 401).

## What not to do

- **Don't `cluster delete`.** Rotation preserves the cluster ID + group ID; delete creates a new row and you have to redeploy the compute plane with new IDs.
- **Don't manually edit `out/<cluster>-register-values.yaml`.** Rotate updates ICMS only; the helm values file already has the right cluster ID.

## If rotation doesn't fix it

The auth failure may be at a different layer — check:
- NATS auth-callout health (`kubectl logs -n nats-system -l app=nats-auth-callout-service`)
- Cluster row exists in `cluster_oidc_by_cluster_id` Cassandra table
- `helm-reval-service` configmap has `authz.oidc.enabled=true`

Route to [diagnose-failed-install.md](diagnose-failed-install.md) for those checks.
