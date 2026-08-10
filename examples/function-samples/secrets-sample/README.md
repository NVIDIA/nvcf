# Secrets Sample
## Build the sample container
```bash
docker buildx build --platform linux/amd64,linux/arm64 -t secrets-sample .
```
Push the image to an OCI registry your self-hosted NVCF cluster can access and register pull credentials with `nvcf-cli registry add`. See [examples/README.md](../../README.md#publishing-container-images) for the full flow.

## Invoke the sample locally
Run the container:
```bash
docker run -it -p 8000:8000 -v ${PWD}/secret-sample:/var/secrets secrets-sample
```

Then invoke to retrieve a specific secret:

```bash
curl --request POST \
  --url localhost:8000/test \
  --header 'Content-Type: application/json' \
  --data '{
  "key": "secret-key-1"
}'
```

Or retrieve all secrets with a blank request:

```bash
curl --request POST \
  --url localhost:8000/test \
  --header 'Content-Type: application/json' \
  --data '{}'
```

### Error Handling
The server gracefully handles missing or empty secret files by:
- Returning an empty dictionary for missing/empty files
- Logging errors to help with debugging
- Continuing to serve requests without crashing

If a secret file is not found or empty, you'll see log entries like:
```
ERROR:http_server:Secret file not found at /var/secrets/accounts-secrets.json
```

The API will still return a 200 OK response with empty dictionaries for any missing secrets.

## Invoke the sample on self-hosted NVCF
Resolve the cluster gateway and generate an invocation API key via `nvcf-cli`:

```bash
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
export NVCF_API_KEY=$(nvcf-cli api-key generate --description "secrets-sample" --json | jq -r '.keys[] | select(.service=="function") | .apiKey')
```

Call the function through the gateway, routing with the `Host` header:

```bash
curl --request POST \
  --url "http://${GATEWAY_ADDR}/test" \
  --header "Host: <function-id>.invocation.${GATEWAY_ADDR}" \
  --header "Authorization: Bearer ${NVCF_API_KEY}" \
  --header "Content-Type: application/json" \
  --data '{
  "key": "secret-key-1"
}'
```
