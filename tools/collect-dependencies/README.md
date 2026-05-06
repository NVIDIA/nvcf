# collect-dependencies

This guide is for using and developing `go run ./tools/collect-dependencies`.

For broader compliance context, review workflow, and how `dependencies.md` fits with `NOTICE` and source headers, see [`../../license-compliance.md`](../../license-compliance.md).

## Basic usage

From the repo root:

```bash
go run ./tools/collect-dependencies
# The generator refreshes `dependencies.md`, which now includes Java.
# One language only (faster). This rewrites `dependencies.md` with just
# that slice, so run the full command before committing the shared rollup.
go run ./tools/collect-dependencies --language go
go run ./tools/collect-dependencies -l rust
go run ./tools/collect-dependencies -l python
go run ./tools/collect-dependencies -l java
go run ./tools/collect-dependencies -l helm
```

## Outputs

- [`../../dependencies.md`](../../dependencies.md): Shared rollup for Go modules, Rust crates, Python packages, Java Maven coordinates, and Helm chart dependencies. Entries are deduped and grouped by normalized license expression. Each bullet keeps a language tag, and MPL groups keep the explicit version (`MPL-1.0`, `MPL-1.1`, `MPL-2.0`) in the heading.
- [`../../dependencies-java.md`](../../dependencies-java.md): Existing Java-only grouped snapshot kept at the repo root for reference. The generator no longer rewrites this file. Java dependencies now flow into `../../dependencies.md`.

Regenerate when imported trees or their dependency manifests change.

## External APIs and network calls

The tool reads local files first (`go.mod`, `go.sum`, `vendor/`, `Cargo.toml`, Python manifests, Helm charts). The tables below list the HTTPS and subprocess calls used after that for version or license lookup. `go`, `cargo`, and `helm` may add their own traffic (module proxy, git, crates index, chart registries) depending on cache and graph. Those tools must be on `PATH` when those steps run.

### Go

| What | External / toolchain behavior | Disable |
|------|------------------------------|---------|
| Manifests and `vendor/` | Local files only | - |
| `go list -mod=mod -m -json all` (once per discovered `go.mod` root) | Resolves the module graph; reads `LICENSE*` under each module `Dir` in `$GOMODCACHE`. May use proxy.golang.org (or `GOPROXY`) and VCS (for example GitHub) like any `go` build. | `COLLECT_DEPS_NO_GO_LIST=1` |
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

### Java (Maven)

For versionless `pom.xml` dependencies, the tool prefers `repo1.maven.org` `maven-metadata.xml` to learn the current `release` or `latest`, and falls back to [search.maven.org](https://search.maven.org/) Solr only if metadata is unavailable.

| What | External / toolchain behavior | Disable |
|------|------------------------------|---------|
| `repo1.maven.org` | `GET` of `…/{groupId path}/{artifactId}/maven-metadata.xml` for third-party coordinates that lack a version (prefer `<release>`, then `<latest>`). Also fetches `…/{artifact}-{version}.pom` to read `<licenses>`. If the module POM omits `<licenses>`, the tool follows the `<parent>` chain on Central. | `COLLECT_DEPS_NO_MAVEN_CENTRAL=1` |
| `search.maven.org` Solr API | Fallback only when `maven-metadata.xml` is unavailable, still used to learn a version for third-party coordinates without one. Optional throttle: `COLLECT_DEPS_MAVEN_THROTTLE_SEC` (default `0.05`). | `COLLECT_DEPS_NO_MAVEN_CENTRAL=1` |

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
COLLECT_DEPS_GO_VENDOR=missing go run ./tools/collect-dependencies

# Every discovered go.mod directory (slow; rewrites vendor/)
COLLECT_DEPS_GO_VENDOR=1 go run ./tools/collect-dependencies
```

This writes under synthetic import trees. Choose in Git whether to commit `vendor/`, many upstreams already do.

`go mod vendor` uses the same module proxy and VCS behavior as other `go` commands. For `go list`, `go mod download`, and GitHub lookups during license fill, see [External APIs and network calls](#external-apis-and-network-calls).

## License resolution limitations

| Language | Source | Caveats |
|----------|--------|---------|
| Go | `vendor/…`, `go list … all`, `go mod download`, GitHub `/license` | Order, hosts, and flags: [External APIs and network calls](#external-apis-and-network-calls). Module-cache `LICENSE` often matches [pkg.go.dev](https://pkg.go.dev) for public modules. |
| Rust | `cargo metadata`, then crates.io | Workspace roots from `cargo locate-project --workspace`. Match `-` and `_` in names. Graph must resolve (`--locked` when `Cargo.lock` exists). crates.io may omit or combine licenses. Git-only or path crates may be missing on crates.io. |
| Python | PyPI JSON (`license_expression`, `license`, classifiers) | Skip unpinned or non-PyPI specs (`@ git`, local paths). Older projects may lack metadata. |
| Java | Maven Central POM `<licenses>` via `repo1.maven.org`, Solr on `search.maven.org` when version is missing | `com.nvidia.*` is treated as internal, so there is no Maven Central license lookup. Third-party rows use the module POM and then parent POMs on Central until `<licenses>` appears. Some artifacts still omit licenses everywhere in the chain, or Solr `latestVersion` may not match your BOM. |
| Helm | `helm show chart`, then GitHub `/license` for `home` or `sources` repo URLs | License fields are not standardized in `Chart.yaml`. Some charts expose `annotations.licenses` or Artifact Hub annotations, others do not. The GitHub fallback reflects the chart source repo license, which is often correct but not guaranteed to be a chart-package specific declaration. Public NGC or other non-GitHub sources may stay blank. |

## Development notes

- Implementation lives in `main.go` with tests in `main_test.go`.
- Run `go test ./...` from `tools/collect-dependencies` after behavior changes.
- If you change generated header strings or caveat text, update `main_test.go` and any checked-in generated docs that intentionally track those strings.
