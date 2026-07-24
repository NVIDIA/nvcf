# Bazel in the NVCF monorepo

This file is the contributor-facing guide for the Bazel build path in the
NVCF umbrella repository. Bazel is the build engine for onboarded subtrees;
upstream-owned subtrees keep their existing build paths until they are
explicitly integrated.

## Current Scope

Bazel currently builds, tests, and packages:

- `src/clis/nvcf-cli` (Go binary + multi-platform release matrix + OCI image)
- `src/libraries/go/lib` (Go library, 92 targets)
- `src/libraries/java/nv-boot-parent` (Java framework libraries and tests)
- `src/control-plane-services/cloud-tasks` (Java libraries, tests, and Spring
  Boot application)

Other upstream-owned subtrees remain excluded until they are onboarded one at
a time. `nv-boot-parent` and onboarded Java service directories are folded
into the root Bazel module and are not nested Bazel workspaces.

## One-time setup

### Linux

```bash
# Bazelisk pins the right Bazel version per repo via .bazelversion.
curl -fSL -o ~/.local/bin/bazel \
  "https://github.com/bazelbuild/bazelisk/releases/download/v1.25.0/bazelisk-linux-$(dpkg --print-architecture)"
chmod +x ~/.local/bin/bazel

# Toolchain prerequisites for the Go and OCI rules.
sudo apt-get install -y gcc g++ git make python3 lld
sudo update-alternatives --install /usr/bin/ld ld /usr/bin/ld.lld 100

# Confirm setup.
bazel version
bazel info release
```

There is also `setup.sh` at the repo root that installs Bazelisk into
`~/.local/bin` if you prefer a script.

### macOS

```bash
# Apple Silicon and Intel both work; Bazelisk picks the right binary.
brew install bazelisk

# git is preinstalled with Xcode CLT; install if needed.
xcode-select --install || true

# (Optional) lld for cross-compiling Linux binaries from your Mac.
brew install llvm
ln -sfn "$(brew --prefix llvm)/bin/ld.lld" "$(brew --prefix)/bin/ld.lld"

bazel version
bazel info release
```

The OCI image targets cross-compile Linux binaries from your Mac via the
`hermetic_cc_toolchain` (Zig). No additional setup is needed; Bazel fetches
the cross-toolchain on first use.

### Common environment

The repo expects Bazel 9.1.1 (pinned in `.bazelversion`). Bazelisk handles
the download automatically; do not install Bazel via apt or brew directly,
as that pins a different version.

Java targets use Java 25 with the root `.bazelrc` setting
`--java_runtime_version=local_jdk`. The pinned containerized CI image supplies
Temurin 25 through `JAVA_HOME`. The Docker-host lane downloads and configures
Temurin 25 through the workflow's `actions/setup-java@v4` step.

For local Java work, install an organization-approved full JDK 25 and point
`JAVA_HOME` at it:

```bash
export JAVA_HOME="<path-to-jdk-25>"
export PATH="${JAVA_HOME}/bin:${PATH}"
java --version
```

Java services with Testcontainers tests and the nv-boot Cassandra tests also
require a running Docker daemon. Use Docker Desktop on macOS or Docker Engine
on Linux.

`bazel info java-home` reports the Java runtime used to run the Bazel server.
That directory is not guaranteed to contain command-line tools such as
`javac`, so it is not the right way to inspect the Java toolchains selected for
build actions. Query those toolchains directly:

```bash
bazel cquery @bazel_tools//tools/jdk:current_java_runtime \
  --output=starlark \
  --starlark:expr='str(providers(target)["ToolchainInfo"].java_runtime.version)'

bazel cquery @bazel_tools//tools/jdk:current_host_java_runtime \
  --output=starlark \
  --starlark:expr='str(providers(target)["ToolchainInfo"].java_runtime.version)'
```

Both commands must print `25`. The normal Java build then proves that the
compiler toolchain works; a separate `javac` command under
`bazel info java-home` is neither required nor expected.

## Java In The Root Module

`nv-boot-parent` and onboarded Java services are ordinary source directories
inside the single root `nvcf` Bazel module. They do not have nested
`MODULE.bazel`, `.bazelrc`, `.bazelversion`, downloader configuration, or
dependency lockfiles. Run their Bazel commands from this repository root.

