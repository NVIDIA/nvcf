# OCI capability layer

Package Linux executable capabilities as deterministic OCI tar metadata.
The build tool runs on the host; do not call setcap or require host root.
Only explicitly supported capabilities may be packaged. No runtime dependency
is added to the image.

Run from the repository root:

```sh
bazel test //tools/oci-capability-layer/...
```

Use the Go standard library and gofmt. Test metadata, unchanged executable
bytes, reproducibility, and invalid inputs. Verify actual container startup
with the requested capability bound and no-new-privileges before changing
the packaging contract.
