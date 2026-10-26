# GitHub ruleset policy

The JSON policies in `.github/rulesets/` own selected protection settings
for `NVIDIA/nvcf`. The helper in `tools/github-rulesets/` checks live state
and prepares reviewable changes. Committing a policy does not change GitHub
settings, and CI never applies it automatically.

## Maintenance Helm checks

`maintenance-helm.json` adds the existing `helm charts` check from the GitHub
Actions app to the active `release` ruleset. The release-branch scope already
includes the stack maintenance branches. Those branches run Helm/render tests,
but GitHub must require the result before a merge can trigger a stack release.

The policy preserves the existing reviews, checks, bypass actors, linear-history
rule, branch scope and branch-creation behavior. It does not activate or require
the optional stack upgrade-policy job.

## Review and apply

Use an authenticated GitHub CLI account with repository ruleset read access.
Applying a reviewed plan requires repository administration write access.

```bash
go run -C tools/github-rulesets . \
  --policy ../../.github/rulesets/maintenance-helm.json check

go run -C tools/github-rulesets . \
  --policy ../../.github/rulesets/maintenance-helm.json plan > /tmp/ruleset-plan.json
```

`check` succeeds when the policy is satisfied and exits unsuccessfully on drift
or errors. `plan` returns the complete before/after payload for each changed
ruleset. Both commands only read GitHub. Review the saved plan and arrange the
administrative change before running:

```bash
go run -C tools/github-rulesets . \
  --policy ../../.github/rulesets/maintenance-helm.json \
  --plan /tmp/ruleset-plan.json apply
```

Apply refuses a plan if the policy or live snapshot changed after review. It
checks each ruleset again before writing, then verifies all expected fields on
readback, including existing protections carried through the change. New rules
can be created by stable name unless the policy requires an existing rule.
Existing target/ref scopes must match exactly; unexpected scope changes or
forbidden rules require manual review instead of automatic removal.

GitHub does not offer a transaction across these updates. A concurrent edit
between the final read and write remains possible, so coordinate administration
and inspect readback. If a multi-rule apply stops partway through, leave the
successful changes in place, generate a fresh plan and review the remaining
changes. The helper never deletes a ruleset or rolls back unrelated settings.

Run `check` after application and confirm that a maintenance PR reports
`helm charts` as required. A failed or pending Helm check must block merge.

## Policy ownership

Each policy has a repository, version and list of named rulesets. Desired rules
merge into existing rules by type. Required checks merge by context, with the
policy controlling the app binding for each named check. Unspecified settings
stay unchanged. An explicit `bypass_actors` list owns the full bypass list;
an empty list removes bypasses and must be reviewed in the plan.

Keep each protection requirement in one policy. Tests run without credentials:

```bash
go test -C tools/github-rulesets ./...
```

## Umbrella tags and maintained documentation

Use `.github/rulesets/umbrella-release.json`
with the same plan, review, apply and check commands above. Apply it before
creating the first product tag or maintained-docs branch, and retain the
reviewed plan and successful readback with the release evidence. The
[public umbrella procedure](umbrella-releases.md)
owns product publication and documentation activation. This policy does not
activate maintained docs or add schema 2 docs support.

| Scope | Allowed operation | Required protection |
| --- | --- | --- |
| `refs/tags/nvcf/**` | Create a new product tag | Reject updates and deletion, with no bypass actors |
| `refs/heads/docs/versions/**` | Create the reviewed initial branch; merge approved corrections | Require one approval, code-owner review and the `docs` check; reject deletion and force pushes |

The status check is bound to the GitHub Actions app. `build-test.yml` reports
`docs` for PRs targeting `docs/**` without a path filter; that job validates the
docs and edition layout. Confirm the selected docs source retains this workflow
before rollout. Do not add a path-filtered check that may never report for a
correction. Initial branch creation is exempt from status checks so the reviewed
prepared tree can be bootstrapped; subsequent updates require a reviewed PR.

The policy rejects existing creation restrictions on either scope, and an
existing update freeze on maintained docs. Investigate those conflicts rather
than silently removing protections. The existing `Frozen docs editions` ruleset
for `docs/releases/**` is outside this policy and stays unchanged, as do component
tag rules and unrelated branch protections.
