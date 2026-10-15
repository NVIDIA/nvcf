# NV Boot Starter Telemetry

Telemetry client for sending [CloudEvents](https://cloudevents.io/) to a Telemetry server. Uses WebClient with 
OAuth2 bearer token via ExchangeFilterFunction. 

**Apps must register a `TelemetryProperties` bean in the Spring context.**

## Adding as a Dependency

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
        <artifactId>nv-boot-starter-telemetry</artifactId>
    </dependency>
</dependencies>
```

## Registering TelemetryProperties

Apps must provide a `TelemetryProperties` bean. All of `url`, `pathPrefix`, `source`, 
and OAuth2 credentials are required; validation fails fast at startup if any are missing.

### ConfigurationProperties binding

Bind from `application.yml` using a custom prefix:

```java
@Configuration
public class TelemetryConfig {

    @Bean
    @RefreshScope
    @ConfigurationProperties(prefix = "app.telemetry")
    public TelemetryProperties telemetryProperties() {
        return new TelemetryProperties();
    }
}
```

```yaml
app:
  telemetry:
    url: https://prod.analytics.nvidiagrid.net
    path-prefix: /api/v2/topic
    source: my-application
    oauth2:
      token-uri: ${spring.security.oauth2.client.provider.telemetry.token-uri}
      client-id: ${spring.security.oauth2.client.registration.telemetry.client-id}
      client-secret: ${spring.security.oauth2.client.registration.telemetry.client-secret}
      scope: ${spring.security.oauth2.client.registration.telemetry.scope:}
```

## Usage

```java
@Service
@RequiredArgsConstructor
public class MyService {

    private final TelemetryClient telemetryClient;
    private final CloudEventBuilderProvider cloudEventBuilderProvider;
    private final JsonMapper jsonMapper;

    public void sendTelemetry(String resourceName, MyEvent event) throws IOException {
        var cloudEvent = cloudEventBuilderProvider.getCloudEventBuilder()
                .withType("com.example.MyEvent")
                .withSource(URI.create("urn:my-app"))
                .withData(PojoCloudEventData.wrap(event, jsonMapper::writeValueAsBytes))
                .build();

        var response = telemetryClient.send(resourceName, List.of(cloudEvent));
        // response.getBodyOptional() for JSON body; response.getStatusCode() for HTTP status
    }
}
```

`TelemetryClient.send()` returns `TelemetryResponse<Map<String, Object>>` with `statusCode` 
and optional `body` (Map for JSON responses, null for 204 No Content).

## Testing

Run module tests:

```bash
mvn -pl nv-boot-starter-telemetry test
```

- **Unit tests** cover validation (`TelemetryClient`, `TelemetryProperties`), `TelemetryResponse`,
  and `CloudEventBuilderProvider` (with `SecurityContextHolder`).
- **Integration tests** use **WireMock** (`wiremock-standalone`, version from `nv-boot-bom`)
  for OAuth2
  token exchange, telemetry POST, and `TelemetryWebClientFactory`;
  `TelemetryAutoConfigurationIntegrationTest` loads a minimal Spring context with
  `WebEnvironment.NONE`.

## Auto-configured beans

| Bean | Type | Condition |
|------|------|-----------|
| `TelemetryClient` | `TelemetryClient` | When app registers `TelemetryProperties` |
| `CloudEventBuilderProvider` | `CloudEventBuilderProvider` | When `TelemetryClient` is configured |
