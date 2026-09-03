# JWT plugin provenance

The `vault-plugin-secrets-jwt` binaries in this directory are build output.
They are not committed; `.gitignore` excludes them and only `.gitkeep` ships.

## Source

`../../plugins/vault-plugin-secrets-jwt` in this repository. The plugin is a
modified copy of the Apache-2.0 project `outfoxx/vault-plugin-secrets-jwt`;
that directory's `NOTICE` enumerates every NVIDIA change.

The image build compiles the plugin from that source in a Dockerfile build
stage, so the binary and the image come from the same commit. Go module inputs
are verified through the committed `go.sum` checksums.

## Dependency floors

Held deliberately, not incidental to a `go mod tidy`:

- Go 1.27.0 - security floor for the standard library
- `golang.org/x/crypto v0.55.0` - security floor
- `golang.org/x/net v0.57.0` - selected by `golang.org/x/crypto v0.55.0`
- `golang.org/x/text v0.41.0` - selected by `golang.org/x/crypto v0.55.0`
- `google.golang.org/grpc v1.83.1` - security floor
- `github.com/go-jose/go-jose/v4 v4.1.4` - direct JWT/JWS implementation and security floor
- `github.com/hashicorp/vault/api v1.15.0`
- `github.com/hashicorp/vault/sdk v0.15.2`

The Vault pins preserve the previously shipped plugin compatibility contract;
the remaining pins are security floors. `scripts/verify-jwt-plugin.sh` asserts
them against the built artifact.

## Local build

    scripts/build-jwt-plugin.sh    # writes both arch binaries here
    scripts/verify-jwt-plugin.sh   # asserts module path, target, toolchain, deps

Hashes are reported by the verifier rather than pinned: any source change in
this repository legitimately changes them.
