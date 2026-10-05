# Add a compute plane to an existing control plane

User has a working NVCF control plane (running somewhere) and wants to register a new GPU cluster against it. This is the multi-cluster scaling path: one control plane → N compute planes.

## Prerequisites the user must have

- **kubectl context for the new compute plane** in their `KUBECONFIG`.
- **Public ICMS URL** of the existing control plane (e.g. `https://icms.nvcf.example.com`).
- **Admin JWT** for the control plane's account, OR ability to mint one via `nvcf-cli init` against the control plane's public api endpoint. (Admin tokens come from the API Keys service via the public api gateway — kubectl access to the control plane is NOT required to obtain one.)
- The control plane's profile file, which `nvcf-cli self-hosted control-plane profile export` writes on a machine that can reach the control-plane cluster. It carries the control plane's endpoints and trust for registration. The command writes it to the stack's `out/` directory and prints that path; the steps below expect it copied to `control-plane-profile.yaml` in the working directory.
- A unique `--cluster-name` that doesn't collide with already-registered clusters. Use `nvcf-cli cluster list-registered --nca-id=$NCA_ID --icms-url=$ICMS` to check.

## Steps

1. Ask for inputs:
   - `--cluster-name=<unique>` — must be unique within the control plane's NCA scope
   - `--kube-context=<context>`: the new GPU cluster's kubectl context
   - `--icms-url=<https://icms.nvcf.example.com>` — control plane's public ICMS URL
   - GPU type? (default `H100`, but ask)

2. Pre-flight just the compute plane. `check` takes both context flags or
   neither, so point the current context at the new GPU cluster and select
   only the compute plane:

   ```sh
   kubectl config use-context "$CTX"
   nvcf-cli self-hosted check --compute-plane \
     --icms-url=$ICMS \
     --json 2>&1 >/dev/null | grep '^{' > check.jsonl
   jq -se 'any(.[]; .event == "final" and .success)' check.jsonl
   ```

   This checks this machine's registry credentials and the compute cluster
   only: leftover NVCF namespaces, node inotify limits, SIS reachability at
   `--icms-url`, and the compute-plane cluster-validator (GPU resources, GPU
   Operator, SMB CSI driver) when `cluster_validator_image` is set. `--pre`
   would also run the control-plane checks against this cluster, so leave it
   off. A missing `helm`, `helmfile` or `kubectl` is a warning here, although
   `compute-plane install` needs them. If the gate fails, show the user the
   `check_completed` events with `passed: false` and `severity: "error"`, and
   address them before proceeding.

3. Register the cluster, then install its compute plane from the values
   file registration writes. Both commands name the new cluster's context
   with `--kube-context`:

   ```sh
   nvcf-cli self-hosted compute-plane register \
     --control-plane-profile=control-plane-profile.yaml \
     --cluster-name=$NAME \
     --kube-context=$CTX \
     --icms-url=$ICMS \
     --token=$JWT \
     --output=$NAME-register-values.yaml
   nvcf-cli self-hosted compute-plane install \
     --values=$NAME-register-values.yaml \
     --kube-context=$CTX \
     --cluster-name=$NAME
   ```

   `compute-plane register` records the cluster's OIDC issuer and JWKS with
   ICMS at `--icms-url` and writes the NVCA operator values. `compute-plane
   install` installs the compute-plane stack with them. Neither touches the
   control-plane cluster: ICMS is reached over HTTPS. `--token` passes the
   admin JWT; without it the token `nvcf-cli init` stored is used.

4. Verify. `nvcf-cli self-hosted status --cluster-name=$NAME --json | jq`: expect `verdict: "healthy"`. The Registered Compute Planes panel from ICMS should now list the new cluster.

5. Smoke. [deploy-and-invoke.md](deploy-and-invoke.md): deploy a small function to the new compute plane to confirm scheduling works.

## What if a cluster with the same name already exists?

`compute-plane register` reuses an existing registration: it matches the ICMS row with the same name, reuses its clusterId and replaces its JWKS. Two scenarios:

- **Re-registering the same cluster** (re-running on the same compute plane that was previously registered): expected, no-op semantics.
- **Different physical cluster but same name**: the second attempt will reuse the ICMS row, but the new compute plane's JWKS will be silently *replaced* — the old compute plane's NVCA agent will start failing PSAT auth. **Confirm with the user** that they meant to overwrite.

When in doubt, run `nvcf-cli cluster list-registered --nca-id=$NCA_ID --icms-url=$ICMS` first and ask the user before proceeding with a name that already exists.
