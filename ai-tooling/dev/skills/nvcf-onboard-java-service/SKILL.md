---
name: nvcf-onboard-java-service
description: >-
  Wire a Java service in the NVCF monorepo so its container image is built,
  tagged and published. Covers the two repositories that must agree, the
  version-continuity trap when history lives in a separate upstream project,
  and the silent-success failure where a release pipeline goes green having
  published nothing. Use when onboarding a new Java service, adding image
  publishing to an existing one, or debugging a release that reported success
  but produced no artifact.
version: "1.0.0"
tags:
  - nvcf
  - java
  - release
  - bazel
tools:
  - Shell
  - Read
---

# Onboarding a Java service for image publishing

Building an image and publishing one are separate problems here. A Java service
gets its image built by CI as soon as it has a `java_oci_image` target; nothing
publishes it until both repositories below are wired.

## The two repositories must agree

| Repository | File | Decides |
|---|---|---|
| NVIDIA/nvcf | `tools/ci/github-release-subprojects.json` | whether a tag is cut at all |
| nvcf-internal | `config/services/<id>.yaml` | where the image is published |
| nvcf-internal | `.gitlab-ci.yml` | whether any job runs |

The registry `id`, the config `service_id`, and the job names must all use the
same identifier. The dispatcher passes it as `NVCF_SERVICE_ID` and the job
rules match on that value.

## Checklist

```text
- [ ] Confirm the service has a java_oci_image target and a
      java_image_contract_test that passes in CI
- [ ] Find the upstream project that owns its version history
- [ ] Push an anchor tag at the current upstream version  (BEFORE registering)
- [ ] Register in tools/ci/github-release-subprojects.json
- [ ] Add config/services/<id>.yaml in nvcf-internal
- [ ] Add .{id}-rules + validate/promote/notify jobs in nvcf-internal
- [ ] Cut a tag and verify the IMAGE EXISTS, not that the pipeline is green
```

## Three traps, all of which have bitten

### 1. A config without jobs succeeds and publishes nothing

`config/services/<id>.yaml` gives the dispatcher something to resolve. It does
not create jobs. With a config and no matching job set, a release dispatch
produces a pipeline containing only `check-agent-tooling`, which succeeds.

This is the worst failure available: a green release pipeline is
indistinguishable from a completed release unless you look in the registry.
Always add the `.{id}-rules` anchor and the `validate-{id}-manifest`,
`promote-{id}`, `notify-{id}` trio, copying an existing service.

### 2. Versions restart at 0.1.0 and go backwards

`github-release` synthesizes an anchor at `INITIAL_RELEASE_FLOOR_VERSION`
(0.0.0) for a registered service with no tags, so its first release is `0.1.0`.

If the service's history lives in a separate upstream project (common: the
monorepo subtree is new, the service is not), that is a regression for anyone
pinning the upstream line, and it fails silently because the release succeeds
and only the number is wrong.

Find the real version first:

```bash
# the upstream project, not this repo
glab api projects/:id/repository/tags | head
```

Then push an anchor tag at that version before registering the service.

### 3. Anchor first, register second

`release-tags.yml` triggers on `**/v*`, so pushing an anchor runs the tag
workflow. While a service is still unregistered, `parse_release_tag` cannot
match its tags and the code takes the `is not a supported release tag;
skipping` path: the anchor lands as a plain tag with no GitHub Release and no
build.

Register first and the anchor creates a Release for a version that was never
built.

```text
correct:  push anchor tag  ->  register in the JSON
wrong:    register         ->  push anchor tag
```

## Java specifics

Java services build from the ROOT Bazel module, unlike services that live in a
nested module. Their config therefore uses:

```yaml
source:
  subtree: "."
images:
  - name: nvcf-<service>
    build:
      type: bazel
      target: //src/control-plane-services/<svc>/<module>:<name>_index
```

Push the `_index` label, never the bare image label: the bare one is the
single host-architecture image, and the index is what carries both
architectures.

`java_image_contract_test` asserts the jar path, the `shelless_ulimit` shim,
the arch-stable `/usr/bin/java` path, and that both architectures are present.
It deliberately does not assert an exec bit, unlike the Go services' entrypoint
test: a jar is read by the JVM, never executed.

## Verifying

A green pipeline is not evidence. Check the artifact:

```bash
skopeo inspect --raw docker://nvcr.io/<org>/ncp-dev/nvcf-<service>:<version> \
  | jq '.manifests[].platform'
```

Expect both `amd64` and `arm64`. If the repository exists with `Tags: []`, the
jobs did not run: see trap 1.

## Related

- [[bazel-java-maven]] for the Bazel wiring itself
- [[bazel-oci-images]] for `java_oci_image`
- [[internal-commit-message]] for the MR description footer
