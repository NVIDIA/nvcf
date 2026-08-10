# NV Boot Parent

NV Spring Boot: Shared libraries and Bill of Materials (BOM). The libraries
can be used by both internal/managed and self-hosted application deployments.

**Baseline:** Spring Boot **4.0.7**, Spring Cloud **2025.1.2** (Oakwood), Java **25**.

## Structure

```text
nv-boot-parent  (extends spring-boot-starter-parent 4.0.7)
├── nv-boot-bom        — BOM for nv-boot-starter-* versions
└── nv-boot-starter-*  — shared libraries
```

## Modules

| Module                                                                                   | Description                                                                    |
|------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------|
| [nv-boot-bom](nv-boot-bom/README.md)                                                     | Bill of Materials for nv-boot module versions                                  |
| [nv-boot-mock-servers-test](nv-boot-mock-servers-test/README.md)                         | WireMock-based mock servers for testing                                        |
| [nv-boot-starter-audit](nv-boot-starter-audit/README.md)                                 | Audit logging and event tracking                                               |
| [nv-boot-starter-cassandra](nv-boot-starter-cassandra/README.md)                         | Cassandra with SSL bundle and refreshable session support                      |
| [nv-boot-starter-core](nv-boot-starter-core/README.md)                                   | Core utilities and shared configuration                                        |
| [nv-boot-starter-exceptions](nv-boot-starter-exceptions/README.md)                       | Exception classes with RFC 7807 Problem Details support                        |
| [nv-boot-starter-jwt](nv-boot-starter-jwt/README.md)                                     | JWT authentication and validation starter                                      |
| [nv-boot-starter-data-migration-notification](nv-boot-starter-data-migration-notification/README.md) | Notification service for raising events such as begin or end of data migration |
| [nv-boot-starter-observability](nv-boot-starter-observability/README.md)                 | Tracing, logging, and metrics configuration                                    |
| [nv-boot-starter-registries](nv-boot-starter-registries/README.md)                       | Container, Helm, Model, and Resource registry clients                          |
| [nv-boot-starter-reloadable-properties](nv-boot-starter-reloadable-properties/README.md) | File-based property reloading with context refresh                             |
| [nv-boot-starter-telemetry](nv-boot-starter-telemetry/README.md)                         | CloudEvents client for Telemetry servers (WebClient, OAuth2 bearer)            |

## Using in External Projects

Spring Boot applications should extend `nv-boot-parent` as their Maven parent and import
`nv-boot-bom` in `<dependencyManagement>`. This gives the application:

- All Spring Boot build conventions (compiler, surefire, jacoco, etc.)
- Managed versions for all third-party dependencies (Bouncy Castle, Spring Cloud, etc.)
- Managed versions for all `nv-boot-starter-*` libraries via `nv-boot-bom`

### Basic setup

```xml
<parent>
    <groupId>com.nvidia.boot</groupId>
    <artifactId>nv-boot-parent</artifactId>
    <version>${get.latest.version}</version>
</parent>

<dependencyManagement>
    <dependencies>
        <dependency>
            <groupId>com.nvidia.boot</groupId>
            <artifactId>nv-boot-bom</artifactId>
            <version>${get.latest.version}</version>
            <type>pom</type>
            <scope>import</scope>
        </dependency>
    </dependencies>
</dependencyManagement>
```

Once declared, nv-boot libraries and third-party dependencies can be used without specifying
a version:

```xml
<dependencies>
    <dependency>
        <groupId>com.nvidia.boot</groupId>
        <artifactId>nv-boot-starter-jwt</artifactId>
    </dependency>
    <dependency>
        <groupId>org.bouncycastle</groupId>
        <artifactId>bcprov-jdk18on</artifactId>
    </dependency>
</dependencies>
```

### Projects that cannot extend nv-boot-parent

