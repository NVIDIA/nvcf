# NVCF Invocation Service

## Build with Bazel

Release images are built from the component's published source tag. Follow
[monorepo release automation](../../../docs/dev/github-release-process.md) for versioning and maintenance releases.
Local image builds do not publish a release.

Requires [bazelisk](https://github.com/bazelbuild/bazelisk) (`bazel` on
PATH delegating to the version pinned in `.bazelversion`).
`MODULE.bazel` pulls the Rust toolchain (1.91.1), `rules_rust`,
`crate_universe`, `rules_oci`, and the distroless/cc base image; no
host toolchains are needed beyond a recent Linux (or macOS) and the
`bazel` shim.

```shell
# Build everything Bazel knows about.
bazel build //...

# Run the unit test. --flaky_test_attempts=3 lets timing-sensitive
# tests self-heal instead of needing a manual retry.
bazel test //... --flaky_test_attempts=3

# Build the multi-arch OCI index for the server binary.
bazel build //crates/server:image_index

# Load the host-arch image into the local docker daemon for smoke tests.
bazel run //crates/server:image_load

# Repin crate_universe after Cargo.lock changes.
CARGO_BAZEL_REPIN=1 bazel sync --only=nvcf_invocation_crates
```

OSS contributors building from the GitHub mirror: the default
`oci.pull` in `MODULE.bazel` points at `nvcr.io/nvidia/distroless/cc`,
which requires NGC credentials. To build off-network, swap that entry
for a public base such as `gcr.io/distroless/cc-debian12` (then
`bazel mod tidy` to refresh the lockfile). `bazel build //...` and
`bazel test //...` work without modification.

Local Bazel cache setup is documented in the `nvcf/nvcf-internal` docs.

## Legacy route access

The legacy exec/pexec handlers delegate bearer-token validation and function
authorization to the control plane. New polling records bind the authenticated
tenant and subject that started the invocation. A replacement token for the same
identity can continue polling. Mismatched or malformed ownership metadata is
rejected in both rollout stages.

The invocation service stores caller identity in the NATS message header
`Nvcf-Http-Invocation-Owner`. The shared `WorkerResultTracking` protobuf payload
is preserved so other services can continue decoding the same message.

Use two releases for rolling and cross-region upgrades:

1. Deploy with `enforce_polling_owner: false` (the default) to every region and
   update every producer of polling records to write ownership metadata. This
   preparation release accepts headerless records after the existing token and
   function authorization checks. Intermediate polls preserve their original
   expiry and never assign them to the polling caller.
2. Wait more than 60 minutes after the last headerless writer is upgraded, or
   longer if the tracking stream's retention has been increased. Enable
   `enforce_polling_owner: true` in a subsequent release across every region.
   Headerless records then return 404. Do not enable enforcement while any
   producer, including a legacy NVCF producer, still writes headerless records.

The counter `nvcf_http_invocation_polling_owner_checks_total` has bounded
`outcome` labels: `matched`, `legacy_accepted`, `missing_rejected`,
`invalid_rejected`, and `mismatch_rejected`. Watch increments of
`legacy_accepted` to find remaining legacy polls. Full caller ownership
enforcement requires the second release; preparation retains the old
authorization behavior for headerless records.

Browser access to legacy routes requires an explicit list of exact HTTP or HTTPS
origins in the service configuration. The default list is empty. Cookie
credentials are not enabled; callers send their bearer token explicitly.

```yaml
cors_allowed_origins:
  - https://console.example.com
enforce_polling_owner: false
```

Environment configuration uses comma-separated `CORS_ALLOWED_ORIGINS` and the
boolean `ENFORCE_POLLING_OWNER`. For Helm, use
`invocation.cors.allowedOrigins` and `invocation.polling.enforceOwnership`.
Configure approved browser origins before deploying the preparation release.

The transparent load-balancer routes continue to forward CORS requests to the
workload, which owns their browser policy.

## Worker callback TLS

`worker_tls` adds a second listener that serves the same routes over TLS, so
workers can verify this pod end to end through a CONNECT proxy. It is off by
default.

