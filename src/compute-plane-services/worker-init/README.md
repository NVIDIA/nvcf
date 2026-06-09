# init

```shell
docker buildx build --platform "linux/amd64,linux/arm64" --push \
  -t stg.nvcr.io/nv-cf/nvcf-core/nvcf_worker_init:2.2.2 \
  -t nvcr.io/qtfpt1h0bieu/nvcf-core/nvcf_worker_init:2.2.2 \
  -f Dockerfile.nvcf-worker-init .
```
