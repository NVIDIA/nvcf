# Cloud Functions

Multi-module repository for REST APIs and gRPC worker APIs
for NVIDIA Cloud Functions (NVCF) service. NVCF manages the
lifecycle of inferencing workloads on GPU-powered worker nodes.

## Modules

| Module                        | Description                                     |
|-------------------------------|-------------------------------------------------|
| [nvcf-core](nvcf-core/)       | Core module with business logic                 |
| [nvcf-service](nvcf-service/) | Spring Boot executable depending on `nvcf-core` |

## CI/CD

The root GitHub workflow builds and tests Cloud Functions from Bazel source
targets. See [BAZEL.md](BAZEL.md) for CI selection and artifact details.

## Minimum Requirements

- [Eclipse Temurin OpenJDK 25](https://adoptium.net/temurin/releases/)
- [Bazelisk](https://github.com/bazelbuild/bazelisk)
- [Docker](https://docs.docker.com/get-docker/)

## Development Environment

### Build from command-line

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/cloud-functions/...
```

#### TestContainers Failing on Linux

On Linux, if Bazel tests fail because
TestContainers are not starting, you may see an error like:

```
ContainerLaunchException: Timed out waiting for container port to open
```

This can happen if the `Userland Proxy` service has been
disabled. Enable it in `/etc/docker/daemon.json`:

```json
{
  "userland-proxy": true
}
```

By default this service is enabled and should be left enabled for Java TestContainers
to work on Linux.

### Run NVCF Service from command-line

Once the service is built successfully, you can run the service from the command-line:

1. Set up Cassandra, NATS, and LocalStack:

    ```bash
    docker compose \
      -f src/control-plane-services/cloud-functions/local_env/docker-compose.yml \
      up -d
    ```

   This allows us to run Cassandra, NATS, and AWS Localstack locally.

2. Run the service from the command line with the `local` profile:

    ```bash
    java -Dspring.profiles.active=local \
      -jar bazel-bin/src/control-plane-services/cloud-functions/nvcf-service/app.jar
    ```

The service uses the following ports:

- HTTP/REST endpoints are exposed on port 8080.
- gRPC endpoints are exposed on port 9090.
- Management/Actuator endpoints are exposed on port 8181.

Actuator / management port is typically not exposed to the load balancer.

The `/health` endpoint is also exposed on the main HTTP port without authentication.
