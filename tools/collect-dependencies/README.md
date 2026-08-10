# collect-dependencies

This guide is for using and developing `go run -C ./tools/collect-dependencies .`.

For broader compliance context, review workflow, and how `dependencies.md` fits with `NOTICE` and source headers, see [`../../license-compliance.md`](../../license-compliance.md).

## Basic usage

From the repo root, use Go, Java 25, and Bazelisk. The `bazel` command must
resolve to Bazelisk so `.bazelversion` selects the repository's Bazel version.

```bash
go run -C ./tools/collect-dependencies .

# One language only (faster). This rewrites `dependencies.md` with just
# that slice, so run the full command before committing the shared rollup.
go run -C ./tools/collect-dependencies . --language go
go run -C ./tools/collect-dependencies . -l rust
go run -C ./tools/collect-dependencies . -l python
go run -C ./tools/collect-dependencies . -l node
go run -C ./tools/collect-dependencies . -l java
go run -C ./tools/collect-dependencies . -l helm
```

## Outputs

- [`../../dependencies.md`](../../dependencies.md): Repository-wide internal
  audit rollup for Go modules, Rust crates, Python packages, Node.js packages
  from pnpm lockfiles, Java runtime coordinates, and Helm chart dependencies.
  Entries are deduplicated and
  grouped by normalized license expression. Each bullet keeps a language tag,
  and MPL groups keep the explicit version (`MPL-1.0`, `MPL-1.1`,
  `MPL-2.0`) in the heading.

Regenerate when dependency manifests, pnpm lockfiles, Java BUILD targets, or
the shared Java dependency graph change.

## Discovery model

When `imports.yaml` exists, the collector scans the paths declared there. That
supports internal or synthetic-import workspaces. The GitHub monorepo
intentionally has no `imports.yaml`, so the collector scans the repository
itself. Missing `imports.yaml` never means "collect nothing."

Go, Rust, Python, and Helm dependencies come from manifests found under those
scan roots. Java uses a more precise path:

1. Find every `src/**/bazel-java-ci.json` component descriptor.
2. Build each component's standard
   `//<component-directory>:runtime_inventory.json` target in one Bazel
   invocation.
3. Merge those generated runtime inventories into `dependencies.md`.
4. Deduplicate coordinates used by more than one Java component.

Every Java component registered with `bazel-java-ci.json` must expose that
standard runtime inventory target. A malformed descriptor, missing target, or
failed inventory build stops generation with an error.

Project `pom.xml` files are not scanned. They may remain temporarily for Maven
coexistence, but Bazel is the Java dependency source of truth in this
monorepo.

## Java dependency files in simple terms

- `MODULE.bazel` declares the complete shared third-party Java dependency hub
  and its version policy.
- `maven_install.json` locks the exact resolved files and checksums for that
  complete hub. It remains required by `rules_jvm_external`.
- A component's `runtime_inventory.json` contains only dependencies that are
  reachable from that component's Bazel runtime targets.
- A component's `NOTICE` and OSRB delta remain component-scoped compliance
  outputs.
- Root `dependencies.md` is the deduplicated union used for repository-wide
  human review.

The collector does not copy every entry from `maven_install.json`. A
dependency available in the shared hub but unused by every Java runtime
inventory is intentionally absent from `dependencies.md`.

## External APIs and network calls

The tool reads local files first (`go.mod`, `go.sum`, `vendor/`, `Cargo.toml`, Python manifests, Helm charts). The tables below list the HTTPS and subprocess calls used after that for version or license lookup. `go`, `cargo`, and `helm` may add their own traffic (module proxy, git, crates index, chart registries) depending on cache and graph. Those tools must be on `PATH` when those steps run.

### Go

| What | External / toolchain behavior | Disable |
|------|------------------------------|---------|
| Manifests and `vendor/` | Local files only | - |
| `go list -mod=readonly -m -json all` (once per discovered `go.mod` root) | Resolves the module graph without changing `go.mod` or `go.sum`; reads `LICENSE*` under each module `Dir` in `$GOMODCACHE`. May use proxy.golang.org (or `GOPROXY`) and VCS (for example GitHub) like any Go build. A module with an incomplete read-only graph is skipped and later fallbacks can still fill license data. | `COLLECT_DEPS_NO_GO_LIST=1` |
| `go mod download -json` *`module@version`* (versions from `go.sum`) | Fills gaps when `go list … all` fails or leaves modules without a license, with the same proxy and VCS behavior as a normal download. Skips `github.com/...` when the GitHub row below is active to avoid duplicate work. | `COLLECT_DEPS_NO_GO_MOD_DOWNLOAD=1` |
| GitHub REST API | `GET https://api.github.com/repos/{owner}/{repo}/license` for `github.com/owner/repo` (SPDX). gitlab.com and other forges are not called. | `COLLECT_DEPS_NO_GITHUB=1` |

Optional auth: `GITHUB_TOKEN` or `GH_TOKEN` for higher rate limits. With `sudo`, export the token in that shell (`sudo -E`, etc.).

### Rust

| What | External / toolchain behavior | Disable |
|------|------------------------------|---------|
| `cargo metadata` (per workspace root) | Subprocess; reads `Cargo.toml` and lockfile license fields. May hit the crates.io index or git if the graph is not fully local. This tool has no flag to skip `cargo`. | - |
| crates.io HTTP API | `GET https://crates.io/api/v1/crates/{crate}` once per crate name still missing a license after metadata (tool sets a User-Agent). | `COLLECT_DEPS_NO_CRATES_IO=1` |

Without `cargo` on `PATH`, metadata is skipped. With `COLLECT_DEPS_NO_CRATES_IO=1`, Rust license cells stay blank unless something else fills them.

### Python

