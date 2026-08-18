# AGENTS.md - NVCF API Helm chart

Scope: `deploy/helm/cloud-functions`, the Helm chart for the NVCF API service.

The chart publishes as `helm-nvcf-api`. Its `Chart.yaml` name is the published
OCI name and must not be renamed to match the directory or the service. The
release lane is registered as `cloud-functions-helm` in
`tools/ci/github-release-subprojects.json`.

## Commands

Run these from this directory.

```sh
make lint       # helm lint with the shared CI values
make template   # render to bin/manifest.yaml
make validate   # template, then kubeconform
make test       # tests/sidecar_release_artifacts_test.sh
```

`lint`, `template`, and `validate` read
`tools/ci/helm-validate-values/cloud-functions.yaml`. The chart leaves
`api.image.registry`, `api.image.repository`, and the matching
`api.accountBootstrap.image` fields empty on purpose, so it does not render
without those values.

`make install`, `make uninstall`, and `make status` default to release `api` in
namespace `nvcf`.

## Conventions

`Chart.yaml` keeps `version: 0.0.0`. The release pipeline sets the real version
from the `deploy/helm/cloud-functions/v*` tag. `appVersion` tracks the API
service image and is bumped by hand.

Match the conventional commit type to the intended version bump, because the tag
generation pipeline reads it:

- `fix(<scope>):` for a patch bump
- `feat(<scope>):` for a minor bump
- `feat(<scope>)!:` or a `BREAKING CHANGE:` footer for a major bump

A `chore` commit skips tag creation entirely.

## Adjacent subtrees

- `src/control-plane-services/cloud-functions` builds the image this chart deploys.
- Sidecar image references in `api.remoteConfig.configData.nvcf.sidecars` use
  Spring placeholders resolved at runtime. `tests/sidecar_release_artifacts_test.sh`
  guards the `release-artifact-*-image` annotations that surface them to stack
  release tooling, so run it after changing any sidecar entry.
