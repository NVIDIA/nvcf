# CI pipeline patterns

How to drive `nvcf-cli` from CI (GitLab CI, GitHub Actions, Jenkins, ArgoCD). Patterns that surface frequently.

## Basic install pipeline

```yaml
# GitLab CI example
deploy_nvcf:
  stage: deploy
  image: nvcr.io/0651155215864979/ncp-dev/nvcf-cli:1.4.0
  variables:
    KUBECONFIG: $KUBECONFIG_FILE
  script:
    - nvcf-cli self-hosted check --pre --json | jq -e '.event != "phase_failed"' || exit 2
    - nvcf-cli self-hosted up --cluster-name=$CLUSTER_NAME --token=$NVCF_ADMIN_JWT --non-interactive --json
    - nvcf-cli self-hosted status --json | jq -e '.verdict == "healthy"'
```

Notes:

- `--non-interactive --token=$JWT` is required in CI; never use interactive `init`.
- Always `--json` for machine-parsing.
- Final status check gates downstream stages on `verdict == "healthy"`.

## GitOps (Argo / Flux) pattern

CI doesn't `kubectl apply` — instead, render manifests, commit them, let the controller apply.

```yaml
render:
  script:
    - nvcf-cli self-hosted install --control-plane --no-apply > rendered/control-plane.yaml
    - nvcf-cli self-hosted install --compute-plane --cluster-name=$NAME --no-apply > rendered/compute-plane.yaml
    - git add rendered/ && git commit -m "deploy NVCF $TAG" && git push
```

Then ArgoCD or Flux watches `rendered/` and applies. The CLI never touches the cluster directly.

For the register step (which mutates ICMS, not just K8s):

```yaml
register:
  script:
    - nvcf-cli cluster register --name=$NAME --nca-id=$NCA --region=$REGION --ignore-existing
```

`--ignore-existing` is the GitOps-friendly idempotent contract.

## Multi-cluster pipeline

When the pipeline manages many compute clusters against one control plane:

```yaml
deploy_compute_planes:
  parallel:
    matrix:
      - CLUSTER: ncp-east-1
        CONTEXT: admin@gpu-east-1
      - CLUSTER: ncp-west-1
        CONTEXT: admin@gpu-west-1
      - CLUSTER: ncp-eu-1
        CONTEXT: admin@gpu-eu-1
  script:
    - nvcf-cli self-hosted up
        --cluster-name=$CLUSTER
        --compute-plane-context=$CONTEXT
        --icms-url=$ICMS_PUBLIC_URL
        --token=$NVCF_ADMIN_JWT
        --non-interactive
        --json
```

Each parallel job registers + installs one compute plane against the shared control plane. Failures are isolated.

## Plan-only preview in PRs

```yaml
preview:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  script:
    - |
      nvcf-cli self-hosted up \
        --cluster-name=$CLUSTER_NAME \
        --plan-only \
        --json | tee plan.jsonl
    - jq -s '.[0]' plan.jsonl > plan.json
    - 'gitlab-mr-comment "## NVCF deploy plan\n\n\`\`\`json\n$(cat plan.json)\n\`\`\`"'
```

`--plan-only` exits 0 without changing cluster state; the JSONL output lists each phase + ETA. Renders nicely as an MR comment.
