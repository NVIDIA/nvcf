# One-Click Local CLI Bring-Up Test Plan

> For agentic workers: required sub-skill: use `superpowers:executing-plans` to execute this plan task by task. Steps use checkbox syntax for tracking.

Goal: Validate the `nvcf-cli self-hosted up` one-click bring-up path on a fresh single-cluster local k3d environment.

Architecture: The test uses one k3d cluster named `ncp-local` for both control plane and compute plane. The CLI is built from the rebased MR branch, the self-managed stack is used from the same worktree through `--stack`, and all evidence is written to a timestamped artifact directory. The test records every failure or deviation in an issue log while it runs.

Tech stack: `nvcf-cli`, Go, k3d, kubectl, Helm, Helmfile, Docker, NGC registry credentials, self-managed stack `HELMFILE_ENV=local`.

---

## Scope

This plan tests only the single-cluster local k3d path.

Use these fixed values unless a reviewer explicitly asks for a rerun:

```bash
export CLUSTER_NAME=ncp-local
export KUBE_CONTEXT=k3d-ncp-local
export NCA_ID=nvcf-default
export REGION=us-west-1
export SIS_URL=http://sis.localhost:8080
export HELMFILE_ENV=local
```

Do not test split-cluster or multi-cluster bring-up in this run. Do not reuse a remote or ELB CLI config.

## Known Issues And Deferred Follow-Up

Keep these items in the issue log if they appear. Do not treat them as new regressions unless the observed behavior is worse than described here.

| Issue | Status for this plan | Expected handling |
| --- | --- | --- |
| 1. Host Helm 4 preflight fallback | Partially fixed by preferring stack-local `bin/` tools when a local `--stack` is supplied. | This plan runs `ensure-binaries`, passes `--stack`, and records which Helm and Helmfile paths preflight used. A failure without stack-local binaries is a known residual gap. |
| 2. Local SIS URL persistence | Partially fixed. `check --pre` should not probe SIS, but post-install commands still need the local ICMS/SIS URL. | This plan passes `--icms-url="${SIS_URL}"` to `up`, `status`, and `check --all`. Config persistence is deferred. |
| 5. NGC credential scope detection | Partially fixed by docs and clearer template comments. | This plan records which credential variables were used and logs any registry authorization failure as a credential-scope issue unless code behavior is clearly wrong. |
| 7. Cassandra hook timeout recovery | Known issue deferred to a follow-up fix and test. | If Cassandra init hooks fail, migrations are skipped, or services crash on missing keyspaces, stop after collecting evidence. Do not attempt to prove recovery in this plan. |
| 8. Account bootstrap credential validation | Partially fixed by validating base64 `username:password` format. | This plan tests format validation and records live registry validation failures separately. Automatic credential encoding and live scope probing are deferred. |
| 10. `status` unknown verdict | Partially fixed by surfacing a `SIS Cluster List` diagnostic row. | This plan accepts `verdict=unknown` only when the diagnostic row explains the SIS-list failure and `check --all` plus Kubernetes backend health still pass. |

## Issue Log Protocol

Every command that fails, times out, needs a workaround, or produces an unexpected warning must add an entry to the issue log before continuing.

Initialize the log at the start:

```bash
export ARTIFACT_DIR=".test-artifacts/one-click-cli-bringup-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "${ARTIFACT_DIR}"
cat > "${ARTIFACT_DIR}/issue-log.md" <<'EOF'
# One-Click Local CLI Bring-Up Issue Log

| Time UTC | Step | Symptom | Command | Evidence | Classification | Follow-up |
| --- | --- | --- | --- | --- | --- | --- |
EOF
```

Use these classifications:

| Classification | Meaning |
| --- | --- |
| fixed-by-mr | Behavior matches a fix in MR !128. |
| known-deferred | Behavior matches a known issue listed above. |
| environment | Host, credential, Docker, k3d, or local state problem. |
| new-regression | Behavior contradicts the expected MR fix. |
| docs-gap | Behavior works but docs or command guidance is missing or confusing. |
| needs-follow-up | Needs owner review before classification. |

Append entries with this shape:

```bash
printf '| %s | %s | %s | `%s` | `%s` | %s | %s |\n' \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  "step-name" \
  "short symptom" \
  "command summary" \
  "artifact path" \
  "classification" \
  "next action" >> "${ARTIFACT_DIR}/issue-log.md"
```