For someone familiar with Maven, these root files divide responsibilities that
often live in a parent POM, Maven settings, and the local repository:

| Root file | Purpose |
|---|---|
| `.bazelversion` | Tells Bazelisk which Bazel release to run. |
| `.bazelrc` | Supplies shared Bazel flags, Java 25 toolchain settings, and downloader configuration. |
| `.bazel_downloader_config` | Redirects supported external downloads through approved mirrors. It does not declare dependencies. |
| `MODULE.bazel` | Declares Bazel rule modules, BOM imports, and the roots of the shared third-party Java graph. |
| `maven_install.json` | Locks resolved third-party Java artifacts, relationships, repositories, and checksums. Its name describes Maven-compatible coordinates; it does not run Maven. |
| `MODULE.bazel.lock` | Locks Bzlmod modules and module-extension evaluation. It is separate from the Java artifact lock. |

Commit changes to these files together when one dependency update affects more
than one of them. Do not edit either lockfile manually.

All Java components use the root-owned `@nv_third_party_deps` hub for external
jars. The hub contains third-party artifacts only. `nv-boot-parent` and each
service remain first-party source targets referenced with direct labels such
as:

```text
//src/libraries/java/nv-boot-parent/nv-boot-starter-core:nv_boot_starter_core
//src/control-plane-services/<service-directory>/<module>:<target>
```

Set a portable output root once per local shell:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"
export JAVA_SERVICE_DIR="src/control-plane-services/<service-directory>"
export JAVA_APP_MODULE="<spring-boot-app-module>"
```

Replace the angle-bracket placeholders with the service directory and its
Spring Boot application module. The component's own `BAZEL.md` supplies its
exact values and targets.

Build or test the complete framework or one Java service:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/libraries/java/nv-boot-parent/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/libraries/java/nv-boot-parent/... \
  --cache_test_results=no \
  --test_output=errors

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build "//${JAVA_SERVICE_DIR}/..."

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test "//${JAVA_SERVICE_DIR}/..." \
  --cache_test_results=no \
  --test_output=errors
```

When the selected scope includes Testcontainers tests, the complete suite
requires a running Docker daemon. `bazel test` automatically builds the code
needed by the selected tests; a separate build is useful for compile-only
feedback and for non-test products such as the Spring Boot app jar.

Build the selected service's executable app jar with:

```bash
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build "//${JAVA_SERVICE_DIR}/${JAVA_APP_MODULE}:app"
```

Its output is:

```text
bazel-bin/src/control-plane-services/<service-directory>/<spring-boot-app-module>/app.jar
```

Real Java test, JUnit, and JaCoCo outputs are under each target's
`bazel-testlogs/<component>/<module>/tests/test.outputs` directory. The
component guides provide commands for one module, class, or method and for
NOTICE, OSRB, Docker, and Maven coexistence:

```text
src/libraries/java/nv-boot-parent/BAZEL.md
src/control-plane-services/<service-directory>/BAZEL.md
```

When the shared third-party Java graph changes, repin it from the root:

```bash
REPIN=1 bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run @nv_third_party_deps//:pin
```

Java CI registration is component-local. Each component owns one
`bazel-java-ci.json`; `.github/workflows/bazel.yml` discovers those files and
derives shared-Java triggers, framework-to-service validation, CI execution
environment, and artifact upload. Use `ci_lane: docker-host` when any component
test requires Docker and `ci_lane: build-container` otherwise. Java root scope
is implicit and is not a descriptor option. The detector supports
dependency-aware selection, but current policy deliberately runs the full
matrix on every PR and push for regression safety.

## Day-to-day commands

### Build

```bash
# Native binary for your host platform.
bazel build //src/clis/nvcf-cli:nvcf-cli

# All five release binaries (linux/darwin/windows x amd64/arm64).
bazel build //src/clis/nvcf-cli:dist

# All Go libraries.
bazel build //src/libraries/go/lib/...

# Everything Bazel knows about.
bazel build //...
```

### Test