| What | External / toolchain behavior | Disable |
|------|------------------------------|---------|
| PyPI JSON API | `GET https://pypi.org/pypi/{project}/json` once per deduplicated package name (`license_expression`, `license`, or `License ::` classifiers). | `COLLECT_DEPS_NO_PYPI=1` |

With PyPI disabled, Python rows state that network lookup was skipped.

### Node.js (pnpm)

| What | External / toolchain behavior | Disable |
|------|------------------------------|---------|
| `pnpm-lock.yaml` | Local only. Reads resolved package keys from the `packages` section. Workspace links and local files are skipped. | - |
| npm registry | `GET https://registry.npmjs.org/{package}/{version}` for each package version. Reads the version metadata `license` field. | `COLLECT_DEPS_NO_NPM=1` |

With npm registry lookup disabled, Node.js rows state that network lookup was skipped.

### Java (Bazel)

| What | External / toolchain behavior | Disable |
|------|------------------------------|---------|
| `bazel build //<component>:runtime_inventory.json` | Runs once for all discovered Java components. Bazel may download artifacts that are absent from its caches, using the repositories and checksums locked by the root Java dependency hub. | Use `--language` for a non-Java slice. |
| Generated runtime inventory JSON | Read locally from `bazel-bin`. Provides the resolved coordinate, name, project URL, and license metadata used by the report. | - |

Set `BAZEL_OUTPUT_USER_ROOT` to reuse a specific Bazel cache location. Set
`COLLECT_DEPS_BAZEL` only when the Bazelisk executable has a different name or
absolute path.

### Helm

Helm dependency discovery is local. The tool reads `Chart.lock` first so resolved versions win over version ranges in `Chart.yaml`. If there is no lockfile, it falls back to the top-level `dependencies` block in `Chart.yaml`.

| What | External / toolchain behavior | Disable |
|------|------------------------------|---------|
| `Chart.lock`, `Chart.yaml` | Local files only | - |
| `helm show chart` | Subprocess. Reads chart metadata for HTTP and OCI chart repositories. For HTTP repos the tool uses an isolated temporary Helm repo config and cache, so it does not mutate your normal Helm settings. | `COLLECT_DEPS_NO_HELM_SHOW=1` |
| GitHub REST API | Fallback only when chart metadata has no license hint and `home` or `sources` points to `github.com/owner/repo`. Reuses the same `GITHUB_TOKEN` or `GH_TOKEN` behavior as Go. | `COLLECT_DEPS_NO_GITHUB=1` |

Without `helm` on `PATH`, Helm dependency rows are still generated from local chart manifests but license cells stay blank. Unauthenticated GitHub fallback can hit rate limits quickly. Export `GITHUB_TOKEN` or `GH_TOKEN` if you want better coverage.

## Optional: `go mod vendor` for Go licenses

License text comes from checked-in `vendor/` (via `vendor/modules.txt`). To create or refresh `vendor/` before the rollup you need `go` on `PATH`, module download access, and `GOPRIVATE` or `GONOSUMDB` if you use private modules:

```bash
# Only modules that do not already have vendor/modules.txt
COLLECT_DEPS_GO_VENDOR=missing go run -C ./tools/collect-dependencies .

# Every discovered go.mod directory (slow; rewrites vendor/)
COLLECT_DEPS_GO_VENDOR=1 go run -C ./tools/collect-dependencies .
```

This writes under Go module trees. Choose in Git whether to commit `vendor/`,
many upstreams already do.

`go mod vendor` uses the same module proxy and VCS behavior as other `go` commands. For `go list`, `go mod download`, and GitHub lookups during license fill, see [External APIs and network calls](#external-apis-and-network-calls).

## License resolution limitations

| Language | Source | Caveats |
|----------|--------|---------|
| Go | `vendor/…`, `go list … all`, `go mod download`, GitHub `/license` | Order, hosts, and flags: [External APIs and network calls](#external-apis-and-network-calls). Module-cache `LICENSE` often matches [pkg.go.dev](https://pkg.go.dev) for public modules. |
| Rust | `cargo metadata`, then crates.io | Workspace roots from `cargo locate-project --workspace`. Match `-` and `_` in names. Graph must resolve (`--locked` when `Cargo.lock` exists). crates.io may omit or combine licenses. Git-only or path crates may be missing on crates.io. |
| Python | PyPI JSON (`license_expression`, `license`, classifiers) | Skip unpinned or non-PyPI specs (`@ git`, local paths). Older projects may lack metadata. |
| Node.js | `pnpm-lock.yaml`, then npm registry package-version metadata | The pnpm lockfile is the resolved source of truth. Workspace links and local files are excluded. Private or unavailable packages may remain unresolved. |
| Java | Bazel-generated component runtime inventories | Results cover dependencies reachable from registered component runtime roots. Test-only tools and unused artifacts in the shared hub are intentionally excluded. Missing license metadata remains unresolved and must be reviewed at the component inventory source. |
| Helm | `helm show chart`, then GitHub `/license` for `home` or `sources` repo URLs | License fields are not standardized in `Chart.yaml`. Some charts expose `annotations.licenses` or Artifact Hub annotations, others do not. The GitHub fallback reflects the chart source repo license, which is often correct but not guaranteed to be a chart-package specific declaration. Public NGC or other non-GitHub sources may stay blank. |

## Development notes

- Implementation lives in `main.go` with tests in `main_test.go`.
- Run `go test ./...` from `tools/collect-dependencies` after behavior changes.
- Build all Java inventories directly with:

  ```bash
  bazel build \
    //src/libraries/java/nv-boot-parent:runtime_inventory.json \
    //src/control-plane-services/<service>:runtime_inventory.json
  ```

- If you change generated header strings or caveat text, update `main_test.go` and any checked-in generated docs that intentionally track those strings.