```yaml
worker_tls:
  enabled: true
  port: 8443
  cert_path: /etc/invocation-service/worker-tls/tls.crt
  key_path: /etc/invocation-service/worker-tls/tls.key
  reload_interval_seconds: 30
```

The environment equivalents are `WORKER_TLS__ENABLED`, `WORKER_TLS__PORT`,
`WORKER_TLS__CERT_PATH`, `WORKER_TLS__KEY_PATH` and
`WORKER_TLS__RELOAD_INTERVAL_SECONDS`.

- The certificate must cover the per-pod callback names, for example
  `*.invocation.<namespace>.svc.cluster.local`.
- To advertise this listener, set `WORKER_STREAM_PROPERTIES__SERVICE_ADDRESS`
  to `https://invocation.<namespace>.svc.cluster.local:8443`. Workers then call
  `https://<dashed-pod-ip>.invocation.<namespace>.svc.cluster.local:8443` and
  trust the CA from `NVCF_PROXY_CA_PEM` or `NVCF_PROXY_CA_FILE`.
- The files are re-read every `reload_interval_seconds`. A renewed pair is
  served without a restart. If the new pair can't be read or the key doesn't
  match, the current certificate stays in use and a warning is logged.
- A missing or invalid certificate at startup stops the service.

## Cargo development (still supported)

```shell
cargo run --package nvcf-invocation-service --bin server --release -- -c crates/server/resources/settings-stg-local.yaml  2>&1 | grep '^{.*}$' | jq '.'
```

This connects to the staging NVCF System, so you can invoke any function as normal.

## Using ngrok for External Worker Access

To expose your local service externally using ngrok and configure the self address:

```shell
# Start ngrok tunnel in background and run the service with self address
ngrok http 8080 --log=stdout > /tmp/ngrok.log 2>&1 & sleep 2;
export WORKER_STREAM_PROPERTIES__SELF_ADDRESS=$(curl -s http://localhost:4040/api/tunnels | jq -r '.tunnels[0].public_url')
echo "Ngrok URL: $WORKER_STREAM_PROPERTIES__SELF_ADDRESS"
cargo run --package nvcf-invocation-service --bin server -- -c crates/server/resources/settings-stg-local.yaml
```

This command will:

1. Start ngrok in the background to forward port 8080
2. Extract the public ngrok URL
3. Run the service with the `WORKER_STREAM_PROPERTIES__SELF_ADDRESS` config set to the ngrok URL

Alternatively, you can run these steps separately:

```shell
# Step 1: Start ngrok
ngrok http 8080

# Step 2: In another terminal, get the ngrok URL and run the service
export WORKER_STREAM_PROPERTIES__SELF_ADDRESS=$(curl -s http://localhost:4040/api/tunnels | jq -r '.tunnels[0].public_url')
echo "Ngrok URL: $WORKER_STREAM_PROPERTIES__SELF_ADDRESS"
cargo run --package nvcf-invocation-service --bin server -- -c crates/server/resources/settings-stg-local.yaml
```

The ngrok URL will be displayed in the ngrok terminal and can be used to access your service externally.

```shell
curl --location 'http://localhost:8080/echo' \
--header 'Accept: application/json' \
--header 'NVCF-POLL-SECONDS: 10' \
--header 'Content-Type: application/json' \
--header 'Authorization: Bearer ...' \
--data '{
    "message": "What should I see in Paris?"
}'
```

## Diagrams

### NVCF as a Transparent Load Balancer

#### General Blocking Form

