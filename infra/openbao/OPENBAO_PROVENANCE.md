# OpenBao server provenance

The image replaces the server binary from the upstream runtime image with a
locally compiled OpenBao binary. The runtime filesystem, entrypoint, default
configuration, user, and command remain from `openbao/openbao:2.6.2`.
The runtime image is pinned to multi-architecture manifest digest
`sha256:11fd73a2102cda9c55d5d881a8c3210303146a7ec1e8ac76f526e175c6d24641`.
The Go 1.27.0 Alpine builder is pinned to multi-architecture manifest digest
`sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc`.

## Source

The build uses the official `openbao-dist-v2.6.2.tar.xz` release asset. That
asset includes the generated web UI used by upstream release binaries.

- Version: `v2.6.2`
- Source commit: `dd9c19c37a878cf4a81b18efb8d6f0599c7da923`
- Source SHA-256: `a7784550a9db16f24e99d65a18c9b12a433707c79ef4c1f34262d3f48171c7a9`
- License: MPL-2.0

`scripts/build-openbao.sh` verifies the source checksum before extracting it.
The MPL-2.0 license remains in the upstream runtime image at
`/licenses/mozilla.txt`.

## Dependency floors

The source build updates these modules before compiling the server:

- Go 1.27.0
- `golang.org/x/crypto v0.56.0`
- `google.golang.org/grpc v1.83.2`
- `github.com/moby/go-archive v0.3.0`

Go minimal version selection also updates the transitive modules required by
those versions. The build runs `go mod tidy` and `go mod verify`, then compiles
with the upstream `ui` build tag and release version metadata.

`scripts/verify-openbao.sh` asserts both target architectures and the three
dependency floors against the module metadata embedded in each binary.