```bash
# All CLI tests (11 targets, sandboxed).
bazel test //src/clis/nvcf-cli/...

# All library tests (51 targets; 41 pass, 10 are flagged for testdata cleanup).
bazel test //src/libraries/go/lib/...

# Stream output even when tests pass.
bazel test //src/clis/nvcf-cli/... --test_output=streamed
```

The Bazel sandbox strips `$HOME` and `$XDG_CACHE_HOME`; `.bazelrc` re-exports
both pointing at `/tmp` so cobra/viper/git-cache code paths do not blow up
on first reference. If you add a test that needs more env, declare it on the
`go_test` rule via `env = {...}`.

### OCI image

```bash
# Build the host-arch image.
bazel build //src/clis/nvcf-cli:image

# Build the multi-arch index (amd64 + arm64) using the zig cross-toolchain.
bazel build //src/clis/nvcf-cli:image_index

# Load the host-arch image into your local docker daemon.
bazel run //src/clis/nvcf-cli:image_load
docker images | grep nvcf-cli

# Push the multi-arch index to the project registry (manual; needs docker login).
bazel run --stamp //src/clis/nvcf-cli:image_push
```

The image push uses `tools/workspace_status.sh` to compute tags. With
`--stamp` enabled, the index is published with `latest`, the version
(from `git describe` or `mr-<sha>`), and the short commit.

### Generate or refresh BUILD files

After adding a Go file or import, run Gazelle to regenerate per-package
BUILD files:

```bash
bazel run //:gazelle

# If you added a new external Go module, refresh use_repo entries too:
bazel mod tidy
```

Gazelle is configured to skip everything outside the current native scope, so it will
not touch upstream-owned subtrees or vendored directories.

#### Rust equivalent

There is no Gazelle equivalent for Rust. BUILD files for `rust_library`
and `rust_binary` targets are written by hand; only third-party crate
metadata is generated, by `crate_universe`. When you edit a Rust crate's
`Cargo.toml` or `Cargo.lock`, repin the crate index:

```bash
# Repin all crate_universe hubs in the module graph:
CARGO_BAZEL_REPIN=1 bazel mod deps

# Narrow to a single hub (faster):
CARGO_BAZEL_REPIN=true CARGO_BAZEL_REPIN_ALLOWLIST=<hub_name> bazel mod deps
```

Do not run `bazel sync --only=...` here. `bazel sync` is a WORKSPACE-mode
command and Bazel 8 rejects it on bzlmod-only repos with
`ERROR: WORKSPACE has to be enabled for sync command to work`.

Commit any diffs to `Cargo.lock` and `MODULE.bazel.lock`.

### Build graph queries (useful for review)

```bash
# Show every target in nvcf-cli.
bazel query //src/clis/nvcf-cli/...

# Show what nvcf-cli depends on (transitively, just our own packages).
bazel query 'kind(go_library, deps(//src/clis/nvcf-cli:nvcf-cli)) intersect //src/...'

# Show the module dependency graph.
bazel mod graph --depth=2

# Compute reverse deps: who depends on the lib's auth package?
bazel query 'rdeps(//..., //src/libraries/go/lib/pkg/auth:auth)'
```

## Caches

Three layers, in priority order from fastest to slowest:

1. **Local action cache** under `~/.cache/bazel/`. Incremental, per-target.
2. **Local repository cache** under `~/.cache/bazel/`. External module
   downloads (Go modules, OCI base images, Zig toolchain). Survives
   `bazel clean`.
3. **Remote cache**. Enabled by default via `.bazelrc` as a read-only cache;
   CI layers on `--config=remote-write` after probing the cache endpoint.

`bazel clean` purges local build outputs but keeps the repo cache.
`bazel clean --expunge` purges everything (rare; recovers from corrupted
cache or stale toolchain pinning).

In CI (`bazel-smoke` job in root `.gitlab-ci.yml`) both local caches are
persisted to `${CI_PROJECT_DIR}/.bazel-cache` and registered in GitLab's
`cache:` keyed on `MODULE.bazel.lock`, so the second pipeline run is fast.

### Remote cache

The repo enables the read-only `--config=remote` profile by default in
`.bazelrc`. The behaviorally important lines are:

```
build --config=remote
build:remote --remote_cache=grpc://<remote-cache-endpoint>
build:remote --remote_upload_local_results=false
build:remote --remote_cache_compression
build:remote --remote_timeout=120
build:remote --remote_retries=5
build:remote --remote_max_connections=50
build:remote --remote_local_fallback
build:remote-write --config=remote
build:remote-write --remote_upload_local_results=true
```

When invoked:

- Bazel checks the remote action cache before executing each compile/test.
  Cache hit -> the action's outputs are downloaded and the action is not
  re-executed.
- Cache miss -> action runs locally. Local developer builds do not upload
  results by default, which avoids cache poisoning from non-hermetic local
  environments. CI uses `--config=remote-write` to populate the cache.
- Per-action remote cache failures are covered by `--remote_local_fallback`,
  so Bazel continues locally and prints a warning for those failures.
- The initial remote Capabilities RPC is not covered by
  `--remote_local_fallback`. If the cache endpoint cannot be resolved or the
  cache frontend is fully unreachable, Bazel can fail before local execution
  starts.

Local opt-out / override:

```bash
# One-shot local-only build.
bazel build --remote_cache= //src/clis/nvcf-cli:nvcf-cli

# Persistent local-only override.
echo 'build --remote_cache=' >> ~/.bazelrc.user

# Workspace-local override, useful for one checkout only.
echo 'build --remote_cache=' >> user.bazelrc
```

Do not use `--noremote_cache`; `--remote_cache` is a string flag, not a
boolean flag, so Bazel rejects the `no` prefix. `user.bazelrc` is in
`.gitignore`-conventions territory; it lets you set personal defaults
without polluting the shared `.bazelrc`.

CI scope: the per-CLI Bazel jobs (`go-test`, `go-build`,
`verify-agent-skill-manifest` in `tools/ci/nvcf-cli.yml`) and umbrella
jobs inherit the default read-only cache. CI jobs add `--config=remote-write`
only after their preflight probe confirms that the remote cache is reachable.

### Lifecycle and ownership

- **Host**: managed by the NVCF team and reviewed on the team's normal
  infrastructure cadence.
- **Provisioning**: managed by the NVCF team through the internal
  remote-cache automation.
- **Storage**: starts modest; expansion happens out-of-band as cache
  size grows. If you observe high cache-miss rates after large ingestion
  windows, check the cache browser for eviction patterns.
- **Failure mode**: builds with `--config=remote --remote_local_fallback`
  degrade to local execution on action errors but hard-fail on initial
  Capabilities RPC failure (e.g., backend storage shards down).
  CI guards against this in two layers (see `.bazel-remote-probe` in
  `.gitlab-ci.yml` and `.bazel-cli` in `tools/ci/nvcf-cli.yml`):
  1. Each Bazel job's `before_script` does a 5-second TCP probe of
     the remote-cache endpoint. If it fails, `--config=remote` is
     dropped for that run and the job continues with local cache only,
     instead of hanging Bazel on the Capabilities RPC.
  2. A CI/CD variable `NVCF_BAZEL_REMOTE` (default `1`) can be set to
     `0` in GitLab project settings to force local-only across all
     Bazel jobs without a code push. Useful when the remote cache is degraded
     in a way the TCP probe can't detect (port open, gRPC wedged).
- **End of lease**: either renew, migrate to longer-term host, or roll
  back to local-only caching. The per-service opt-in pattern means
  the rollback footprint is small.

Inspecting cache hits/misses: open the build's invocation in the cache browser
(set Bazel's `--bes_results_url` locally if you want clickable links per
build).

## CI

Every workflow that needs the Bazel toolchain runs in the `bazel-ci` job
container, sourced from the repository variable `BAZEL_CI_IMAGE` (currently
`ghcr.io/nvidia/nvcf/bazel-ci`). Each workflow carries the current pin as a
`||` fallback so CI still runs when the variable is unavailable, for example on
a fork.