If your project already has a corporate or framework parent POM and cannot extend
`nv-boot-parent`, import `nv-boot-bom` in your `<dependencyManagement>` block instead.
This gives managed versions for all `nv-boot-starter-*` libraries, but does **not** provide the
build conventions or third-party version management that come with the full parent:

```xml
<dependencyManagement>
    <dependencies>
        <dependency>
            <groupId>com.nvidia.boot</groupId>
            <artifactId>nv-boot-bom</artifactId>
            <version>${get.latest.version}</version>
            <type>pom</type>
            <scope>import</scope>
        </dependency>
    </dependencies>
</dependencyManagement>
```

Third-party dependencies used by the nv-boot libraries (e.g. Bouncy Castle, etc.)
will still be resolved transitively, but their versions will not be centrally managed — you will
need to manage them explicitly if you use them directly in your own code.

### Overriding a dependency version

All third-party dependency versions are declared as properties in `nv-boot-parent`. Because
external applications extend `nv-boot-parent`, any version property can be overridden in the
application's own `<properties>` block. This is the recommended approach for pulling in a patched
version to address a CVE without waiting for an `nv-boot-parent` release:

```xml
<properties>
     <bouncycastle.version>1.85</bouncycastle.version>
     ...
</properties>
```

The overridden property takes effect for that dependency wherever it is used — both directly in
the application and transitively through any nv-boot library on the classpath.

### Available version properties

- **`nv-boot-parent`** (this project) — properties listed in the table below
- **`spring-boot-starter-parent`** — build and plugin version properties (e.g. `maven-compiler-plugin.version`)
- **`spring-boot-dependencies`** — all Spring Boot managed dependency versions 
     (e.g. `spring-framework.version`, `jackson-bom.version`, `logback.version`). See the
     [Spring Boot dependency versions reference](https://docs.spring.io/spring-boot/appendix/dependency-versions/properties.html)
     for the full list

Properties defined in `nv-boot-parent`:

| Property                    | Controls                                                    |
|-----------------------------|-------------------------------------------------------------|
| `bouncycastle.version`      | `bcpkix-jdk18on`, `bcprov-jdk18on`                          |
| `commons-io.version`        | `commons-io`                                                |
| `commons-lang3.version`     | `commons-lang3`                                             |
| `commons-logging.version`   | `commons-logging`                                           |
| `commons-text.version`      | `commons-text`                                              |
| `cloudevents.version`       | `cloudevents-core`, `cloudevents-json-jackson`              |
| `guice.version`             | `com.google.inject:guice`                                   |
| `shedlock.version`          | `net.javacrumbs.shedlock:*`                                 |
| `spring-cloud.version`      | `org.springframework.cloud:*`                               |
| `springdoc-openapi.version` | `springdoc-openapi-starter-*` (Boot 4 / OpenAPI 3.x line)   |
| `wiremock.version`          | `wiremock`, `wiremock-standalone`                           |

## Minimum Requirements

* [Eclipse Temurin OpenJDK 25](https://adoptium.net/temurin/releases/)
* [Maven 3.8.7](https://maven.apache.org/download.cgi) or higher
* [Git 2.15.2](https://git-scm.com/downloads) or higher
* [Docker](https://docs.docker.com/get-docker/)

## Building

```bash
mvn clean verify
```

Use `mvn clean install` when you only need artifacts locally without the full verify lifecycle.

Bazel is available during migration. See [BAZEL.md](BAZEL.md) for build, test,
coverage, NOTICE, and downstream source-consumption commands.

## Third-party notices

The repository root **`NOTICE`** file lists shipped third-party dependencies and
their declared licenses. It is generated by the Bazel-native NOTICE tool from
`tools/bazel/notice_roots.json`, `maven_install.json`, and checked-in metadata;
it does not depend on project POM files or `license-maven-plugin`.

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nv-boot-parent-bazel-cache"
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  run //:generate_notice -- --update-metadata --write
```

License metadata is only as accurate as upstream artifact metadata; review with
Legal/OSRB before release.
