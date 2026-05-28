# Generic HTTP Function Invocation

HTTP invocation executes an inference request against a deployed Cloud Functions
function through the invocation service. Use this page for standard HTTP
request and response workloads, multipart or binary-style payloads, and HTTP
streaming with Server-Sent Events (SSE).

<Note>
If you invoke a function without specifying a function version ID, and multiple
versions are deployed, Cloud Functions can route the request to any deployed
version for that function.

</Note>

For gRPC functions, see [gRPC Function Invocation](./grpc-function-invocation.md).

For HTTP examples on this page, see [HTTP Invocation](#http-invocation) and
[HTTP Streaming](#http-streaming).

## Invocation Path

HTTP invocation enters through the public invocation endpoint and is handled by
Invocation Service. The worker request path delivers the request to the
selected customer function and returns the response through Invocation Service.

![HTTP invocation path](images/nvcf-http-invocation-path.svg)

### Multi-Cluster View

In a global deployment, DNS or a custom front door selects a regional HTTP
invocation endpoint. Each region keeps its own Invocation Service, NATS worker
request path, and customer HTTP function placement. The cross-cluster line shows
the NATS chatter that supports regional request-path state when configured.

![HTTP multi-cluster invocation path](images/nvcf-http-multicluster-invocation.svg)

## HTTP Invocation

HTTP invocation uses the invocation route exposed by your gateway. In self-hosted
deployments, requests usually go to the gateway load balancer and use the `Host`
header for routing:

```bash
export GATEWAY_ADDR=<gateway-address>
export FUNCTION_ID=<function-id>
export API_KEY=<api-key>
```

Invoke a function endpoint by using the function ID as the wildcard subdomain in
the `Host` header:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/echo" \
  --header "Host: ${FUNCTION_ID}.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{"message": "hello"}'
```

With production DNS and TLS, the same request can use the DNS hostname directly:

```bash
curl --request POST \
  --url "https://${FUNCTION_ID}.invocation.<domain>/echo" \
  --header "Authorization: Bearer ${API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{"message": "hello"}'
```

Function routes preserve endpoint paths and query parameters:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/echo?name=John" \
  --header "Host: ${FUNCTION_ID}.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{"name": "John"}'
```

You can also send multipart or binary-style payloads to a custom function
endpoint:

```bash
curl --location --request PUT \
  --url "http://${GATEWAY_ADDR}/my-cool-endpoint?abc=123" \
  --header "Host: ${FUNCTION_ID}.invocation.${GATEWAY_ADDR}" \
  --header "Accept: application/octet-stream" \
  --header "Authorization: Bearer ${API_KEY}" \
  --form 'metadata="123"' \
  --form 'file=@"/path/to/cool-file.zip"'
```

<Note>
Cloud Functions uses HTTP/2 persistent connections. For best performance, keep
client connections open until the client no longer needs to communicate with the
server.

</Note>

Size multipart and binary requests against your gateway, load balancer, and
function container limits.

## Long-Running HTTP Invocation

Cloud Functions supports long-running inference over HTTP/2 for synchronous HTTP
invocations. Use this mode when a function can take longer than a typical idle
connection window to produce a response.

The client must keep the HTTP/2 connection healthy while it waits. Configure the
client to send HTTP/2 PING frames during idle response periods. Cloud Functions
does not send those client-side PING frames for you, and enabling HTTP/2 alone
is not enough if the client library does not send PING frames while it waits.

Long-running HTTP/2 invocation uses the same request payload, function hostname,
and `Authorization` header as other HTTP invocations. Set the client request
timeout higher than the expected inference duration and test with the same client
runtime used in production.

HTTP/2 PING frames are connection-level frames. They are not HTTP headers, JSON
fields, or request body data.

### Go Clients

Go clients can configure HTTP/2 keepalive behavior with
`golang.org/x/net/http2`. Set `ReadIdleTimeout` to the maximum idle period before
the client sends a health-check PING frame. Set `PingTimeout` to the time the
client waits for the PING acknowledgement before it closes the connection.

```go
base := &http.Transport{
	TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12,
	},
}

h2, err := http2.ConfigureTransports(base)
if err != nil {
	return err
}

h2.ReadIdleTimeout = 30 * time.Second
h2.PingTimeout = 10 * time.Second

client := &http.Client{
	Transport: base,
	Timeout:   30 * time.Minute,
}
```

Choose timeout values that match your workload and network path. A shorter
`ReadIdleTimeout` sends PING frames more often. A very short interval can add
unnecessary connection traffic.

### Python Clients

Python clients that use `httpx`, including clients built on the OpenAI Python
SDK, can enable HTTP/2, but the standard `httpx` transport does not expose
HTTP/2 PING keepalive settings.

For long-running invocations that can stay idle while the function runs, use a
Python transport that can send HTTP/2 PING frames or contact NVIDIA Support for
the recommended Python workaround. Increasing the request timeout without
HTTP/2 PING support can still leave the connection vulnerable to idle connection
closures.

### curl

The `curl` command line can help verify basic HTTP connectivity, but do not use
it as the only validation tool for long-running idle invocations. It does not
provide a practical way to send HTTP/2 PING frames while waiting for a response.

Use a production client library that can configure HTTP/2 PING behavior when you
validate long-running inference.

## HTTP Streaming

HTTP streaming lets a function return an event stream to the client. The client
uses the same invocation endpoint and sends `Accept: text/event-stream`.

### Prerequisites

- A deployed Cloud Functions function.
- A function endpoint that can return `Content-Type: text/event-stream`.
- Familiarity with [Server-Sent Events (SSE)](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events).

### Client Request

The client initiates streaming by making a request with
`Accept: text/event-stream`:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/echo" \
  --header "Host: ${FUNCTION_ID}.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${API_KEY}" \
  --header "Accept: text/event-stream" \
  --header "Content-Type: application/json" \
  --data '{
    "messages": [
      {
        "role": "user",
        "content": "Hello"
      }
    ],
    "temperature": 0.2,
    "top_p": 0.7,
    "max_tokens": 512
  }'
```

If the inference container response includes `Content-Type: text/event-stream`,
Cloud Functions keeps the client connection open and forwards events from the
container response.

<Note>
The worker reads events from the inference container for up to the global request
timeout, or until the inference container closes the connection. Do not create an
infinite event stream. If the client disconnects, the worker eventually times out
and closes the request.

</Note>

Streaming reduces latency by sending data as it becomes available, avoids polling,
and lets the inference container decide whether a given response should be
streamed.

For the streaming request sample on this page, see [Client Request](#client-request).

## Statuses and Errors

Direct HTTP invocation returns the status, headers, and body produced by your
inference container. If your container returns an error, clients receive the
container status code and response body.

For consistent client handling, return JSON from your inference container and
set `Content-Type: application/json`. Example:

```json
{
  "error": "invalid datatype for input message"
}
```

Cloud Functions adds invocation headers such as `nvcf-reqid` and `nvcf-status`
when a request is accepted by the invocation service. Platform-generated errors
can still use the platform error response format. For platform API behavior, see
[API](./api.md).

<Warning>
Emit logs from your inference container so invocation failures can be diagnosed.
See [Observability](./observability.md) and [Troubleshooting](./troubleshooting.md)
for logging and debugging guidance.

</Warning>

### Common Invocation Errors

| Failure type | Description |
| --- | --- |
| Invocation response returns 4xx or 5xx | Inspect the response body and container logs. Direct HTTP invocation forwards container-generated errors. For platform-generated errors, see [API](./api.md). |
| Invocation request takes a long time to return | Check function capacity to see if the request is queued. Consider adding container metrics and logs. Set the `NVCF-POLL-SECONDS` header to a longer value, up to the deployment limit, to rule out client polling issues. |
| Invocation response returns 401 or 403 | Verify that the `Authorization` header is set to a valid API key with invocation permissions. |
| Container out of memory | Profile memory usage locally and check container logs for out-of-memory evidence. For PyTorch workloads, see the [PyTorch GPU memory guide](https://pytorch.org/blog/understanding-gpu-memory-1/). |
| Invocation response returns 504 | No worker picked up the request within the polling timeout window. Increase `NVCF-POLL-SECONDS` if the request should remain queued longer. |