That image is built in the internal
[`nvcf/bazel-ci-templates`](https://gitlab-master.nvidia.com/nvcf/bazel-ci-templates)
project, stamped with a version, and mirrored to GHCR. The mirror is currently
manual; automation is planned. To change the image's Bazel, Java, or operating
system tooling, update the internal template first, publish and mirror a new
tag, then update the `BAZEL_CI_IMAGE` repository variable. Do not hardcode the
tag per workflow: it drifted to three different versions across workflows and
this document before the variable was introduced.

The root `ci/Dockerfile.bazel` and `.github/workflows/bazel-ci-image.yml` were a
stale, divergent copy (no Java, older Bazel) and have been removed. The image is
built in `nvcf/bazel-ci-templates` and mirrored to ghcr (see above); this repo
does not build it.

The detect job also enforces the single-module import boundary:

```bash
bash tools/ci/check-java-import-boundaries
```

Run this after refreshing `nv-boot-parent` or an onboarded Java service. It
fails when a standalone Bazel root file, lockfile, workspace file, or migration
directory is reintroduced under an imported subtree.

The existing internal Bazel jobs include:

- `bazel-smoke`: pulls the image, runs `bazel info release`, then
  `bazel build --config=remote //src/libraries/go/lib/...
  //src/clis/nvcf-cli:image_index` and `bazel mod graph`. It does not
  rebuild `//src/clis/nvcf-cli/...` because the per-CLI child pipeline
  (`nvcf-cli-ci` -> `go-test`/`go-build`) already covers that against
  the same remote cache. Adding CLI targets back here just burns
  bandwidth and runner time without improving signal.
- `bazel-image-push` (manual): runs `bazel run --stamp --config=remote
  //src/clis/nvcf-cli:image_push`. With the remote cache warm from the
  per-CLI build, the image layers come straight out of the remote cache.

Per-service jobs in `tools/ci/nvcf-cli.yml` (`go-test`, `go-build`)
were rewritten to use Bazel; the legacy `Makefile` was deleted. Downstream
archive/package/publish/ngc-push stages still consume
`src/clis/nvcf-cli/build/nvcf-cli-{platform}` files, which the bazel-driven
`go-build` job now populates by copying out of `bazel-bin`.

## Stamping (release metadata)

`bazel build --stamp //src/clis/nvcf-cli:nvcf-cli` injects:

| Symbol | Source |
|---|---|
| `main.Version` | `git describe --tags`, falls back to `mr-<short-sha>`, override via `$NVCF_VERSION` |
| `main.GitCommit` | `git rev-parse --short HEAD` (with `-dirty` suffix if working tree is dirty) |
| `main.GitBranch` | `git rev-parse --abbrev-ref HEAD` |
| `main.BuildDate` | UTC ISO timestamp |
| `main.BuildUser` | `whoami`, override via `$NVCF_BUILD_USER` |
| `main.GoVersion` | constant `bazel-rules_go`, override via `$NVCF_GO_VERSION` |

The script that emits these values is `tools/workspace_status.sh`. CI sets
`$NVCF_VERSION` and `$NVCF_BUILD_USER` so release builds carry the same
metadata the legacy `Makefile` injected via `-ldflags -X`.

Without `--stamp`, the defaults declared in `main.go` (`Version = "dev"`,
etc.) are used, which keeps developer iteration fast (no git invocation per
build).

## Adding a new Go module

For native subtrees outside the current scope:

1. Add the module path to `go.work.bazel` under `use (...)`.
2. Add or update its `go.mod`.
3. Add a `BUILD.bazel` at the subtree root with at least:
   `# gazelle:prefix <module-path>` and `# gazelle:exclude vendor` if it has one.
4. Remove the subtree from `.bazelignore` and from the
   `# gazelle:exclude <subtree>` lines in the root `BUILD.bazel`.
5. Run `bazel run //:gazelle` then `bazel mod tidy`.
6. `bazel build //path/to/subtree/...` to validate.

The public checkout does not contain the internal source-mirroring
configuration. For an upstream-owned subtree, distinguish between source files
that continue to mirror from the standalone repository and monorepo-native
Bazel overlays. The import process must exclude standalone `MODULE.bazel`,
lockfiles, `.bazelrc`, `.bazelversion`, downloader config, dependency hub,
and `bazel-enablement` content. Root-module BUILD adaptations and monorepo
agent/documentation overlays must be preserved during refreshes.

## Per-service publish cadence

Each subtree publishes on its own terms. The umbrella stack release lives
in `deploy/stacks/self-managed/` and is independent. Before copy-pasting
the `nvcf-cli` Bazel publish pattern onto a new service, check that
service's existing `.gitlab-ci.yml` for its current cadence and mirror
it in the rules of the Bazel-driven publish job:

| Cadence | Example services | Rule |
|---|---|---|
| Tag only | `nvcf-cli` (current Bazel CLI) | `if: '$CI_COMMIT_TAG'` (manual gate optional) |
| Every merge to main | Several control-plane and invocation-plane services | `if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH` |
| Scheduled or manual | Niche services and tools | `when: manual` or pipeline schedule |

Knock-on choices that follow from the cadence:

- Version derivation (`tools/workspace_status.sh`). The CLI uses
  `git describe --tags --exact-match HEAD || mr-<sha>` because tag-only
  publish makes that the meaningful version. Per-merge services usually
  want a SHA-style version (`<short-sha>` or `0.0.0-<short-sha>`) so
  every main commit produces a distinct OCI tag without semver implication.
  Either fork `workspace_status.sh` per service or add an env-driven
  branch (`NVCF_VERSION_STYLE=sha` etc.).

- OCI tag set on push. Tag-driven services typically push
  `:latest`, `:<semver>`, `:<short-sha>`. Per-merge services usually
  push `:latest` and `:<short-sha>` only.

- GitLab cache pressure. Per-merge services trigger Bazel on every main
  commit, so the project-scoped cache (`MODULE.bazel.lock` keyed) sees
  more churn. The cache strategy still works; expect more frequent
  cache repopulations after `MODULE.bazel.lock` updates.

- Image push job rules. Reuse the cadence rule at the job level rather
  than gating via job stage; this keeps the publish stage flexible per
  service without restructuring the parent pipeline. Example for a
  per-merge service:

  ```yaml
  oci-image-push:
    extends: .bazel-cli
    stage: publish
    script:
      - bazel run --stamp //path/to/service:image_push
    rules:
      - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  ```

Do not unify these into a single helper template across services. The
cadence is intentionally per-service and is owned by that service's
maintainers; centralising it would couple unrelated release decisions.

## Troubleshooting

- `unknown repo '...' requested` during build: usually means `MODULE.bazel`
  needs a `use_repo` entry. Run `bazel mod tidy` and rebuild.
- `Failed to query remote execution capabilities` or
  `UNAVAILABLE: Unable to resolve host` during build: your environment may not
  be able to reach the configured remote cache. Re-run the command with
  `--remote_cache=` or add `build --remote_cache=` to `~/.bazelrc.user` for a
  persistent local-only override.
- `compilepkg: missing strict dependencies`: the BUILD file under-declares
  its `deps`. Run `bazel run //:gazelle` to regenerate, or hand-add the
  missing target to `deps`.
- Build fails with stale BUILD content after `git pull`: try
  `bazel clean` (not `--expunge`); the repo cache is fine to keep.
- macOS multi-arch image build fails fetching the Zig toolchain: confirm
  outbound HTTPS to `ziglang.org` is allowed by your network policy.
- `bazel info release` blocks for >30 s on first run: it is downloading the
  pinned Bazel binary. One-time cost.

## Additional Subtree Rollout

Per-service rollout state for upstream-owned subtrees is tracked in an internal
plan that references upstream GitLab URLs and per-service rollout state that
does not belong in the public mirror, including which upstream MRs are open,
which are merged, and which umbrella `imports.yaml` bumps have landed. Update
that internal plan as each service moves through the playbook.

## Out of scope (Later Phase)

- Wiring upstream-owned subtrees listed in `imports.yaml`. One MR per upstream
  owner. See the tracker.
- Migrating goreleaser-driven release stages
  (archive/package/publish/ngc-push) onto Bazel-native equivalents (e.g.
  `pkg_tar`, `oci_push`, custom rules for NGC). Today the artifact
  contracts are preserved via copy-from-bazel-bin shims in CI.
- Go coverage report publication in CI. Java JUnit and JaCoCo reports are
  already generated and uploaded by their component lanes.
- Lint integration. `golangci-lint` still runs as a separate job and is
  not yet wrapped into a Bazel rule.
