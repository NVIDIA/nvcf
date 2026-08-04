# OSS dependency scanning

How to enumerate third-party dependencies in this repository for source
scanning. Written for Black Duck / Pulse, but the queries are tool-agnostic.

Every command here was run against a clean checkout and returns the stated
result. An empty result can be legitimate for a filtered or language-specific
query. Report non-zero exits, with the full command output.

## Scope: what `//...` covers today

`//...` does not cover the whole repository. Services that still carry their own
nested `MODULE.bazel` are listed in `.bazelignore`, and Bazel cannot load them
from the repository root:

    bazel query //... | wc -l          # 509 targets

Those 509 are the root-owned targets, `nvcf-cli`, `nvsnap`, and the Java
components. Eighteen services are excluded. A scan of `//...` therefore reports
a minority of first-party targets, and a clean result understates coverage
rather than proving the repository is clean.

To scan an excluded service, run from its own directory:

    cd src/compute-plane-services/nvca && bazel query //...

This is being addressed by consolidating every service into the root module.
When that lands, `//...` covers everything and the per-directory step goes away.

## Queries by language

`kind(j.*import, ...)` matches `jvm_import` only. There is no `go_import` or
`rust_import` rule, so that pattern silently returns nothing for Go and Rust.

Everything, all languages. Use `query`, not `cquery`, and understand why:

    bazel query 'deps(//...)' --output=label | sort -u

`query` runs at the loading phase and takes every branch of every `select()`, so
it reports what the repository could depend on under any configuration. That
conservative superset is what a license or vulnerability scan wants.

`cquery` runs after analysis and resolves `select()` against a configuration, so
it drops branches that configuration does not take. It is the better tool when
you need to know what a specific build actually pulls in, and the worse one for
scanning. Neither output contains the other:

| | labels |
| --- | --- |
| `bazel query 'deps(//...)'` | 19668 |
| `bazel cquery 'deps(//...)'` | 30155 |
| present in `query`, absent from `cquery` | 2293 |

`cquery` returns more in total because it emits a row per configured target, and
the same label can appear under host, exec and target configurations. It still
misses 2293 labels that `query` sees, mostly C++ dependencies reachable only
through branches no configuration selects here.

The language filters below use `cquery` deliberately: `kind()` matches on rule
class, which is only accurate after analysis.

Java third-party jars:

    bazel cquery --noimplicit_deps 'kind("jvm_import", deps(//...))' \
      --output=label | sed 's/ (.*)//' | sort -u

Go third-party modules:

    bazel cquery --noimplicit_deps 'kind("go_library", deps(//...))' \
      --output=label | sed 's/ (.*)//' \
      | grep '^@@gazelle++go_deps+' \
      | sed 's|^@@gazelle++go_deps+\([^/]*\)//.*|\1|' | sort -u

Rust crates, from the nested module that owns them:

    cd src/libraries/rust/stargate
    bazel cquery --noimplicit_deps 'kind("rust_library", deps(//...))' \
      --output=label | sed 's/ (.*)//' | grep '^@@rules_rust' \
      | sed -E 's|^@@rules_rust\+\+crate\+stargate_crates__||; s|//.*||' | sort -u

## Versions

Bazel labels carry no version. `com_github_sirupsen_logrus` does not say which
release it is. For anything version-sensitive, ask the module extension:

    bazel mod show_repo @@gazelle++go_deps+com_github_sirupsen_logrus

which prints `importpath`, `version` and `sum`.

For Go and Rust specifically, a scanner's native `GO_MOD` and `CARGO` detectors
read `go.mod` and `Cargo.lock` directly and give versions without any of this.
Prefer them. The Bazel detector earns its keep on Java, where the dependency set
is not otherwise enumerable from a single file.

## macOS

Two `cc_binary` targets under `src/compute-plane-services/nvsnap/lib` are Linux
only. Exclude them explicitly:

    bazel cquery --noimplicit_deps \
      'deps(//... except //src/compute-plane-services/nvsnap/lib/...)' \
      --output=label

A `target_compatible_with` constraint would not help here. It causes
`bazel build //...` to skip a target, but neither `query` nor `cquery` applies
incompatible-target skipping: both still list the target. That is separate from
how the two treat `select()`, described above. Any dependency scan sees them
either way, so the exclusion has to be explicit.

## Running in a container

The repository mounted at a path other than its own root is fine, but three
things commonly break:

Writable cache. Bazelisk writes to `$HOME` before Bazel starts. With a read-only
or foreign-UID home it fails with `could not create directory`, which is easy to
mistake for a Bazel error:

    HOME=/tmp BAZELISK_HOME=/tmp/bz bazelisk --output_user_root=/tmp/ob query '//...'

Memory. `.bazelrc` sets `startup --host_jvm_args=-Xmx4g`. If the container is
capped below roughly 5 GB the JVM is killed during startup and the only output
is a bare `ERROR:` with no body. If you see that, raise the limit first.

Network. `//...` fetches from the Bazel Central Registry, the Go module proxy,
a Maven mirror, `static.rust-lang.org`, and OCI registries. A network-restricted
container fails at fetch, which surfaces as a query error.

When reporting a failure, include full stderr rather than the `ERROR:` line,
plus `bazel info`, the exit code, and the container memory limit.
