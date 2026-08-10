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

| Artifact               | Registry | URL                                                                        |
|------------------------|---|----------------------------------------------------------------------------|
| nvcf-core JAR          | URM | https://urm.nvidia.com/artifactory/sw-nvcf-maven/com/nvidia/nvcf/nvcf-core/ |
| nvcf-service container | NGC | `nvcr.io/nvidian/kaze/nvcf-service-oss:<version>`                          |
| SonarQube              | sonar.nvidia.com | https://sonar.nvidia.com/dashboard?id=GPUSW_DGXC_nvcf-service_nvcf_service |

## Minimum Requirements

- [Eclipse Temurin OpenJDK 25](https://adoptium.net/temurin/releases/)
- [Maven 3.8.7](https://maven.apache.org/download.cgi) or higher
- [Docker](https://docs.docker.com/get-docker/)

## Development Environment

### Build from command-line

```bash
cd ~/Workdir
git clone ssh://git@gitlab-master.nvidia.com:12051/nvcf/nvcf-api/cloud-functions.git
cd cloud-functions
mvn clean package
```

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

By default this service is enabled and should be left enabled for Java TestContainers
to work on Linux.

### Run NVCF Service from command-line

Once the service is built successfully, you can run the service from the command-line:

0. Setup Cassandra DB, NATS, and Localstack
    ```bash
    cd ~/Workdir/cloud-functions/local_env
    docker compose up
    ```
   This allows us to run Cassandra, NATS, and AWS Localstack locally.
   
0. Run the service from command-line using `local` profile:
    ```bash
    cd ~/Workdir/cloud-functions
    java -Dspring.profiles.active=local -jar nvcf-service/target/app.jar
    ```

The service uses following ports:
- **HTTP/REST** endpoints are exposed on port 8080
- **gRPC** endpoints are exposed on port 9090
- **Management/Actuator** endpoints are exposed on port 8181

Actuator / management port is typically not exposed to the load balancer.

The `/health` endpoint is also exposed on the main HTTP port without authentication.
