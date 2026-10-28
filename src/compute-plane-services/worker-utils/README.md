# worker-utils

NVCF worker service that runs alongside the user's inference container. It polls
NVCF for invocation requests, forwards and streams them to the user container,
streams the responses back, reports progress, and handles request cancellation
and spot-instance termination.

## Build

The binary is built with Bazel:

```bash
bazel build //src/compute-plane-services/worker-utils/cmd:cmd
```

## Test

```bash
# Run unit tests via Bazel
bazel test //src/compute-plane-services/worker-utils/...

# Or with the Go toolchain, from this directory
go test ./...
```

## Callback connection preparation

`NVCF_CALLBACK_PRECONNECT_ENABLED=true` enables bounded first-hop connection
setup alongside inference. The default is disabled. The normal authenticated
callback POST and streaming protocol are unchanged. Preparation sends no HTTP
request. Ready sockets expire after 30 seconds; setup is limited to 2 seconds
and 16 speculative entries per utility. Expiration or failure falls back to
normal dialing. A bounded cooldown permits at most one preparation attempt
per route each second. This does not change the 5-minute transport idle timeout.

See [the design](CALLBACK_PRECONNECT_DESIGN.md) and
[test plan](CALLBACK_PRECONNECT_TEST_PLAN.md) for ownership, proxy support,
limitations and local validation commands.
Use [the local setup guide](CALLBACK_PRECONNECT_LOCAL_SETUP.md) for the Bazel
path and the isolated Go/container test runner. Callback destinations come
from each work message; preparation keeps different targets and proxies
separate. The flag is per utility and should remain off for workloads that
have not been qualified.
