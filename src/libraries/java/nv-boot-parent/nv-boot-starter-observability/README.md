# NV Boot Starter Observability

Tracing, logging, and metrics configuration for NV Boot applications: OpenTelemetry semantic
conventions, management context tracing, actuator endpoint tracing, filtered log stack traces,
and common metrics tags.

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
        <artifactId>nv-boot-starter-observability</artifactId>
    </dependency>
</dependencies>
```

> **Note:** Replace `${nv-boot.version}` with the desired nv-boot version, or use a property from your parent/BOM.

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

## Features

- **Common metrics tags** – Adds env, host_id, host_dc to all metrics via `MeterRegistryCustomizer`
- **Filtered log stack traces** – Logback configuration that filters noisy stack trace elements
  (Spring, Reactor, Netty, reflection, etc.) via `StackFilteringThrowableConverter`. Loaded by
  `LogbackEnvironmentPostProcessor` at startup.
- **OTel semantic conventions** – HTTP spans use OpenTelemetry attribute 
    names (`http.request.method`, `url.path`, `http.route`, etc.) for consistency with the OTel Java agent
- **Resource attributes** – Adds host, process, and runtime attributes (PID, arch, OS, Java version) to spans
- **Attribute redaction** – Redacts sensitive column values (e.g. passwords, secrets) from Cassandra query spans
    before export when configured; supports YAML `sensitive-columns` / `scan-package` and
    `@DoNotTraceValue` annotation scanning (scanning requires Spring Data Cassandra on the classpath)
- **Exception shortening** – Shortens exception stack traces in span events to keep only tracked
    packages (e.g. `com.nvidia`) plus one line of context
- **Management context tracing** – When actuator runs on a separate 
    port (e.g. `management.server.port=8181`), HTTP requests to actuator endpoints are 
    traced via `ServerHttpObservationFilter` (MVC only) registered in the management context
- **Cassandra observability** – When Spring Data Cassandra’s observability types are on the
    classpath, registers a `CqlSessionBuilderCustomizer` named
    `cassandraObservationRequestTrackerCustomizer` that adds `ObservationRequestTracker` to the
    shared `CqlSessionBuilder`, so CQL queries produce spans with `db.*` attributes (e.g.
    `db.cassandra.consistency_level`, `db.cassandra.coordinator.dc`, `db.name`, `db.operation`,
    `db.statement`, `db.system`). This bean is **not** registered if a bean with the same name
    already exists (for example `nv-boot-starter-cassandra` is ordered earlier and provides it when
    `CassandraSslBundleConfiguration` is active).
  - When no bean named `cassandraSession` exists, installs a `@Primary` `observableCqlSession`
    that wraps Boot’s default `CqlSession` with `ObservableCqlSessionFactory`; apps can opt out by
    exposing a `CqlSessionObservabilityProperties` bean with `enabled=false`. See
    [Cassandra observation opt-out](#cassandra-observation-opt-out) below. If `cassandraSession`
    is present (including `nv-boot-starter-cassandra`’s `RefreshingCqlSession`), this wrap is
    skipped; wrapping for that path is owned by the Cassandra starter.

## Environment Post Processors

- `LogbackEnvironmentPostProcessor` – Loads logback configuration properties
  (`logging.config`, `logging.exception-conversion-word`) for filtered stack traces.

## Auto Configured Beans

This starter adds the following beans to the Spring context:

- `MeterRegistryCustomizer` – Adds common tags (env, host_id, host_dc) to all metrics
- `ServerRequestObservationConvention` – Custom convention that emits OTel semantic convention attributes for HTTP spans
- `SdkTracerProviderBuilderCustomizer` – Adds process/host/runtime resource attributes to the tracer
- `AttributeRedactingSpanExporter` (via BeanPostProcessor) – Wraps SpanExporter to redact sensitive attributes in Cassandra spans when `sensitive-columns` or `scan-package` discovery yields column names
- `ExceptionShorteningSpanExporter` (via BeanPostProcessor) – Wraps SpanExporter to shorten exception stack traces before export
- `ServerHttpObservationFilter` – (MVC only) registered in the management context so actuator endpoints on a separate port produce traces
- `CqlSessionBuilderCustomizer` (`cassandraObservationRequestTrackerCustomizer`) – Registers
  `ObservationRequestTracker` on the `CqlSessionBuilder` when Spring Data Cassandra observability
  is on the classpath, unless a bean with that name already exists (e.g. from
  `nv-boot-starter-cassandra`)

### SpanExporter wrapper order

When both redaction and exception shortening are enabled, the SpanExporter chain is (outermost to innermost):

1. **ExceptionShorteningSpanExporter** – Shortens exception stack traces
2. **AttributeRedactingSpanExporter** – Redacts sensitive Cassandra attributes (only when `sensitive-columns` is non-empty)
3. **OTLP exporter** – Sends spans to the collector

Attribute redaction is applied before exception shortening (closer to the OTLP exporter). If
`sensitive-columns` is empty or not configured, `AttributeRedactingSpanExporter` is not added
and no redaction occurs.

### Cassandra observation opt-out

Without a `cassandraSession` bean, `ObservableCqlSessionFactory.wrap(cqlSession)` is applied to generate observations (metrics & spans)

Apps that want to instrument manually (e.g. via `ObservableReactiveSessionFactoryBean`)
should opt out to avoid duplicate / orphan sync spans. Opt out by registering a
`com.nvidia.boot.observability.tracing.cassandra.CqlSessionObservabilityProperties` bean:

- **Bean absent** (default): treated as if `enabled=true`. `cqlSession` is wrapped with observability.
- **Bean present with `enabled=true`** (class default): same as bean absent. Library wraps.
- **Bean present with `enabled=false`**: library returns an unwrapped `CqlSession` so the app's
  own wrap is the only observation layer active.

```java
@Configuration
class CqlSessionObservabilityConfig {

