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
- NCA ID (NVIDIA Cloud Account ID) based exclusions for the global rate
- Per NCA ID rate limiting for a function (introduced 2025/10). Each NCA ID can have its own limit; when a specific NCA ID rate in `perNcaIdRate` is configured, it replaces the global `rateLimit` for that NCA. Function owners can use either the global `rateLimit` or `perNcaIdRate` (or both)
- Per user (caller) rate limiting (introduced 2026/04). A single `perUserRate` is configured on the function version and applied **independently** against every unique caller — counters are keyed by `clientAuthSubject`, the per-caller identity the upstream service resolves from the request's credentials. Enforced **in addition to** the NCA-tier limit. A request with an empty `clientAuthSubject` skips the user tier; callers that share one `clientAuthSubject` share one counter
- Multiple rates per entry (e.g. `"5-S,300-H"`). Every rate within an entry must allow; rates with the same time window are deduplicated to the stricter limit
- GRPC interface for integration with Invocation Service and gRPC Proxy Service
- Configurable rate limits with Olric-backed shared counters across pods
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

service -> service: validate incoming gRPC request (NCA ID, Function ID, Function Version ID required; clientAuthSubject optional)
service -> service: load the applicable rate-limit policy for the function version
group Build the set of tiers that apply to this request
    alt clientAuthSubject is non-empty AND perUserRate is configured
        service -> service: include the per-user tier (counters scoped to this clientAuthSubject)
    end
    alt a perNcaIdConfigs entry exists for this NCA
        service -> service: include the per-NCA-ID tier
    else no per-NCA-ID override exists
        service -> service: include the global tier (skipped when this NCA is in excludedNcaIds)
    end
end
service -> service: AND-check every tier — every tier's counter is incremented and must allow
service -> service: apply ulule/limiter using Olric-backed shared counters
note right
        Olric counter keys (one per rate string per tier):
        NCA tier:  NCA ID + ":" + Function Version ID + ":" + Rate
        User tier: "user:" + clientAuthSubject + ":" + NCA ID +
                   ":" + Function Version ID + ":" + Rate

        Shared Olric counters make the configured limit
        consistent across all rate-limiter pods.
end note
service -> service: construct ALLOW / DISALLOW response and send gRPC response
```

## How rate limits compose

A request is **allowed** only when every counter that applies to it is under
its limit. Counters are organized along two orthogonal axes:

1. **Tier** — which scope of limit applies (per-user, per-NCA-ID, global).
2. **Rate** — within a tier's rate string, multiple rates may be configured
   (e.g. `"5-S,300-H"`); each rate is its own counter.

### Tier selection

Three tiers can apply to a request:

- **Per-user tier** — enforced when the function-version has a `perUserRate`
  *and* the request carries a caller identity (`clientAuthSubject`). Each
  distinct `clientAuthSubject` gets an independent counter. See
  [Caller identity (clientAuthSubject)](#caller-identity-clientauthsubject) for
  how that value is determined and when the tier activates.
- **Per-NCA-ID tier** — when `perNcaIdConfigs` has an entry for the request's
  `ncaId`, that per-NCA rate is used and the global rate is **not** evaluated.
- **Global tier** — the function-version's `rate`, used when no per-NCA-ID entry
  matches. Bypassed when the `ncaId` is listed in `excludedNcaIds`.

The tiers compose with **AND** semantics: every tier that applies must allow.
The NCA axis contributes exactly one tier (per-NCA-ID *or* global, never both);
the per-user tier, when active, is additive on top. `excludedNcaIds` only carves
the listed NCAs out of the **global** rate — it does not affect the per-NCA-ID
or per-user tiers, which remain enforced whenever they are configured.

### Caller identity (clientAuthSubject)

Every rate-limit request carries an `ncaId` (the account the call runs under) —
it is mandatory and the service rejects any request that omits it.
`clientAuthSubject` is an optional, per-caller identity that the upstream service
resolves from the request's credentials and forwards to the limiter. The limiter
treats it as an opaque key — it does not care how the identity was obtained (it
can come from a per-user delegation token, a per-user API key, or any other
credential that carries a caller identity).

How the value shapes the per-user tier:

- **Unique per caller** → each caller gets its own per-user counter — true
  per-user limiting (a credential that identifies an individual caller).
- **Shared by many callers** → those callers share one per-user counter, so the
  per-user tier effectively becomes a per-credential / account-wide limit (a
  shared service or account-level credential resolves to a single identity).
- **Empty** → the per-user tier is skipped entirely; only the NCA-axis tier
  applies (the legacy, no-caller-identity behavior).

The per-user counter is also scoped by `ncaId` (the Olric key includes it, see
below), so the same identity under different accounts is tracked separately.

A request is evaluated against **at most two tiers**:

```
allowed  =  [ user tier   (only if perUserRate is configured) ]
       AND  [ exactly ONE NCA-axis tier:  per-NCA-ID  OR  global ]
