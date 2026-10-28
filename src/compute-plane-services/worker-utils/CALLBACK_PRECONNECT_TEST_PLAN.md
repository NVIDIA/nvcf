# Callback preparation test plan

Use local listeners, generated test TLS certificates and synthetic tokens.
Do not invoke NVCF, Kubernetes, production inference or external callback URLs.

## Acceptance matrix

| Layer | Scenario | Required evidence |
|---|---|---|
| Unit | Disabled | Original transport behavior, zero speculative dials |
| Unit | Start and coalesce | Caller returns before dial completes, one preparation per route |
| Unit | Capacity | At most 16 parked/in-progress preparations; fallback remains available |
| Unit | Cooldown | Repeated failures cannot cause a preparation storm; retry resumes after cooldown |
| Unit | Isolation | Different target origins and proxy identities cannot share a preparation |
| Unit | Cancellation | Work cancellation, handler close and client close release sockets/dials |
| Unit | Expiration | Expired socket is closed and never handed out |
| Unit | Failure | Failed preparation permits an ordinary dial |
| Unit | Handoff race | One claimant; monitor stopped; deadlines cleared; race detector clean |
| Integration | Direct TLS | TLS handshake completes before simulated inference response; no HTTP bytes early |
| Integration | Authentication | Exactly one POST with the original bearer token after inference is ready |
| Integration | Streaming | First chunk arrives before body production finishes; no added buffering |
| Integration | Buffered | Exact embedded HTTP response status, headers and body survive |
| Integration | Reuse | Later callbacks use the transport idle pool; no POST duplication |
| Integration | HTTPS proxy | Preparation connects to proxy, normal proxy routing and auth preserved |
| Integration | TLS trust | Invalid certificate fails, custom trusted roots succeed; verification never disabled |
| Integration | Peer close | Remote-close during parking causes normal-dial fallback |
| Integration | Long inference | Prepared socket expires; later callback still succeeds |
| Integration | Error status | Callback rejection still propagates normally |
| Integration | Worker lifecycle | Embedded NATS, utility, inference mock and callback complete in both modes with the flag enabled |
| Compatibility | Multiple targets | Concurrent callbacks to two TLS origins keep prepared sockets, tokens and binary payloads on their intended routes, with the flag off and on |
| Compatibility | Proxy changes | Different proxies and different credentials at one proxy cannot consume each other's prepared sockets |
| Compatibility | Existing HTTP modes | Assets, object storage, regional work, credential refresh and both 202 polling paths pass with the flag off and on |
| Compatibility | Stateful bypass | Existing stateful no-peer work remains acknowledged with the flag off and on; successful sessions and task execution require separate qualification |
| Observability | Trace and metrics | Correlated setup/TLS/POST spans, claim event, bounded pre-initialized labels, no token attributes |
| Experiment | Overlap | Controlled setup delay overlaps simulated inference; timestamps show removed setup wait |

Use synchronization channels and connection counts for correctness. The
controlled overlap experiment verifies socket consumption and reports timing
without a wall-clock pass threshold. Record injected delays, sample count,
enabled/disabled medians and limitations.

## Execution

For a repeatable component or isolated-container setup, use
[the local setup guide](CALLBACK_PRECONNECT_LOCAL_SETUP.md) and
`./test-callback-preconnect.sh all`. It creates no live deployment.

From this component, with an appropriate Go toolchain/workspace:

```sh
go test ./worker/httpstream
go test -race -count=10 ./worker/httpstream
go test ./worker ./service
go vet ./worker/httpstream ./worker ./service
git diff --check
```

Update Bazel sources and dependency declarations for new Go files. Run the
Bazel target if a matching local toolchain/dependency setup is available.
Record any unavailable broader test lane explicitly. Full-stack invocation service and live-cluster qualification are outside
these component tests.

## Review before completion

Inspect locking, cancellation, goroutine ownership, TTL races, TLS/SNI and
proxy isolation. Confirm no request body is replayed, no HTTP preflight is
sent, and telemetry excludes URLs/credentials. Record actual test outcomes in the pull request. Distinguish implemented
coverage from future deployment qualification.
