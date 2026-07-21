# NV Boot Starter Core

Core utilities and shared configuration for NV Boot applications. Provides defaults,
DNS cache TTL, environment post-processors, health endpoint, and OpenAPI governance.

## Adding as a Dependency

Add the library as a dependency in your application's `pom.xml`:

```xml
<dependencyManagement>
    <dependencies>
        <dependency>
            <groupId>com.nvidia.boot</groupId>
            <artifactId>nv-boot-bom</artifactId>
            <version>${nv-boot.version}</version>
            <type>pom</type>
            <scope>import</scope>
        </dependency>
    </dependencies>
</dependencyManagement>

<dependencies>
    <dependency>
        <groupId>com.nvidia.boot</groupId>
        <artifactId>nv-boot-starter-core</artifactId>
    </dependency>
</dependencies>
```

Depending on the type of web application used, add a dependency on correct spring web starter.

```yaml
spring:
  main:
    web-application-type: [reactive|servlet]
```

MVC/Servlet:
```xml
<dependencies>
    <dependency>
          <groupId>org.springframework.boot</groupId>
          <artifactId>spring-boot-starter-web</artifactId>
    </dependency>
</dependencies>
```

Webflux/Reactive:
```xml
<dependencies>
    <dependency>
          <groupId>org.springframework.boot</groupId>
          <artifactId>spring-boot-starter-webflux</artifactId>
    </dependency>
</dependencies>
```

## Configuration

Configure via `application.yaml` using `springdoc.*` and `management.*` properties:

```yaml
springdoc:
  api-docs:
    path: /v3/openapi
  packages-to-scan: com.nvidia
  info:
    title: "My API"
    description: "My API Description"
    version: "1.0.0"
```

## Auto Configured Beans

The starter autoconfigures and registers the following beans with the Spring application
context:

| Bean | Type | Description |
|------|------|-------------|
| HealthResponseCacheProperties | configuration | `java.time.Duration` TTL for caching `GET /health` responses; default **3s** if you do not register your own bean |
| CachedHealthResponseService | service | Caches minimal `/health` responses per TTL |
| HealthController | `@RestController` | `GET /health` endpoint with status, application, version |
| ApplicationHealthIndicator | `HealthIndicator` | Adds app name, profile, version to health response |
| OpenAPI | `OpenAPI` | Default API metadata (title, version, contact) when none exists; uses `springdoc.info.*` |
| OpenApiCustomizer | `OpenApiCustomizer` | Applies governance to component schemas |

## Auto Configuration

The library uses `CoreAutoConfiguration` as the single entry point, which imports:

| Class                            | Package | Description                                                                                                                  |
|----------------------------------|---------|------------------------------------------------------------------------------------------------------------------------------|
| ServletCoreCorsConfiguration     | cors    | Conditional on Servlet Application Type: registers FilterRegistrationBean for CORS                                           |
| ReactiveCoreCorsConfiguration    | cors    | Conditional on Reactive Application Type: registers CorsWebFilter for CORS                                                   |
| HealthConfiguration              | health  | Registers HealthResponseCacheProperties (default), CachedHealthResponseService, HealthController, ApplicationHealthIndicator |
| OpenApiConfiguration             | openapi | Registers default OpenAPI bean when none exists                                                                              |
| ServletOpenApiCorsConfiguration  | openapi | Conditional on Servlet Application Type: registers FilterRegistrationBean for api-docs path                                  |
| ReactiveOpenApiCorsConfiguration | openapi | Conditional on Reactive Application Type: registers WebFilter for api-docs path                                              |

## Bootstrap Configuration

The library uses following bootstrap configuration classes (in `bootstrap` package):

| Class                       | Description |
|-----------------------------|--------------|
| DnsCacheBootstrapConfiguration | Sets JVM DNS cache TTL to 60 seconds |
| MiscBootstrapConfiguration     | Validates active profiles at startup |

## EnvironmentPostProcessor

The library uses following environment post processors (in `env` package):

| Class                         | Description |
|-------------------------------|--------------|
| BootCoreEnvironmentPostProcessor | Loads default properties and version from git.properties |
| ValidateEnvironmentPostProcessor | Validates required properties (spring.application.name, spring.application.version, spring.profiles.active) at startup |

## Health response cache (`GET /health`)

`GET /health` responses are cached to reduce load from frequent probes. The default
time-to-live is **3 seconds**.

To use a custom TTL, register a `HealthResponseCacheProperties` bean (for example 5 seconds):

```java
@Configuration
class MyHealthCacheConfiguration {
    @Bean
    HealthResponseCacheProperties healthResponseCacheProperties() {
        return new HealthResponseCacheProperties(Duration.ofSeconds(5));
    }
}
```

If this bean is **absent**, the starter defaults TTL to **3 seconds**.

## Warmup

The `com.nvidia.boot.core.warmup` package provides `BootWarmupBase` for running cache or dependency
warmup after the application is ready.

- Extend `BootWarmupBase` and implement `createWarmupTasks()` to return a list of
  `BootWarmupBase.WarmupRunnable` tasks (each has a name, execution order, and `Runnable` body).
- Register the concrete class as a Spring bean (for example `@Component`).
- Pass `true` to the constructor to keep Actuator health **DOWN** until warmup completes, or
  pass a `Duration` to cap how long health stays down before timing out (default cap is 5 minutes).

Example:

```java
@Component
public class MyWarmup extends BootWarmupBase {

    public MyWarmup(MyService myService) {
        super(true); // block health until warmup completes
    }

    @Override
    public List<WarmupRunnable> createWarmupTasks() {
        return List.of(
                new WarmupRunnable("load-cache", 0, () -> { /* ... */ }));
    }
}
```
