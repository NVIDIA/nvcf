# OpenBao server provenance

The image replaces the server binary from the upstream runtime image with a
locally compiled OpenBao binary. The runtime filesystem, entrypoint, default
configuration, user, and command remain from `openbao/openbao:2.6.3`.
The runtime image is pinned to multi-architecture manifest digest
`sha256:a60afafda36337abe833c4a63894bf1095098f29abea4091e7e555a33dd52889`.
The Go 1.27.3 Alpine 3.23 builder is pinned to multi-architecture manifest digest
`sha256:8a5046497cda39860b832ffdbe8dd66018a0f05bb01352f533a36e7e2ced66a0`.

## Source

The build uses the official `openbao-dist-v2.6.3.tar.xz` release asset. That
asset includes the generated web UI used by upstream release binaries.

- Version: `v2.6.3`
- Source commit: `63a65e6b907589dbb952c371a70260a065bf8bd7`
- Source SHA-256: `471a2c2e3a156a833704f43335ed5e66476a90719ce692b80c3f541e346cda69`
- License: MPL-2.0

`scripts/build-openbao.sh` verifies the source checksum before extracting it.
The MPL-2.0 license remains in the upstream runtime image at
`/licenses/mozilla.txt`.

## Dependency floors

The source build enforces these module and toolchain floors before compiling
the server:

- Go 1.27.3
- `golang.org/x/crypto v0.56.0`
- `google.golang.org/grpc v1.83.2`
- `github.com/moby/go-archive v0.3.0`

Go minimal version selection also updates the transitive modules required by
those versions. The build runs `go mod tidy` and `go mod verify`, then compiles
with the upstream `ui` build tag and release version metadata.

`scripts/verify-openbao.sh` asserts both target architectures and the three
dependency floors against the module metadata embedded in each binary.
The final image requires Alpine `ca-certificates` and
`ca-certificates-bundle` 20261020-r0 or newer. Run
`scripts/test-runtime-packages.sh` with `RUNTIME_IMAGE` to verify the installed
packages, check that the affected emSign root is absent from Go-loaded CA paths,
and inspect the server and target plugin binaries copied into the image.