```

The NCA axis picks **one** tier, never both: if `perNcaIdConfigs` has an entry
for this `ncaId` the per-NCA-ID rate is used and the global rate is **not**
evaluated; otherwise the global rate applies. The user tier is **additive** —
it never replaces the NCA tier, it AND-gates alongside it.

| `perUserRate` set? | per-NCA-ID match for this `ncaId`? | global `rate` set? | Tiers actually checked |
|---|---|---|---|
| ✓ | ✓ | ✓ | **user + per-NCA-ID** (global skipped) |
| ✓ | ✗ | ✓ | **user + global** |
| ✓ | ✗ | ✗ | **user only** |
| ✗ | ✓ | ✓ | per-NCA-ID only (global skipped) |
| ✗ | ✗ | ✓ | global only (bypassed if `ncaId` in `excludedNcaIds`) |
| ✗ | ✗ | ✗ | none → always allowed |

Note that `excludedNcaIds` only removes the **global** gate; a per-user or
per-NCA-ID tier stays enforced for an excluded NCA whenever it is configured.

### Multiple rates within a tier

Within any single tier, the rate string may list several rates separated by
commas (`"5-S,300-H"`). Each rate is its own Olric counter, parsed by
`parseRates`. A request must pass **every** rate in the tier; counters are
all incremented on every evaluation regardless of outcome (so windows stay
in sync).

### Worked example

Policy on a function-version:

```yaml
rate: "10-S,1000-H"            # global rate, two windows
perUserRate: "5-S,500-H"       # per-user tier, two windows applied per caller
excludedNcaIds: ["nca_X"]      # global rate carve-out
```

The user-tier rate is applied independently against every unique
`clientAuthSubject`; each caller has its own counter.

| Request | Counters checked | Outcome |
|---|---|---|
| `clientAuthSubject=user_a`, `ncaId=nca_Y` | user_a `5-S` + user_a `500-H` + global `10-S` + global `1000-H` | Allowed only if all four are under limit |
| `clientAuthSubject=user_b`, `ncaId=nca_Y` | user_b `5-S` + user_b `500-H` + global `10-S` + global `1000-H` (user_b has its own user-tier counter) | Allowed only if all four are under limit |
| `clientAuthSubject=user_a`, `ncaId=nca_X` | user_a `5-S` + user_a `500-H` only (global skipped because `nca_X` is excluded) | User-tier still enforced |
| `clientAuthSubject=""`, `ncaId=nca_X` | none (global skipped, no user tier without clientAuthSubject) | Always allowed |

Note that `user_a` and `user_b` **share** the same `global` counters for
`nca_Y` (the global key is `ncaId:functionVersionId:rate`, with no caller
component) — only their user-tier counters are independent. So the two callers
compete for the one global `10-S` / `1000-H` budget while each still gets its
own `5-S` / `500-H` user budget.

### Olric counter key layout

| Tier | Key |
|---|---|
| Per-user | `"user:" + clientAuthSubject + ":" + ncaId + ":" + functionVersionId + ":" + rate` |
| Per-NCA-ID / global | `ncaId + ":" + functionVersionId + ":" + rate` |

The `"user:"` prefix keeps user-tier counters in a separate namespace so they
never collide with NCA-tier counters that share the same NCA / function /
rate.

## Configuration

The service can be configured through environment variables:

- `OAUTH2_ISSUER`: Expected `iss` claim on inbound JWTs
- `OAUTH2_JWKS_URL`: URL the validator fetches signing keys from (set explicitly so the binary can run against any OAuth2/OIDC provider, regardless of where it serves keys)
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

### GRPC Rate Limiting

```bash
grpcurl -v -H "Authorization: Bearer <token>" \
-H "function-id: <function-id>" \
-H "function-version-id: <function-version-id>" \
-d '{"message": "test"}' \
<grpc-endpoint>:443 Echo/EchoMessage
```

### HTTP Rate Limiting

```bash
curl --location 'https://<function-id>.invocation.<your-domain>/echo' \
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
