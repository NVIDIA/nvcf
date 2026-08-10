Notes:
- Multi instances of the same container will be spun up. 
- One must realize it is the "head" node and run the main server entrypoint.
- The rest of the nodes only require running a health/readiness endpoint as defined in the helm chart
- The main head node will run `mpirun` and connect to the other nodes to coordinate.
- $HOSTFILE contains the names of the other replicas (including the head node) for use with `mpirun`

## Building the Container

The base image is configurable via the `BASE_IMAGE` build argument to support different environments.

**AWS** (default):
```
docker build -t multi-node-test .
```

**NCP**:
```
docker build \
  --build-arg BASE_IMAGE=ghcr.io/coreweave/nccl-tests:13.0.2-devel-ubuntu22.04-nccl2.29.2-1-d73ec07 \
  -t multi-node-test .
```

| Environment | Base Image |
|-------------|-----------|
| AWS | `public.ecr.aws/hpc-cloud/nccl-tests:latest` |
| NCP | `ghcr.io/coreweave/nccl-tests:13.0.2-devel-ubuntu22.04-nccl2.29.2-1-d73ec07` |
