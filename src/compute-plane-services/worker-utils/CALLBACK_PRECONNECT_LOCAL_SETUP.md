# Local callback preparation development

This guide tests the utility locally. It needs no GPU, Kubernetes cluster,
NVCF credentials, deployed function, or callback gateway. The tests run real
loopback HTTP/TLS sockets and embedded NATS with mock application, asset,
object-storage and telemetry services. These component integration tests do
not validate a complete NVCF deployment.

## Repository build path

Follow the root [local setup](../../../README.md#local-dev-env-setup),
[Bazel guide](../../../BAZEL.md), and [contributing guide](../../../CONTRIBUTING.md).
Install Bazelisk; `.bazelversion` selects the required Bazel release.
From the repository root:

```sh
bazel build //src/clis/nvcf-cli:nvcf-cli \
  --remote_cache= --remote_executor= --remote_upload_local_results=false
bazel test //src/compute-plane-services/worker-utils/worker/httpstream:httpstream_test \
  //src/compute-plane-services/worker-utils/worker:worker_test \
  //src/compute-plane-services/worker-utils/service:service_test \
  --remote_cache= --remote_executor= --remote_upload_local_results=false \
  --test_output=errors
```

These commands download dependencies and build locally. They do not publish
images or upload remote cache results. Do not use image push targets or the
`remote-write` profile. A toolchain/download failure is not a passing test.

Contributions require maintainer design discussion, focused changes, tests,
docs, Conventional Commits and DCO sign-off.

## Go component runner

With Go 1.26 or newer, from the component directory:

```sh
./test-callback-preconnect.sh callback
./test-callback-preconnect.sh all
```

The runner uses a temporary Go workspace selecting the three local modules.
It runs tests, the callback race suite, vet, and a binary build. It never
starts that binary and removes only its own temporary workspace/binary.
This disposable binary omits VCS stamping so read-only container mounts with
different ownership work. Release builds still use the repository's Bazel path.
Set `NVCF_TEST_GO` to an absolute Go binary path if Go is not on `PATH`.
Dependencies can be downloaded by Go unless `GOPROXY=off` and `GOSUMDB=off`
are set with a populated cache. No module versions or global config change.

The `callback` mode uses ephemeral loopback ports. The `all` mode also runs
existing worker/service mocks on ports 8001, 8002, 9090 and 8360. Use the
container lane below when other local processes occupy them. Do not stop an
unrelated process to free a port.

## Isolated Linux runner

From the repository root, choose a local Go 1.26 image and empty task-specific
cache directories. Record the resolved image ID for reproducibility.
The first run below can fetch public Go dependencies. It mounts no credentials,
Docker socket, or host ports into the container. The source is read-only.

```sh
test_cache=$(mktemp -d "${TMPDIR:-/tmp}/nvcf-callback-cache.XXXXXX")
mkdir "$test_cache/mod" "$test_cache/build"
docker image inspect golang:1.26 --format '{{.Id}}'
docker run --rm --cpus 4 --memory 6g --pids-limit 512 \
  --mount "type=bind,src=$PWD,dst=/src,readonly" \
  --mount "type=bind,src=$test_cache/mod,dst=/go/pkg/mod" \
  --mount "type=bind,src=$test_cache/build,dst=/go-build" \
  --env GOCACHE=/go-build golang:1.26 \
  bash /src/src/compute-plane-services/worker-utils/test-callback-preconnect.sh all
```

If the Docker VM cannot share the host temporary directory, choose a unique
cache directory inside the shared workspace instead. After populating caches,
repeat the same command with `--network none --env GOPROXY=off --env GOSUMDB=off`.
Loopback mocks still work with networking disabled. Preserve logs before
removing only this experiment's cache directory. Never mount production
credentials or point these tests at a live endpoint.

The full module also has tests outside this feature. Report their results
separately in the pull request. Component tests do not qualify all NVCF
workload types.
