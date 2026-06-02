# Bazel in the NVCF monorepo

This file is the contributor-facing guide for the Bazel build path in the
NVCF umbrella repo. Bazel is the build engine for the native subtrees in
Phase 1; synthetic-import subtrees keep their existing build paths until they
go native.

For the design rationale (why Bazel, why phased rollout, what synthetic-import
constraints apply), see the merge request that introduced this scaffolding
and the Bazel skill set under `.claude/skills/bazel-*`. The skills cover the
patterns this repo already uses and the ones future phases will need:

| Skill | Use it for |
|---|---|
| `bazel-monorepo-bootstrap` | Re-bootstrapping or auditing root files (`MODULE.bazel`, `.bazelrc`, `tools/workspace_status.sh`, `ci/Dockerfile.bazel`) |
| `bazel-go-gazelle` | Adding or maintaining Go subtrees (Phase 2) |
| `bazel-oci-images` | Adding `rules_oci` images for new services |
| `bazel-java-maven` | Onboarding Java services (e.g. `llm-api-gateway`) |
| `bazel-rust-crate-universe` | Onboarding Rust services (e.g. `parsec`) |
| `bazel-gitlab-child-pipelines` | Wiring a new service into the Bazel CI flow |
| `bazel-synthetic-import-strategy` | Anything touching `imports.yaml` synthetic-import subtrees |

## Phase 1 scope

Bazel currently builds, tests, and packages:

- `src/clis/nvcf-cli` (Go binary + multi-platform release matrix + OCI image)
- `src/libraries/go/lib` (Go library, 92 targets)

Synthetic-import subtrees listed in `imports.yaml` with
`authoritative_source: upstream` are intentionally excluded via
`.bazelignore` and `# gazelle:exclude` directives in the root `BUILD.bazel`.
They will be onboarded one at a time as Phase B in separate MRs.

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

The repo expects Bazel 8.6.0 (pinned in `.bazelversion`). Bazelisk handles
the download automatically; do not install Bazel via apt or brew directly,
as that pins a different version.

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

Gazelle is configured to skip everything outside Phase 1 scope, so it will
not touch synthetic-import subtrees or vendored directories.

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

Commit any diffs to `Cargo.lock` and `MODULE.bazel.lock`. See the
`bazel-rust-crate-universe` skill for the full onboarding flow.

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

Two Bazel-aware jobs in the root `.gitlab-ci.yml`:

- `bazel-ci-image`: rebuilds `ci/Dockerfile.bazel` via buildah and pushes
  to `${CI_REGISTRY_IMAGE}/bazel-ci:<ref-slug>`. Triggers only when
  `ci/Dockerfile.bazel` or `.bazelversion` changes (or when a web pipeline
  is run with `$REBUILD_BAZEL_IMAGE` set).
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

For native subtrees outside Phase 1 scope today:

1. Add the module path to `go.work.bazel` under `use (...)`.
2. Add or update its `go.mod`.
3. Add a `BUILD.bazel` at the subtree root with at least:
   `# gazelle:prefix <module-path>` and `# gazelle:exclude vendor` if it has one.
4. Remove the subtree from `.bazelignore` and from the
   `# gazelle:exclude <subtree>` lines in the root `BUILD.bazel`.
5. Run `bazel run //:gazelle` then `bazel mod tidy`.
6. `bazel build //path/to/subtree/...` to validate.

For synthetic-import subtrees (`authoritative_source: upstream` in
`imports.yaml`), see `.cursor/skills/bazel-synthetic-import-strategy/SKILL.md`.
The short version: Bazel files belong upstream so they survive the next
`tools/scripts/sync_synthetic_imports` run.

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

## Phase B status

Per-service rollout state for synthetic-import subtrees is tracked in
an internal plan that references upstream GitLab URLs and per-service
rollout state that does not belong in the public mirror, including which
upstream MRs are open, which are merged, and which umbrella `imports.yaml`
bumps have landed. Update that internal plan as each service moves through
the playbook.

## Out of scope (Phase B and later)

- Wiring synthetic-import subtrees (22 entries in `imports.yaml`). One MR
  per upstream owner. See the tracker.
- Migrating goreleaser-driven release stages
  (archive/package/publish/ngc-push) onto Bazel-native equivalents (e.g.
  `pkg_tar`, `oci_push`, custom rules for NGC). Today the artifact
  contracts are preserved via copy-from-bazel-bin shims in CI.
- Coverage report generation in CI. `bazel coverage` works locally; CI
  parsing of coverage output is deferred.
- Lint integration. `golangci-lint` still runs as a separate job and is
  not yet wrapped into a Bazel rule.
