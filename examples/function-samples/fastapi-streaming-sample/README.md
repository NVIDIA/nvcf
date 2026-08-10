# FastAPI Sample
## Build the sample container
```bash
docker buildx build --platform linux/amd64,linux/arm64 -t fastapi-streaming-sample .
```
Push the image to an OCI registry your self-hosted NVCF cluster can access and register pull credentials with `nvcf-cli registry add`. See [examples/README.md](../../README.md#publishing-container-images) for the full flow.

## Invoke the sample locally
```bash
curl --request POST \
  --url localhost:8000/streaming_echo \
  --header 'Content-Type: application/json' \
  --data '{
  "message": "hello there"
}'
```

## Invoke the sample on self-hosted NVCF
Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "fastapi-streaming-sample" --json | jq -r '.keys[] | select(.service=="function") | .apiKey')
```

Request an SSE stream through the gateway, routing with the `Host` header:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/echo" \
  --header "Host: <function-id>.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --header "Accept: text/event-stream" \
  --data '{
  "message": "hello there"
}'
```