## Task 1: Prepare The Rebased Branch And Artifact Directory

Files:

- Read: `AGENTS.md`
- Read: `docs/AGENTS.md`
- Read: `examples/self-hosted-local-development/README.md`
- Read: `docs/user/quickstart.md`
- Write runtime artifacts only under `.test-artifacts/one-click-cli-bringup-<timestamp>/`

- [ ] Step 1: Confirm the checkout is the rebased MR branch.

Run:

```bash
git status --short --branch
git rev-parse HEAD
git merge-base HEAD origin/main
git log --oneline --decorate --max-count=8
```

Expected:

- The branch is ahead of `origin/main` with the MR commits.
- There are no uncommitted source changes before the test starts.
- The merge base is current `origin/main`.

- [ ] Step 2: Create the artifact directory and issue log.

Run:

```bash
export ARTIFACT_DIR=".test-artifacts/one-click-cli-bringup-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "${ARTIFACT_DIR}"
cat > "${ARTIFACT_DIR}/issue-log.md" <<'EOF'
# One-Click Local CLI Bring-Up Issue Log

| Time UTC | Step | Symptom | Command | Evidence | Classification | Follow-up |
| --- | --- | --- | --- | --- | --- | --- |
EOF
```

Expected: `${ARTIFACT_DIR}/issue-log.md` exists.

- [ ] Step 3: Snapshot branch metadata.

Run:

```bash
{
  date -u
  git status --short --branch
  git rev-parse HEAD
  git rev-parse origin/main
  git log --oneline --decorate --max-count=12
} > "${ARTIFACT_DIR}/branch-snapshot.txt" 2>&1
```

Expected: `branch-snapshot.txt` identifies the exact commit under test.

## Task 2: Inventory Local State Before Any Cleanup

Files:

- Write runtime artifacts only under `${ARTIFACT_DIR}`

- [ ] Step 1: Record host tools and existing local state.

Run:

```bash
{
  date -u
  docker --version
  k3d version
  kubectl version --client
  helm version
  helmfile --version
  kubectl config current-context || true
  k3d cluster list || true
  docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}' || true
  helm list -A || true
  kubectl get nodes -o wide || true
  kubectl get pods -A -o wide || true
} > "${ARTIFACT_DIR}/pre-cleanup-inventory.txt" 2>&1
```

Expected: Inventory completes even if no cluster exists.

- [ ] Step 2: Ask for cleanup approval if `ncp-local` already exists.

Run:

```bash
k3d cluster get ncp-local > "${ARTIFACT_DIR}/existing-ncp-local.txt" 2>&1
```

Expected:

- If the command fails because the cluster does not exist, continue.
- If the command succeeds, stop and ask the user before deleting or reusing it.
- If cleanup is approved, run `examples/self-hosted-local-development/teardown.sh` or `k3d cluster delete ncp-local`, then append the cleanup decision to the issue log with classification `environment`.

## Task 3: Build The CLI And Prepare Stack-Pinned Tools

Files:

- Read: `src/clis/nvcf-cli/AGENTS.md` if present
- Modify runtime-only: `src/clis/nvcf-cli/nvcf-cli`
- Modify runtime-only: `deploy/stacks/self-managed/bin/`

- [ ] Step 1: Build the CLI from the rebased branch.

Run:

```bash
(cd src/clis/nvcf-cli && go build -o ./nvcf-cli .) \
  > "${ARTIFACT_DIR}/cli-build.log" 2>&1
```

Expected: command exits `0` and `src/clis/nvcf-cli/nvcf-cli` exists.

- [ ] Step 2: Install stack-pinned Helm, Helmfile, and helm-diff binaries.

Run:

```bash
make -C deploy/stacks/self-managed ensure-binaries \
  > "${ARTIFACT_DIR}/ensure-binaries.log" 2>&1
```

Expected: command exits `0`.

- [ ] Step 3: Record the exact binaries that will be used.

Run:

```bash
{
  deploy/stacks/self-managed/bin/helm version
  deploy/stacks/self-managed/bin/helmfile version
  PATH="$(pwd)/deploy/stacks/self-managed/bin:${PATH}" which helm
  PATH="$(pwd)/deploy/stacks/self-managed/bin:${PATH}" which helmfile
} > "${ARTIFACT_DIR}/stack-pinned-tools.txt" 2>&1
```