```plantuml
participant Client as client
participant "NVCF Invocation Service" as is
participant "Envoy" as envoy
participant NATS as nats
participant "Worker (Utils Container)" as worker
participant "Worker (Inference Container)" as inference
participant S3 as s3
participant "NVCF API" as api

worker -> nats: subscribe to queue rq_${region}_${fv-id}

client -> is: http request

group if auth not cached
    is -> api: auth function
    api --> is: allowed function versions, client auth info
end group

is -> is: encode http request headers and up to 32k of body into protobuf
note right
    the entire request including headers can optionally be omitted from
    the protobuf. if omitted, the worker will fetch all request data
    instead of just the request body with GET attach.
end note
group if more request data is available
    is -> is: add GET attach address and token to protobuf
    group async
        client -> is: stream request data
        is -> is: wait for worker to call GET attach
        is --> envoy: stream request data
        envoy --> worker: stream request data
        note right
            continued from the worker perspective later in the diagram
        end note
    end group
end group

is -> is: add POST attach address and token into protobuf
is -> nats: push request to queue on rq.${region}.${fv-id}.${rq-id}

nats --> worker: encoded http request
worker -> inference: decoded http request
group if more request data is available
    group async
        worker -> envoy: GET attach
        note right
            continuation from the worker perspective
        end note
        envoy -> is: GET attach
        note right
            envoy allows us to direct requests to a particular instance
            of the invocation service
        end note
        is --> worker: stream request data
        worker -> inference: stream request data
    end group
end group

group if transformation mode enabled
    loop
        group if NVCF-POLL-SECONDS reached or progress file detected
            worker -> worker: encode generated 202 http response
        end group
    end loop
end group

inference --> worker: http response header

group if http response is 202
    note over client,is
        The invocation service records the authenticated tenant and subject.
        Records with ownership require the same caller before reaching the worker.
        Preparation accepts headerless records until the enforcement release.
    end note
    loop
        worker -> nats: listen for more http requests for this request id on core-nats subject rq_polling.${rq-id}
        note right
            the current pexec polling api allows us to differentiate
            a polling request from a new request which lets us know when
            to write into the regular request subject vs this polling subject.

            for future full passthrough apis, we could differentiate using a
            request id or cookie in the http header.
        end note
    end loop
end group

group if transformation mode enabled
    worker -> worker: intercept and transform http responses from the inference container
    inference --> worker: read full http response body
    group if large response
        worker -> s3: zip and stream to s3
        worker -> worker: encode 302 http response redirecting to s3
        note left
            if the client calls exec rather than pexec,
            the api is responsible for converting this 302
            response into an exec wrapper body
        end note
    end group
    group if 4xx or 5xx response
        worker -> worker: map error body to problem details format
    end group
    worker -> worker: present transformed http response as the inference response
end group

inference --> worker: inference response
worker -> worker: map response status to metrics event with InvocationStatus
worker -> envoy: POST attach request containing encoded http inference response
envoy -> is: POST attach request containing encoded http inference response
note right
    envoy allows us to direct requests to a particular instance
    of the invocation service
end note
is --> client: http response
worker --> nats: ack work request
```

## Memory Allocator Features

The service supports multiple memory allocator and profiling configurations:

### Default: jemalloc with Profiling Support

The service uses `jemalloc` by default with integrated pprof server for memory profiling:

```shell
cargo run --package nvcf-invocation-service --bin server  # Uses jemalloc + profiling (default)
```

### jemalloc without Profiling

Use jemalloc allocator but disable the pprof HTTP server:

```shell
cargo run --package nvcf-invocation-service --bin server --no-default-features --features jemalloc
```

### Alternative: mimalloc

For high-performance allocation without profiling, you can use `mimalloc`:

```shell
cargo run --package nvcf-invocation-service --bin server --no-default-features --features mimalloc
```

When using both the `jemalloc` and `profiling` features, the service automatically starts a pprof HTTP server on port 6060 with this endpoint:

- `http://localhost:6060/debug/pprof/allocs` - Current heap profile (protobuf format)

Example usage:

```shell
# Get heap profile (protobuf format for go tool pprof)
curl -o heap.pb.gz http://localhost:6060/debug/pprof/allocs

# Analyze with go tool pprof (includes flamegraph generation)
go tool pprof -http=:8080 heap.pb.gz
```

The profiles can be analyzed using tools like `go tool pprof` or flame graph generators.

## Code coverage

Generate a coverage report in a format VS Code can read:

```cargo-tarpaulin
% cargo install cargo-tarpaulin
% cargo tarpaulin --out LCov --output-dir ./coverage
```

Install the Coverage Gutters extension.  From Vscode:

1. Ctl-Shift-p
1. Type then select, `Coverage gutters: watch`

From the code editor, see that covered lines have green sidebars, otherwise red.
To see entire lines highlighted in green and red:

1. Right click the extension and select settings
1. In the settings pane, select `Show line coverage`
1. Open (or re-open) any file
