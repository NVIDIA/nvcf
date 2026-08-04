# JWT plugin provenance

The `vault-plugin-secrets-jwt` binaries in this directory are build output.
They are not committed; `.gitignore` excludes them and only `.gitkeep` ships.

## Source

`../../plugins/vault-plugin-secrets-jwt` in this repository. The plugin is a
modified copy of the Apache-2.0 project `outfoxx/vault-plugin-secrets-jwt`;
that directory's `NOTICE` enumerates every NVIDIA change.

The image build compiles the plugin from that source in a Dockerfile build
stage, so the binary and the image come from the same commit. Nothing is
fetched from outside this repository at build time.

## Dependency floors

Held deliberately, not incidental to a `go mod tidy`:

- `golang.org/x/net v0.55.0` - security floor
- `github.com/hashicorp/vault/api v1.15.0`
- `github.com/hashicorp/vault/sdk v0.15.2`
- `google.golang.org/grpc v1.69.4`
- `github.com/go-jose/go-jose/v4 v4.0.4`

The vault and grpc pins keep compatibility with the previously shipped plugin
binary. `scripts/verify-jwt-plugin.sh` asserts them against the built artifact.

## Local build

    scripts/build-jwt-plugin.sh    # writes both arch binaries here
    scripts/verify-jwt-plugin.sh   # asserts module path, target, toolchain, deps

Hashes are reported by the verifier rather than pinned: any source change in
this repository legitimately changes them.
