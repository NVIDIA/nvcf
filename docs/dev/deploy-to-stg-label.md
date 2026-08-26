# The deploy-to-stg pull request label

Add the `deploy-to-stg` label to a pull request and CI builds that PR's service
image and pushes it to the internal NGC dev (ncp-dev) registry, so you can pull
it into a cluster before the PR merges.

Source of truth:
[`.github/workflows/image-push-manual.yml`](../../.github/workflows/image-push-manual.yml).

## How to use it

1. Add the `deploy-to-stg` label to your PR. That builds and pushes once.
2. Every later push to the PR rebuilds and pushes again.
3. Remove the label to stop further builds.

Reopening a PR that still carries the label also builds. Adding an unrelated
label to a PR that already carries `deploy-to-stg` does not.

Pull requests from forks are skipped. The workflow uses `pull_request`, not
`pull_request_target`, so untrusted code never runs with the registry
credentials, and the fork check runs in the first job.

## Which service gets built

A PR event carries no service name, so the workflow derives one. It diffs the
PR's own changes against the merge base, takes the `src/<plane>/<service>`
prefix of every changed file, and builds only when:

- exactly one such subtree changed, and
- that subtree has a `BUILD.bazel`.

Anything else is skipped with a notice on the `resolve subtree` job. It does
not fail the PR.

If your PR touches more than one service, or you want to push from a ref
without labeling a PR, run the workflow manually: Actions, `image-push
(manual)`, Run workflow, and pass `service_path`, for example
`src/invocation-plane-services/grpc-proxy`.

## Where the image lands

The registry base is the `NCP_DEV_REGISTRY` repository secret. The repository
name under it comes from the Bazel `oci_image_index` target name:

| Target name | Pushed repository |
|---|---|
| `image` | `<service>` |
| `<component>_image` | `<service>-<component>`, underscores become hyphens |
| `<name>-image` | `<name>`, used as is |

Every `oci_image_index` target under the subtree is pushed, so a subtree with
several images publishes several repositories.

Each push gets two tags: `gh.<run-number>-<first 8 characters of the run's
commit SHA>`, and `latest-dispatch`. Tags are always machine-derived; there is
no way to pass one in. Images are multi-arch, linux/amd64 and linux/arm64.

The exact destination is printed in the `push to ncp-dev` job log, on lines of
the form `[push] <target> -> <repository>:<tag>`. The workflow does not post
the image reference back to the PR.

## What the workflow needs

Permissions: `contents: read` and `packages: read`. The `packages` scope is
what lets the job pull the private `ghcr.io/nvidia/nvcf/bazel-ci` container it
runs in.

Repository secrets:

- `NGC_NCP_DEV_KEY`: NGC key with push access to the dev registry.
- `NCP_DEV_REGISTRY`: registry base, host, tenant, and repository prefix.
- `BAZEL_REMOTE_CACHE_TOKEN`: optional. Without it the build falls back to the
  local disk cache and is slower.

Repository variables: `BAZEL_CI_IMAGE`, `BAZEL_REMOTE_CACHE_ENDPOINT`,
`BAZEL_REMOTE_CACHE_CA`.

These runs only read the shared Bazel cache.
[`tools/ci/bazel-cache-upload-mode`](../../tools/ci/bazel-cache-upload-mode)
grants write access only to `merge_group` and pushes to `main`, so a PR build
can never write to it.

## When it does not work

Nothing ran, or every job is skipped. Check the condition on `resolve subtree`.
It skips when the PR is from a fork, when the label is not on the PR, or when
the event was a `labeled` event for some other label.

It ran but pushed nothing. Read the notice on `resolve subtree`. There are
three:

- no `src/<plane>/<service>` file changed,
- the single subtree that changed has no `BUILD.bazel`,
- more than one subtree changed, so pick one with `workflow_dispatch`.

The push job failed:

- `ERROR: set secrets NGC_NCP_DEV_KEY and NCP_DEV_REGISTRY`: repository
  configuration, not your PR.
- `ERROR: no such subtree`: bad `service_path` on a manual run. The error lists
  the subtrees that own a Bazel module.
- `ERROR: no oci_image_index targets under <path>`: the subtree has no image
  target such as `go_oci_image` or `java_oci_image`.
- `ERROR: bazel query failed`: a BUILD or package loading error in the subtree.
  Reproduce it locally.

A run that vanished mid-build was most likely superseded. PR runs are grouped
by ref with `cancel-in-progress`, so a newer push cancels the older run. Manual
runs are grouped by `service_path` and are not cancelled.

## Charts

There is no label for charts.
[`.github/workflows/chart-push-manual.yml`](../../.github/workflows/chart-push-manual.yml)
packages a chart from `deploy/helm/` and pushes it to the same dev registry. It
is `workflow_dispatch` only.