    @Bean
    CqlSessionObservabilityProperties cqlSessionObservabilityProperties() {
        var properties = new CqlSessionObservabilityProperties();
        properties.setEnabled(false);
        return properties;
    }

    @Bean
    CqlSessionBuilderCustomizer observationRequestTrackerCustomizer() {
        return builder -> builder.addRequestTracker(ObservationRequestTracker.INSTANCE);
    }

    @Bean
    ObservableReactiveSessionFactoryBean observableReactiveSession(
            CqlSession cqlSession,
            ObservationRegistry observationRegistry) {
        return new ObservableReactiveSessionFactoryBean(cqlSession, observationRegistry);
    }

    // Recommended to suppress the metrics from the observation. These metrics are not useful 
    // as spring data + driver metrics are likely sufficient
    @Bean
    MeterFilter suppressCassandraObservationMetrics() {
        Set<String> suppressed = Set.of(
                "execute", "execute.active", "execute.cassandra.node.success",
                "prepare", "prepare.active");
        return MeterFilter.deny(id -> suppressed.contains(id.getName()));
    }
}
```

**IMPORTANT:** If a `cassandraSession` bean is present (from `nv-boot-starter-cassandra` or
elsewhere), the `observableCqlSession` wrap is not applied; `CqlSessionObservabilityProperties` only
gates that fallback. For the refreshing session path, use
`RefreshingCqlSessionObservabilityProperties` from `nv-boot-starter-cassandra` to opt out of sync
wrap. The `CqlSessionBuilderCustomizer` for `ObservationRequestTracker` is provided once: either
by the Cassandra starter (when its configuration is active) or by this starter, not both.

## Configuration

Configure OTLP export, tracing, and metrics in `application.yml`:

```yaml
management:
  metrics:
    tags:
      env: ${ENVIRONMENT:dev}
      host_id: ${HOSTNAME:unknown}
      host_dc: ${AWS_REGION:${CLOUD_REGION:unknown}}
  tracing:
    sampling:
      probability: 1.0
    redaction:
      enabled: true
      cassandra:
        sensitive-columns: []  # Add column names to redact, e.g. [password_hash, api_key]
    exceptions:
      shorten: true
      packages: ["com.nvidia"]
  otlp:
    tracing:
      endpoint: http://localhost:4317/v1/traces
```

### Attribute redaction (secrets, passwords)

Redacts sensitive column values from Cassandra spans (`db.query.text`, `db.query.parameter.*`)
before export.

```yaml
management:
  tracing:
    redaction:
      enabled: true
      cassandra:
        sensitive-columns:
          - password_hash
          - secret_data
          - api_key
          - container_environment  # May contain secrets
        # Optional: scan @Table classes for @DoNotTraceValue (merge with sensitive-columns)
        scan-package: com.nvidia.foo.bar.app_root     # Root package of the app.
```

- `management.tracing.redaction.enabled` – Enable redaction wiring (default: `true`). Actual 
   redaction runs only when `sensitive-columns` is non-empty and/or `scan-package` discovers 
   columns; set to `false` to disable the feature entirely.
- `management.tracing.redaction.cassandra.sensitive-columns` – Column names to redact in Cassandra query traces.
  Values matching `column = 'value'` are replaced with `column = ?`. Case-insensitive.
  **If empty or not specified, no redaction is applied** – the SpanExporter is not wrapped.
- `management.tracing.redaction.cassandra.scan-package` – Base package to scan for `@Table` entity classes
  with `@DoNotTraceValue` on fields. When set and Spring Data Cassandra is on the classpath, discovered
  column names are merged with `sensitive-columns`. Use for annotation-based discovery (e.g. `com.nvidia`).
  Use `@DoNotTraceValue` from `com.nvidia.boot.observability.tracing.redaction`
  on entity fields; add `scan-package: com.nvidia` (or your entity package) to auto-discover
  columns without listing them in YAML.

### Exception shortening

- `management.tracing.exceptions.shorten` – Enable shortening (default: `true`)
- `management.tracing.exceptions.packages` – Package prefixes to keep in stack traces 
   (default: `["com.nvidia"]`). Stack traces are truncated after the last line matching any
   of these packages, plus one line.

### Batch span export

If the collector rejects spans with a gRPC error such as:

```
grpc: received message after decompression larger than max
```

reduce the export batch size so each request sends fewer spans:

```yaml
management:
  tracing:
    opentelemetry:
      export:
        max-batch-size: 100
        max-queue-size: 1024
```

| Property | Default | Description |
|----------|---------|-------------|
| `management.tracing.opentelemetry.export.max-batch-size` | 512 | Maximum spans per export. Must be ≤ max-queue-size. |
| `management.tracing.opentelemetry.export.max-queue-size` | 2048 | Maximum spans in the queue before dropping. |
| `management.tracing.opentelemetry.export.schedule-delay` | 5s | Delay between exports. |
| `management.tracing.opentelemetry.export.timeout` | 30s | Maximum time an export may run before being cancelled. |

**Note:** Smaller batches mean more frequent, smaller requests.

## Dependencies

This starter brings in:

- `spring-boot-starter-actuator`
- `spring-boot-starter-web`
- `micrometer-tracing-bridge-otel`
- `opentelemetry-exporter-otlp`
