# nvcf-ui OpenBao Addon

This addon provisions OpenBao JWT signing access for the optional `nvcf-ui`
service. It is off by default and runs only when the owning UI chart or stack
config opts in. Core installs that do not deploy the UI never create these
credential-minting paths.

## What it does

Creates, for the `nvcf-ui` ServiceAccount in the `nvcf-ui` namespace:

- JWT secret sign role and read/write policy on the SIS API mount
  (`services/sis-api/jwt`)
- JWT secret sign role and read/write policy on the NVCF API mount
  (`services/nvcf-api/jwt`)
- The NVCT JWT secret engine (`services/nvct-api/jwt`) plus a sign role and
  read/write policy. This mount exists only to let the UI mint NVCT tokens, so
  the addon owns it rather than core migration `20_setup_nvct.sh`.
- JWT auth role `nvcf-ui` bound to the `nvcf-ui` namespace and service account,
  attached to the three sign policies above

## Usage

The owning UI chart runs the `nvcf-openbao-migrations` image in a Helm hook Job
with two independent environment variables:

- `ADDONS_NVCF_UI_ENABLED=true` opts this invocation into running the `nvcf-ui`
  addon.
- `CORE_MIGRATIONS_ENABLED=false` disables the numbered core migrations for this
  invocation. It does not enable any addon; it only prevents the entrypoint from
  replaying `migrations/*.sh`.

Together they mean: skip the core migrations, then run the opted-in `nvcf-ui`
addon, so the Job provisions nvcf-ui auth only. This mirrors the LLS and LLM
addon wiring.

## Failure semantics

Fail-hard when enabled. If the script aborts, the entrypoint records the failure
and exits non-zero, the same contract as core migrations. Combined with the
owning Job's `restartPolicy: OnFailure` + `backoffLimit`, transient errors retry
but deterministic failures surface as a Job-level failure that blocks the Helm
hook. `MIGRATIONS_ALLOW_FAILURES=true` is the explicit operator escape hatch for
emergency rollback.
