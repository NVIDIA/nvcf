# Helper image producers

Run commands from this directory. These images patch the pinned upstream
Kubernetes toolbox and NATS box while preserving their tools except kubectl. Build kubectl from checksum-pinned
source with the same Go and dependency baseline as the migration producer. Keep numeric
UID/GID 1000, upstream attribution, and writable home/cache under `/tmp`.

- Verify source rejection paths: `./source-test.sh`.
- Validate definitions: `docker buildx bake --print`.
- Build both architectures as local OCI archives: `docker buildx bake`.
- Load one architecture: `docker buildx bake --set '*.platform=linux/arm64' --set '*.output=type=docker'`.
- Verify loaded images: `PLATFORM=linux/arm64 ./test.sh`. Repeat for amd64.
- Check syntax: `bash -n test.sh` and `sh -n patch-brotli.sh scripts/*.sh`.
- Check release registration from repo root: `python3 tools/ci/test-github-release.py`.

Keep base references, OCI labels and NOTICE aligned. Pin each replacement APK
by branch, architecture, version and SHA-256. Never bypass signature checks or
upgrade unrelated packages. Test final package contents and unchanged tool
hashes on both architectures. Publication needs SBOMs, scans and registry
readback before consumers select derivative references.
