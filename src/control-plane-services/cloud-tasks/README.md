# NVIDIA Cloud Tasks

Multi-module repository for REST APIs and gRPC worker APIs
for NVIDIA Cloud Tasks (NVCT) service. NVCT manages the
lifecycle of run-to-completion workloads, such as model
fine-tuning, TensorRT engine builds, and batch inference,
on GPU-powered worker nodes.

## License

This project is licensed under the
[Apache License 2.0](../../../LICENSE). A complete attribution of bundled
third-party libraries, including each direct and transitive runtime
dependency, its version, and its license, lives in [NOTICE](NOTICE).
Regenerate it from the monorepo root with:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run //src/control-plane-services/cloud-tasks:generate_notice -- --write
```

Re-run after any change to dependencies and commit the updated `NOTICE`
alongside the dependency change.

See the monorepo [contribution guide](../../../CONTRIBUTING.md), including the
DCO sign-off requirement.

## Modules

| Module | Description |
|---|---|
| [nvct-core](nvct-core/) | Core module with business logic |
| [nvct-service](nvct-service/) | Spring Boot executable depending on `nvct-core` |

## Minimum Requirements

- [Eclipse Temurin OpenJDK 25](https://adoptium.net/temurin/releases/)
- [Bazelisk](https://github.com/bazelbuild/bazelisk)
- [Docker](https://docs.docker.com/get-docker/)

## Development Environment

### Build from command-line

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/cloud-tasks/...
```

Run all commands from the monorepo root. See [BAZEL.md](BAZEL.md) for detailed
build, test, coverage, NOTICE, executable jar, and container workflows.

#### TestContainers Failing on Linux

On Linux, if Bazel tests fail because
TestContainers are not starting, you may see an error like:

```text
ContainerLaunchException: Timed out waiting for container port to open
```

This can happen if the `Userland Proxy` service has been
disabled. Enable it in `/etc/docker/daemon.json`:

```json
{
  "userland-proxy": true
}
```

By default this service is enabled and should be left
enabled for Java TestContainers to work on Linux.

### Run NVCT Service from command-line

Once the service is built successfully, you can run the
service from the command-line:

1. Set up Cassandra:

    ```bash
    docker compose \
      -f src/control-plane-services/cloud-tasks/local_env/docker-compose.yml \
      up -d
    ```

   This starts Cassandra locally.

2. Run the service from the command line with the `local` profile:

    ```bash
    java -Dspring.profiles.active=local \
      -jar bazel-bin/src/control-plane-services/cloud-tasks/nvct-service/app.jar
    ```

The service uses the following ports:

- HTTP/REST endpoints are exposed on port 8080.
- gRPC endpoints are exposed on port 9090.
- Management/Actuator endpoints are exposed on port 8181.

Actuator / management port is typically not exposed to the load balancer.

The `/health` endpoint is also exposed on the main HTTP port without authentication.
