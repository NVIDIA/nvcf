# Add a compute plane to an existing control plane

User has a working NVCF control plane (running somewhere) and wants to register a new GPU cluster against it. This is the multi-cluster scaling path: one control plane → N compute planes.

## Prerequisites the user must have

- **kubectl context for the new compute plane** in their `KUBECONFIG`.
- **Public ICMS URL** of the existing control plane (e.g. `https://icms.nvcf.example.com`).
- **Admin JWT** for the control plane's account, OR ability to mint one via `nvcf-cli init` against the control plane's public api endpoint. (Admin tokens come from the API Keys service via the public api gateway — kubectl access to the control plane is NOT required to obtain one.)
- A unique `--cluster-name` that doesn't collide with already-registered clusters. Use `nvcf-cli cluster list-registered --nca-id=$NCA_ID --icms-url=$ICMS` to check.

## Steps

1. **Ask for inputs:**
   - `--cluster-name=<unique>` — must be unique within the control plane's NCA scope
   - `--compute-plane-context=<context>` — the new GPU cluster's kubectl context
   - `--icms-url=<https://icms.nvcf.example.com>` — control plane's public ICMS URL
   - GPU type? (default `H100`, but ask)

2. **Pre-flight just the compute plane:**

   ```sh
   nvcf-cli self-hosted check --pre \
     --compute-plane-context=$CTX \
     --icms-url=$ICMS \
     --json | jq -c .
   ```

   This validates the GPU operator is installed, GPU node labels are present, and ICMS is reachable via HTTP. Address any failures before proceeding.

3. **Run `add-compute-plane`:**

   ```sh
   nvcf-cli self-hosted add-compute-plane \
     --cluster-name=$NAME \
     --compute-plane-context=$CTX \
     --icms-url=$ICMS \
     --token=$JWT \
     --non-interactive \
     --json
   ```

   This is a separate subcommand from `up`. It runs only the compute-plane-relevant phases: 1 (compute-plane preflight), 5 (register), 6 (compute-plane apply), 8 (final health on the new compute plane). It does NOT touch the control plane and does NOT accept `--control-plane-context` — the control plane is reached over HTTPS at `--icms-url`. `--token` is required because there's no kubectl path to mint one from.

4. **Verify.** `nvcf-cli self-hosted status --cluster-name=$NAME --json | jq` — expect `verdict: "healthy"`. The Registered Compute Planes panel from ICMS should now list the new cluster.

5. **Smoke.** [deploy-and-invoke.md](deploy-and-invoke.md) — deploy a small function to the new compute plane to confirm scheduling works.

## What if a cluster with the same name already exists?

`add-compute-plane`'s register phase uses `--ignore-existing` semantics — it'll match the existing ICMS row and reuse its clusterId. Two scenarios:

- **Re-registering the same cluster** (re-running on the same compute plane that was previously registered): expected, no-op semantics.
- **Different physical cluster but same name**: the second attempt will reuse the ICMS row, but the new compute plane's JWKS will be silently *replaced* — the old compute plane's NVCA agent will start failing PSAT auth. **Confirm with the user** that they meant to overwrite.

When in doubt, run `nvcf-cli cluster list-registered --nca-id=$NCA_ID --icms-url=$ICMS` first and ask the user before proceeding with a name that already exists.
