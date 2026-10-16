# Non-root dependency images

Run commands from this directory. These derivatives preserve the pinned
upstream binaries and entrypoints while declaring numeric UID/GID 1000.
Use one producer version for all three images.

- Validate definitions: `docker buildx bake --print`.
- Build both architectures as local OCI archives: `docker buildx bake`.
- Build for local runtime checks: `docker buildx bake --set '*.platform=linux/arm64' --set '*.output=type=docker'`.
- Verify loaded images: `./test.sh`. Use `linux/amd64` on an amd64 host.
- Check shell syntax: `bash -n test.sh`.

Keep upstream versions and digests in sync with the base labels and NOTICE.
Do not replace a base with a newer release as part of a metadata-only fix.
Publication must scan and verify every resulting architecture before consumers
select the new references. Runtime checks must include NATS reload signaling,
JetStream volume ownership, and the Helm hooks that use the Kubernetes helper.