Expected:

- Helm is stack-pinned `v3.x`.
- Helmfile is stack-pinned `1.1.x`.
- The paths resolve under `deploy/stacks/self-managed/bin`.

## Task 4: Bootstrap One Fresh k3d Cluster

Files:

- Read: `examples/self-hosted-local-development/setup.sh`
- Write runtime state: local k3d cluster `ncp-local`

- [ ] Step 1: Create the single local k3d cluster.

Run:

```bash
examples/self-hosted-local-development/setup.sh \
  > "${ARTIFACT_DIR}/cluster-setup.log" 2>&1
```

Expected:

- The command exits `0`.
- The active context is `k3d-ncp-local`.
- Fake GPU operator, CSI SMB, and Gateway prerequisites are installed.

- [ ] Step 2: Snapshot the fresh cluster.

Run:

```bash
{
  kubectl config current-context
  kubectl get nodes -o wide
  kubectl get storageclass
  kubectl get gatewayclass -A || true
  kubectl get httproute -A || true
  kubectl get pods -A -o wide
} > "${ARTIFACT_DIR}/fresh-cluster-snapshot.txt" 2>&1
```

Expected:

- Current context is `k3d-ncp-local`.
- Nodes are Ready.
- Prerequisite pods are Running or Completed.

## Task 5: Prepare Local Secrets And Registry Access

Files:

- Modify runtime-only: `deploy/stacks/self-managed/secrets/local-secrets.yaml`
- Write runtime artifacts only under `${ARTIFACT_DIR}`

- [ ] Step 1: Verify required credential environment.

Run:

```bash
test -n "${NGC_API_KEY:-}"
```

Expected: command exits `0`. If it fails, stop and ask the user for the credential environment. Do not print secret values.

- [ ] Step 2: Generate gitignored local secrets from the template.

Run:

```bash
export CONTAINER_CREDENTIAL="$(printf '$oauthtoken:%s' "${NGC_API_KEY}" | base64 | tr -d '\n')"
export HELM_CREDENTIAL="$(printf '$oauthtoken:%s' "${NGC_HELM_API_KEY:-${NGC_API_KEY}}" | base64 | tr -d '\n')"
export MODEL_CREDENTIAL="$(printf '$oauthtoken:%s' "${NGC_MODEL_API_KEY:-${NGC_API_KEY}}" | base64 | tr -d '\n')"

cp deploy/stacks/self-managed/secrets/secrets.yaml.template \
  deploy/stacks/self-managed/secrets/local-secrets.yaml

perl -0pi -e '
BEGIN {
  @values = (
    $ENV{"CONTAINER_CREDENTIAL"},
    $ENV{"CONTAINER_CREDENTIAL"},
    $ENV{"HELM_CREDENTIAL"},
    $ENV{"MODEL_CREDENTIAL"},
  );
  $i = 0;
}
s/value: REPLACE_WITH_BASE64_DOCKER_CREDENTIAL/"value: " . $values[$i++]/ge
' deploy/stacks/self-managed/secrets/local-secrets.yaml
```

Expected: `local-secrets.yaml` exists and contains no placeholder values.

- [ ] Step 3: Validate secret format.

Run:

```bash
deploy/stacks/self-managed/scripts/validate-secrets.sh local \
  deploy/stacks/self-managed/secrets \
  > "${ARTIFACT_DIR}/validate-secrets.log" 2>&1
```

Expected: command exits `0`.

- [ ] Step 4: Verify invalid secret format is rejected.

Run:

```bash
cp deploy/stacks/self-managed/secrets/local-secrets.yaml \
  "${ARTIFACT_DIR}/bad-local-secrets.yaml"
perl -0pi -e 's/value: [A-Za-z0-9+\/=]+/value: not-base64/' \
  "${ARTIFACT_DIR}/bad-local-secrets.yaml"
mkdir -p "${ARTIFACT_DIR}/bad-secrets-dir"
cp "${ARTIFACT_DIR}/bad-local-secrets.yaml" \
  "${ARTIFACT_DIR}/bad-secrets-dir/local-secrets.yaml"
if deploy/stacks/self-managed/scripts/validate-secrets.sh local \
  "${ARTIFACT_DIR}/bad-secrets-dir" \
  > "${ARTIFACT_DIR}/validate-bad-secrets.log" 2>&1; then
  echo "expected bad secrets validation to fail" >&2
  exit 1
fi
```

