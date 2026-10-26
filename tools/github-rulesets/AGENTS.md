# GitHub ruleset tooling

This standard-library Go module owns policy planning and reviewed application.
Versioned policies live in `.github/rulesets/`; the operating procedure is
`docs/dev/github-ruleset-policy.md`.

Run `go test ./...` and `go vet ./...` from this directory. The existing
`tools/ci/check-go-tools` workflow discovers this module automatically.

Keep tests offline. Exercise API interactions through `RulesetAPI` test doubles.
Do not change live repository settings while developing or testing a policy.
Preserve unowned rules, reviews, checks and bypass actors on update. New policy
requirements must have regression tests for both safe application and rejection
of conflicting or stale live state.
