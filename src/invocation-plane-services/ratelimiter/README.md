# NVCF Rate Limiter

A rate limiting service for NVIDIA Cloud Functions (NVCF) that controls the rate of invocation requests to NVCF functions based on configurable policies. This service helps prevent abuse and ensures fair resource allocation by limiting the number of requests that can be made to specific functions.

## Build with Bazel

Bazel is the canonical build path. The legacy Dockerfile + `go build`
flow stays available for dev iteration outside Bazel.

```shell
# Build everything Bazel knows about.
bazel build //...

# Run all tests with auto-retry on timing-sensitive failures.
bazel test //... --flaky_test_attempts=3

# Build the multi-arch OCI image index (linux/amd64 + linux/arm64).
bazel build //:image_index

# Push to the internal NGC registries (kaze / nv-ngc-devops / ncp-dev).
bazel run //:image_push
bazel run //:image_push_devops
bazel run //:image_push_ncp_dev

# Regenerate per-package BUILD files after Go source changes.
bazel run //:gazelle

# Refresh module graph after go.mod changes.
bazel mod tidy
```

The image is published from CI on the default branch. Local pushes
need an `nvcr.io` docker login in the active `DOCKER_CONFIG`.

## Overview

The NVCF Rate Limiter provides:

- Per-function rate limiting based on NCA ID, function ID, and version ID
- NCA ID (NVIDIA Cloud Account ID) based exclusions
- Per NCA ID rate limiting for a function (New feature introduced in 2025/10. Each NCA ID can have its own limit. When a specific NCA ID rate in perNcaIdRate is specified, it will take precedence over the original global rateLimit field. Users can also use either global rateLimit(old behavior) or perNcaIdRate, dont have to provide both.)
- GRPC interface for integration with Invocation Service and gRPC Proxy Service
- Configurable rate limits with in-memory storage
- Authentication using JWT

