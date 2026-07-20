# NV Boot Starter Exceptions

Exception classes with RFC 7807 Problem Details support. Extends Spring's 
`ErrorResponseException` for consistent error handling across NV applications.

## Adding as a Dependency

Add to your application's `pom.xml`:

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
        <artifactId>nv-boot-starter-exceptions</artifactId>
    </dependency>
</dependencies>
```

## Auto Configured Beans

Spring Boot loads auto-configuration from this starter via
`META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports`.

Depending on your web stack and classpath, one of these may be registered (each only
when no bean of the corresponding `ResponseEntityExceptionHandler` type is already defined):

| Configuration | When it applies | Bean |
|-----------------|-----------------|------|
| `ServletExceptionsAutoConfiguration` | Servlet web app (`WebApplicationType.SERVLET`), `spring-webmvc` present | `DefaultMvcExceptionHandler` |
| `ReactiveExceptionsAutoConfiguration` | Reactive web app (`WebApplicationType.REACTIVE`), `spring-webflux` present | `DefaultReactiveExceptionHandler` |

If both Spring MVC and WebFlux are on the classpath, Spring Boot still chooses a single
web application type (typically Servlet unless you set `spring.main.web-application-type=reactive`).
See the Spring Boot reference on [web applications](https://docs.spring.io/spring-boot/reference/web/index.html).
Provide your own `@ControllerAdvice` / `ResponseEntityExceptionHandler` bean to replace the defaults.

### Exception Classes (throw, not inject)

| Class | HTTP Status | Use Case |
|-------|-------------|----------|
| `BadRequestException` | 400 | Invalid request parameters |
| `UnauthorizedException` | 401 | Authentication required |
| `ForbiddenException` | 403 | Access denied |
| `NotFoundException` | 404 | Resource not found |
| `ConflictException` | 409 | Resource already exists |
| `TooManyRequestsException` | 429 | Rate limit exceeded |
| `UpstreamException` | 502 | Upstream service failure |
| `UnprocessableEntityException` | 422 | Validation failed |
| `PaymentRequiredException` | 402 | Payment required |

### Exception handlers (extend to customize)

For servlet apps, extend `BootMvcExceptionHandler`; for WebFlux, extend
`BootReactiveExceptionHandler`. The auto-configured defaults extend the same bases.

```java
@RestControllerAdvice
public class MyExceptionHandler extends BootMvcExceptionHandler {
    // Inherits handling for BootResponseException and security mappings
}
```