Expected: command exits `0` because the validation script rejected the bad file.

- [ ] Step 5: Log into NGC registries without printing secrets.

Run:

```bash
docker login nvcr.io -u '$oauthtoken' -p "${NGC_API_KEY}" \
  > "${ARTIFACT_DIR}/docker-login-nvcr.log" 2>&1
helm registry login nvcr.io -u '$oauthtoken' -p "${NGC_API_KEY}" \
  > "${ARTIFACT_DIR}/helm-login-nvcr.log" 2>&1
```

Expected: both commands exit `0`. If either fails, append a credential-scope issue before continuing.

## Task 6: Run CLI Preflight For The One-Click Path

Files:

- Read runtime binary: `src/clis/nvcf-cli/nvcf-cli`
- Write runtime artifacts only under `${ARTIFACT_DIR}`

- [ ] Step 1: Run local preflight with the local stack.

Run:

```bash
export REPO_ROOT="$(pwd)"
export STACK_PATH="${REPO_ROOT}/deploy/stacks/self-managed"
export CLI="${REPO_ROOT}/src/clis/nvcf-cli/nvcf-cli"
PATH="${STACK_PATH}/bin:${PATH}" \
  "${CLI}" self-hosted check --pre --plain --stack="${STACK_PATH}" \
  > "${ARTIFACT_DIR}/check-pre.log" 2>&1
```

Expected:

- Command exits `0`.
- The log does not contain `sis.nvcf.nvidia.com`.
- The log does not fail because of host Helm 4.

- [ ] Step 2: Record whether the deprecated SIS alias is still accepted.

Run after the control plane is installed in Task 7 if this pre-install command cannot reach SIS:

```bash
PATH="${STACK_PATH}/bin:${PATH}" \
  "${CLI}" self-hosted check --pre --plain --stack="${STACK_PATH}" --sis-url="${SIS_URL}" \
  > "${ARTIFACT_DIR}/check-pre-sis-alias.log" 2>&1
```

Expected:

- Command exits `0`.
- The command line accepts `--sis-url`.
- No public SIS probe is attempted during `--pre`.

## Task 7: Run One-Click Bring-Up

Files:

- Read runtime binary: `src/clis/nvcf-cli/nvcf-cli`
- Read and write runtime stack output: `deploy/stacks/self-managed/out/`
- Write runtime artifacts only under `${ARTIFACT_DIR}`

- [ ] Step 1: Run the one-click CLI install.

Run:

```bash
set -o pipefail
PATH="${STACK_PATH}/bin:${PATH}" \
NGC_API_KEY="${NGC_API_KEY}" \
NVCF_ICMS_URL="${SIS_URL}" \
  "${CLI}" self-hosted up \
    --env=local \
    --stack="${STACK_PATH}" \
    --cluster-name="${CLUSTER_NAME}" \
    --nca-id="${NCA_ID}" \
    --region="${REGION}" \
    --icms-url="${SIS_URL}" \
    --refresh-token \
    --plain \
  2>&1 | tee "${ARTIFACT_DIR}/self-hosted-up.log"
```

Expected:

- Command exits `0`.
- Output reaches final health.
- Output does not contain `unknown flag: --sequential-helmfiles`.
- Output does not report `ErrImagePull`, `ImagePullBackOff`, or `ComputePlaneNotReady`.
- If Cassandra hook or migration failure appears, classify it as known-deferred issue 7, collect evidence, and stop.

## Task 8: Verify Final Health Through CLI And Kubernetes

Files:

- Read runtime binary: `src/clis/nvcf-cli/nvcf-cli`
- Write runtime artifacts only under `${ARTIFACT_DIR}`

- [ ] Step 1: Run `self-hosted status` against the local SIS URL.

Run:

```bash
PATH="${STACK_PATH}/bin:${PATH}" \
  "${CLI}" self-hosted status --plain \
    --cluster-name="${CLUSTER_NAME}" \
    --icms-url="${SIS_URL}" \
  > "${ARTIFACT_DIR}/self-hosted-status.log" 2>&1
```

Expected:

