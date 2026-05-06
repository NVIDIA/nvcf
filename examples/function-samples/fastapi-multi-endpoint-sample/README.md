# FastAPI Multi-Endpoint Sample
## Build the sample container
```bash
docker buildx build --platform linux/amd64,linux/arm64 -t fastapi-multi-endpoint-sample .
```
Push the image to an OCI registry your self-hosted NVCF cluster can access and register pull credentials with `nvcf-cli registry add`. See [examples/README.md](../../README.md#publishing-container-images) for the full flow.

## Invoke the sample locally
```bash
curl --request POST \
  --url localhost:8000/echo \
  --header 'Content-Type: application/json' \
  --data '{
  "message": "hello"
}'
```

## Invoke the sample on self-hosted NVCF
Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "fastapi-multi-endpoint-sample" --json | jq -r .apiKey)
```

Call an endpoint directly through the gateway, routing with the `Host` header:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/echo" \
  --header "Host: <function-id>.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
  "message": "hello"
}'
```

You can also dispatch without the function ID in the Host header by providing it as a header (NOTE: experimental):

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/echo" \
  --header "Host: invocation.${GATEWAY_ADDR}" \
  --header "function-id: <function-id>" \
  --header "function-version-id: <version-id>" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
  "message": "hello"
}'
```

Query parameters work the same as for any other gateway call:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/echo?name=John" \
  --header "Host: <function-id>.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
  "name": "John"
}'
```
