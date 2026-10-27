# API Keys Service

NVIDIA API Keys service manages the lifecycle of API keys (issue,
authenticate, introspect, rotate, revoke).

## Synchronous bulk operations

The trusted management boundary submits `PUT` requests to:

- `/v2/api-keys/key-owner/users/{userId}/key-operations`
- `/v2/api-keys/account-admin/accounts/{ncaId}/key-operations`
- `/v2/api-keys/platform-admin/key-operations`

Set `operation` to `REVOKED` or `DELETE`. Each dimension not fixed by the route
requires an `accounts`, `users`, or `issuerServices` array of distinct IDs or
the singleton `["*"]`. Platform requests selecting all three wildcards also
require `confirm_platform_wide: true`. Configure the array limit with
`apikeys.bulk-max-selector-values` (default 10).

HTTP 200 returns `KeyOperationResponse` after processing finishes. Selection
uses the submission time as its creation cutoff. Processing stops on failure,
and problem details include `bulkOperation` with `FAILED` status and counts
for keys visited so far. `partialApplication` indicates that a mutation was
attempted. Earlier writes remain applied. Retries start a new selection with
a new cutoff.

Bulk mutations clear this instance's key and introspection caches before the
response, including after partial failure. Other replicas expire cached key
rows after 60 seconds and cached introspection responses after 60 seconds.
These layers can retain a successful decision for up to 120 seconds after a
mutation, plus in-flight request time. Existing caller-side HTTP caching can
add another 360 seconds for introspection or 60 seconds for evaluation.

## Minimum Requirements

- [Eclipse Temurin OpenJDK 25](https://adoptium.net/temurin/releases/)
- [Bazelisk](https://github.com/bazelbuild/bazelisk)
- [Docker](https://docs.docker.com/get-docker/)

## Development Environment

### Build from command-line

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/api-keys/...
```

See [BAZEL.md](BAZEL.md) for the monorepo-native Bazel build, tests, coverage,
NOTICE, executable jar, and Docker workflow.

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

### Run the service from command-line

Once the service is built successfully, you can run it from the
command-line:

1. Set up Cassandra:

    ```bash
    docker compose \
      -f src/control-plane-services/api-keys/local_env/docker-compose.yml \
      up -d
    ```

   Cassandra runs on `localhost:9042` with the `nvcf_api_keys`
   keyspace pre-loaded from `local_env/cassandra/schema/`. Local
   secrets (Cassandra credentials, JWE keys, service
   registrations) are read from `local_env/vault/secrets.json`.

2. Run the service with the `local` profile:

    ```bash
    java -Dspring.profiles.active=local \
      -jar bazel-bin/src/control-plane-services/api-keys/app.jar
    ```

The service uses the following ports:

- HTTP/REST endpoints are exposed on port 8080.
- Management/Actuator endpoints are exposed on port 9090.

Actuator / management port is typically not exposed to the load balancer.

The `/health` endpoint is also exposed on the main HTTP port without authentication.

The component `NOTICE` is generated from the Bazel executable runtime. Use the
commands in [BAZEL.md](BAZEL.md). Do not restore source-repository NOTICE
generation commands in this imported subtree.

## Run the app from IntelliJ IDEA

1. Open the monorepo root as a Bazel project.
2. Open `Settings` > `Build, Execution, Deployment` > `Build Tools` >
   `Bazel`.
3. Set `Project View Path` to
   `<monorepo-root>/tools/intellij/.managed.bazelproject` and sync the
   project.
4. Run `@//src/control-plane-services/api-keys:app_run` from the Bazel tool
   window or the gutter beside `App.main()`.
5. Open the generated `Bazel` run configuration. Keep `Run with Bazel`
   selected.
6. Add these values to `CLI arguments to your application`, replacing the
   example root with the absolute path to this checkout:

   ```text
   --jvm_flag=-Dspring.profiles.active=local
   --jvm_flag=-Dnv-boot.reloadable-properties.file=file:/absolute/path/to/nvcf/src/control-plane-services/api-keys/local_env/vault/secrets.json
   ```

Bazel runs the application from its runfiles tree. The absolute secrets path
keeps the external file outside Bazel while making it available to Spring.
Do not set `-Duser.dir`; it breaks Bazel's relative Java classpath.
