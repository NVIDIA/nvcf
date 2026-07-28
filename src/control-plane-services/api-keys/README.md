# API Keys Service

NVIDIA API Keys service manages the lifecycle of API keys (issue,
authenticate, introspect, rotate, revoke).

## Minimum Requirements

- [Eclipse Temurin OpenJDK 25](https://adoptium.net/temurin/releases/)
- [Maven 3.8.7](https://maven.apache.org/download.cgi) or higher
- [Docker](https://docs.docker.com/get-docker/)

## Development Environment

### Build from command-line

```bash
cd src/control-plane-services/api-keys
mvn clean package
```

See [BAZEL.md](BAZEL.md) for the monorepo-native Bazel build, tests, coverage,
NOTICE, executable jar, and Docker workflow.

#### TestContainers Failing on Linux

On Linux, if tests fail during `mvn clean package` because
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

By default this service is enabled and should be left
enabled for Java TestContainers to work on Linux.

### Run the service from command-line

Once the service is built successfully, you can run it from the
command-line:

1. Set up Cassandra:

    ```bash
    cd src/control-plane-services/api-keys/local_env
    docker compose up
    ```

   Cassandra runs on `localhost:9042` with the `nvcf_api_keys`
   keyspace pre-loaded from `local_env/cassandra/schema/`. Local
   secrets (Cassandra credentials, JWE keys, service
   registrations) are read from `local_env/vault/secrets.json`.

2. Run the service with the `local` profile:

    ```bash
    cd src/control-plane-services/api-keys
    java -Dspring.profiles.active=local -jar target/app.jar
    ```

The service uses the following ports:

- HTTP/REST endpoints are exposed on port 8080.
- Management/Actuator endpoints are exposed on port 9090.

Actuator / management port is typically not exposed to the load balancer.

The `/health` endpoint is also exposed on the main HTTP port without authentication.

The component `NOTICE` is generated from the Bazel executable runtime. Use the
commands in [BAZEL.md](BAZEL.md). Do not run the standalone Maven NOTICE
generator in this imported subtree.