The service uses the [ulule/limiter](https://github.com/ulule/limiter) library to implement rate limiting functionality. It maintains a TTL cache that maps function version IDs to rate limiter metadata, including the rate limiter instance and any excluded NCA IDs.
Rate Limiter Service also leverages [buraksezer/olric](https://github.com/buraksezer/olric) for in store memory for multi instance support.

## RateLimiter Interaction with Invocation Service/gRPC Proxy 
```plantuml
participant "Invocation Service/gRPC Proxy" as client
participant "NVCF Ratelimiter" as service

client -> client: check in memory flag for this NCA ID
group if in memory flag is set
    client -> service: grpc request (inline sync check)
    service -> client: grpc response
    group if grpc response is ALLOW
        client -> client: clear the in memory flag for this NCA ID
        client -> client: allow the request
    end group
    group if grpc response is DISALLOW
        client -> client: disallow the inflight request
        note right
            After an inline check with rate limiter service,
            the request for this particular NCA ID and function should not go through.
        end note          
    end group
end group

group if in memory flag is not set
    client -> client: allow the inflight request          
    client -> service: grpc request (background async check)
    service -> client: grpc response
    group if grpc response is DISALLOW
        client -> client: mark a flag in memory for this NCA ID
    end group
    note right
        Always allow the request go through no matter what, but mark
        in memory flag for this NCA ID and function so the future requests 
        can be rate limited
    end note
end group
```

## RateLimiter Architecture
```plantuml
participant "NVCF Ratelimiter" as service

service -> service: validate incoming gRPC request (NCA ID, Function ID, Function Version ID)
service -> service: load the applicable rate-limit policy for the function version
group Multi-level policy evaluation
    alt per-NCA-ID rate is configured for this caller
        service -> service: apply the caller-specific per-NCA-ID rate
        service -> service: skip the global policy for this caller
    else no per-NCA-ID override exists
        service -> service: apply the global function-version rate
        service -> service: auto-allow callers in the global excluded NCA ID list
    end
end
service -> service: apply ulule/limiter using Olric-backed shared counters
note right
        Logical counter key:
        NCA ID + ":" + Function Version ID + ":" + Rate String

        Shared Olric counters make the configured limit
        consistent across all rate-limiter pods.
end note
service -> service: construct ALLOW / DISALLOW response and send gRPC response
```

## Configuration

The service can be configured through environment variables:

- `OAUTH2_ISSUER`: OAuth2 issuer URL for inbound JWT validation (JWKS discovery)
- `AUDIENCE`: Expected JWT audience (`aud`) for inbound requests
- `OAUTH2_PROVIDER_HOST`: Hostname for OAuth2 token endpoint when using client-credentials for the outbound NVCF gRPC client
- `OTEL_EXPORTER_OTLP_ENDPOINT`: Endpoint for OpenTelemetry traces
- `TRACING_ACCESS_TOKEN`: Token for tracing access
- `SECRETS_PATH`: Path to secrets JSON (`id` / `secret` for OAuth2, or `nvcfApiToken` for a static bearer token)
- `POD_IP`: IP address of the pod
- `AWS_REGION`: AWS region
- `NVCF_API_URL`: URL of the NVCF API
- `CACHE_TTL`: Time-to-live for cache entries (in seconds)

GRPC Client credentials are provided by either OAuth2 Client Credentials or a bearer token loaded from the secrets.json file.
OAuth2 Client Credentials should be provided at the json key "id" and "secret".
Alternatively, bearer token credentials should be provided at the json key "nvcfApiToken".

### Secrets File

```json
{
  "id": "<OAuth2 client id>",
  "secret": "<OAuth2 client secret>",
  "nvcfApiToken": "<static bearer token, optional>",
  "tracingAccessToken": "<observability vendor access token, optional>"
}
```

## Upstream Usage Examples

This is how the NVCF Invocation Service and NVCF GRPC Proxy are triggered. Invoking functions which have rate limiting enabled will cause those services to call the NVCF Rate Limiter.

### GRPC Rate Limiting in Staging

```bash
grpcurl -v -H "Authorization: Bearer <token>" \
-H "function-id:163a784e-b3bf-4724-b3b1-0b2a873a9410" \
-H "function-version-id: 6954a4b5-256c-40a8-8711-bf4050125996" \
-d '{"message": "test"}' \
stg.grpc.nvcf.nvidia.com:443 Echo/EchoMessage
```

### HTTP Rate Limiting in Staging

```bash
curl --location 'https://1f7c8647-c8c5-4792-9339-108d831dadb5.invocation.stg.api.nvcf.nvidia.com/echo' \
--header 'Accept: application/json' \
--header 'Content-Type: application/json' \
--header 'Authorization: Bearer <token>' \
--data '{
 "message": "{}",
 "repeats": 0,
 "stream": false,
 "delay": 0
}'
```

## Documentation

- [Service Requirements Document](https://docs.google.com/document/d/1aCeqdD_A5F5ZVb0YGlwLE_TaV6UT06JOJM4JLjJREfE/edit?pli=1&tab=t.0#heading=h.dpy7eqe3c3pv)
- [Service Design Document](https://docs.google.com/document/d/1UeGwRAx0-gxR0Ft3OFcsG9a1nzI4K20ivQRvEL_ARtA/edit?usp=sharing)
- [Grafana Dashboard](https://nvcf-grafana.thanos.nvidiangn.net/d/aecnvpsz0dszkc/nvcf-rate-limiter?orgId=1)
- [Lighstep Dashboard](https://app.lightstep.com/nvidia-prod/dashboard/nvcf/z2cqkKR9?time_window=days_1&selected_group_id=1TYVBJ48)
- [Kratos Logs](https://obs.kratos.nvidia.com/explore?schemaVersion=1&panes=%7B%22c6v%22%3A%7B%22datasource%22%3A%22de6udhluln3swd%22%2C%22queries%22%3A%5B%7B%22refId%22%3A%22A%22%2C%22expr%22%3A%22%7Bsource%3D%5C%22app%5C%22%7D%22%2C%22queryType%22%3A%22range%22%2C%22datasource%22%3A%7B%22type%22%3A%22loki%22%2C%22uid%22%3A%22de6udhluln3swd%22%7D%2C%22editorMode%22%3A%22builder%22%2C%22direction%22%3A%22backward%22%7D%5D%2C%22range%22%3A%7B%22from%22%3A%22now-1h%22%2C%22to%22%3A%22now%22%7D%7D%7D&orgId=138)

## Development

### Building the Service

```bash
go build ./...
```

### Running Tests

```bash
go test ./...
```

### Docker Build

```bash
docker build -t nvcf-ratelimiter .
```