- Command exits `0`.
- If verdict is `unknown`, the output includes `SIS Cluster List` with a concrete error.
- If verdict is `unknown` without a diagnostic row, classify it as `new-regression`.

- [ ] Step 2: Run full CLI checks.

Run:

```bash
PATH="${STACK_PATH}/bin:${PATH}" \
  "${CLI}" self-hosted check --all --plain \
    --cluster-name="${CLUSTER_NAME}" \
    --icms-url="${SIS_URL}" \
  > "${ARTIFACT_DIR}/self-hosted-check-all.log" 2>&1
```

Expected:

- Command exits `0`.
- Output contains `verdict=ok`.
- Output shows zero failed checks.

- [ ] Step 3: Snapshot Kubernetes and Helm state.

Run:

```bash
{
  kubectl get pods -A -o wide
  kubectl get events -A --sort-by='.lastTimestamp'
  kubectl get nvcfbackends -A -o wide
  kubectl get nvcfbackends -A -o yaml
  helm list -A
} > "${ARTIFACT_DIR}/final-kubernetes-snapshot.txt" 2>&1
```

Expected:

- All active workload pods are Running or Completed.
- All self-managed Helm releases are deployed.
- `NVCFBackend` for `ncp-local` is healthy.

- [ ] Step 4: Check the deprecated `--sis-url` alias after install.

Run:

```bash
PATH="${STACK_PATH}/bin:${PATH}" \
  "${CLI}" self-hosted check --all --plain \
    --cluster-name="${CLUSTER_NAME}" \
    --sis-url="${SIS_URL}" \
  > "${ARTIFACT_DIR}/self-hosted-check-all-sis-alias.log" 2>&1
```

Expected:

- Command exits `0`.
- Output contains `verdict=ok`.
- This confirms compatibility while docs prefer `--icms-url`.

## Task 9: Summarize Results And Open Follow-Up Work

Files:

- Write runtime artifact: `${ARTIFACT_DIR}/summary.md`
- Write runtime artifact: `${ARTIFACT_DIR}/issue-log.md`

- [ ] Step 1: Produce a concise summary.

Run:

```bash
cat > "${ARTIFACT_DIR}/summary.md" <<EOF
# One-Click Local CLI Bring-Up Summary

Date UTC: $(date -u +%Y-%m-%dT%H:%M:%SZ)
Branch: $(git rev-parse --abbrev-ref HEAD)
Commit: $(git rev-parse HEAD)
Cluster: ${CLUSTER_NAME}
Context: ${KUBE_CONTEXT}
Stack: ${STACK_PATH}

Required gates:
- CLI build:
- Stack pinned tools:
- k3d setup:
- check --pre:
- self-hosted up:
- self-hosted status:
- check --all:
- NVCFBackend health:
- Helm releases:

Issue log:
- ${ARTIFACT_DIR}/issue-log.md
EOF
```

Expected: summary exists and points to the issue log.

- [ ] Step 2: Review the issue log for required follow-ups.

Run:

```bash
cat "${ARTIFACT_DIR}/issue-log.md"
```

Expected:

- Issue 7 remains marked as known-deferred if encountered.
- Any residual issue from the known-issue table is marked as `known-deferred`.
- Any behavior that contradicts an MR fix is marked as `new-regression`.
- No raw credentials appear in the issue log or artifacts.

- [ ] Step 3: Update the MR with the final test outcome.

Run after the summary is complete:

```bash
glab mr note 128 --repo nvcf/nvcf --message "$(cat "${ARTIFACT_DIR}/summary.md")"
```

Expected: MR !128 has a note with the test result and artifact pointers.

## Acceptance Criteria

The one-click bring-up test passes only when all criteria are true:

- The branch is rebased onto current `origin/main`.
- One fresh k3d cluster named `ncp-local` is used for both planes.
- `nvcf-cli self-hosted up` exits `0` with `--env=local`, `--stack`, and `--icms-url`.
- `self-hosted check --all --plain --cluster-name=ncp-local --icms-url=http://sis.localhost:8080` exits `0`.
- Active pods are Running or Completed.
- Helm releases are deployed.
- `NVCFBackend` for `ncp-local` is healthy.
- The issue log exists and classifies every deviation.
- Issue 7 and the other partial fixes listed above are clearly marked as known-deferred when they appear.
