# NV Boot Starter Cassandra

Library for Cassandra with SSL bundle support, credential rotation, refreshable
`CqlSession`, and optional Apache Cassandra Java Driver metrics via Micrometer. Supports
certificate rotation via Spring Boot SSL bundles and credential updates via
`RefreshScopeRefreshedEvent`.

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
        <artifactId>nv-boot-starter-cassandra</artifactId>
    </dependency>
</dependencies>
```

## Configuration

Set `spring.cassandra.ssl.bundle` to the **same name** as the PEM bundle under
`spring.ssl.bundle.pem` (examples below use `cassandra-ssl`). 

For a **local plaintext** cluster, either do not set `spring.cassandra.ssl.enabled` or 
set it to `spring.cassandra.ssl.enabled=false`. In such a scenario, the starter still registers the
refreshable `cassandraSession` without requiring `spring.cassandra.ssl.bundle` to be configured.

### PEM files on the filesystem (`file:`)

Use `file:` URLs so Spring Boot can load certificate and key material from disk. With
`reload-on-update: true`, Spring Boot watches those files and reloads the SSL bundle when they
change (see [SSL](https://docs.spring.io/spring-boot/reference/features/ssl.html) in the Spring
Boot reference).

```yaml
spring:
  ssl:
    bundle:
      pem:
        cassandra-ssl:
          reload-on-update: true
          keystore:
            certificate: "file:/vault/secrets/cassandra-app-cert.pem"
            private-key: "file:/vault/secrets/cassandra-app-key.pem"
          truststore:
            certificate: "file:/vault/secrets/cassandra-tls-cert.pem"

  cassandra:
    ssl:
      bundle: cassandra-ssl
```

### Base64 material from properties (`base64:` + `kv.*`)

When secrets are base64-encoded and exposed as properties (for example from a rendered secrets
file resolved as `kv.cassandra-ssl.*`), prefix values with `base64:` so Spring Boot decodes them
into PEM (see [SSL](https://docs.spring.io/spring-boot/reference/features/ssl.html)). Do not set
`reload-on-update` for this style - rotation is driven by the property source (for example
`nv-boot-starter-reloadable-properties` updating the secrets file and refreshing the context).

```yaml
spring:
  ssl:
    bundle:
      pem:
        cassandra-ssl:
          keystore:
            certificate: "base64:${kv.cassandra-ssl.appCert}"
            private-key: "base64:${kv.cassandra-ssl.appKey}"
          truststore:
            certificate: "base64:${kv.cassandra-ssl.tlsCert}"

  cassandra:
    ssl:
      bundle: cassandra-ssl
```

After a refresh, the starter rebuilds the Cassandra session from the rebound `SslProperties`
bean so TLS matches the updated `base64:` values (the registered `SslBundles` bean alone may
still reflect startup material until the bundle is reloaded by other means).

## Driver metrics

The starter pulls in `java-driver-metrics-micrometer`. You can export Cassandra Java Driver
[session and node metrics](https://apache.github.io/cassandra-java-driver/latest/core/metrics/)
to Micrometer by listing the metric names you want under `spring.cassandra.session-metrics` and/or
`spring.cassandra.node-metrics`. Both properties are **optional**; if you omit them (or leave both
lists empty), no driver metrics customizer is registered and the driver uses its default metrics
behavior.

- **Opt-in:** At least one **non-empty** list is required. The configuration class is only applied
  when `spring.cassandra.session-metrics` or `spring.cassandra.node-metrics` is bound to a list
  with at least one entry. If a key is present with an empty list (for example
  `session-metrics: []`), the customizer is **not** registered.
- **Implementation:** When enabled, the starter configures the Micrometer metrics factory,
  `TaggingMetricIdGenerator` (so node address is carried in Micrometer tags rather than in metric
  names—important for OpenTelemetry instrument naming), and the enabled metric lists you specify.

Example:

```yaml
spring:
  cassandra:
    session-name: my-app-session
    session-metrics:
      - bytes-sent
      - connected-nodes
      - cql-requests
    node-metrics:
      - pool.open-connections
      - pool.in-flight
```

See the driver reference configuration for the full list of available metric names.

## Observability (CQL tracing spans)

This starter’s `CassandraSslBundleConfiguration` registers a `CqlSessionBuilderCustomizer` bean
named `cassandraObservationRequestTrackerCustomizer` that adds Spring Data Cassandra’s
`ObservationRequestTracker` to the shared `CqlSessionBuilder`, so observations started by
`ObservableCqlSessionFactory` can finish on each request.

When an `ObservationRegistry` is also available (typically from `nv-boot-starter-observability`),
the primary `cassandraSession` bean (`RefreshingCqlSession`) is wrapped with
`ObservableCqlSessionFactory.wrap(...)` by default so synchronous CQL produces tracing spans.

If you use **both** this starter and `nv-boot-starter-observability`, the observability starter
**does not** register a second `cassandraObservationRequestTrackerCustomizer`: it backs off when
that bean name is already present (this starter is ordered ahead and owns the customizer). If you
use **only** `nv-boot-starter-observability`, it registers that customizer for Boot’s default
`CqlSession` path.

### When to disable it

Reactive apps that already wrap at the reactive layer via
`ObservableReactiveSessionFactoryBean` (the Spring Data idiomatic reactive pattern) should opt out
of the library's sync wrap. Without that opt-out, reactive apps see duplicate + orphan sync CQL
spans:

1. Spring Data's `CqlSessionObservationInterceptor` starts a new observation on every `execute` / `prepareAsync`.
2. On the CQL statement, the execution thread has nothing to connect the observation with (`ThreadLocal` did not get anything from reactive operations).
3. Span has no parent → emitted as a standalone trace *in addition to* the correctly-parented reactive span.

### Opt-out: expose a `RefreshingCqlSessionObservabilityProperties` bean

The library follows the "app-owned properties bean" convention - 
`com.nvidia.boot.cassandra.configuration.RefreshingCqlSessionObservabilityProperties`:

- **Bean absent** (default): treated as if `enabled=true`. `cqlSession` is wrapped with observability.
- **Bean present with `enabled=true`** (class default): same as bean absent. Library wraps.
- **Bean present with `enabled=false`**: library returns an unwrapped `CqlSession` so the app's
  own wrap is the only observation layer active.

```java
@Configuration
class CqlSessionObservabilityConfig {

    @Bean
    RefreshingCqlSessionObservabilityProperties refreshingCqlSessionObservabilityProperties() {
        var properties = new RefreshingCqlSessionObservabilityProperties();
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

    // Recommended to suppress the metrics from the observation. These metrics are not useful as spring data + driver metrics are likely sufficient
    @Bean
    MeterFilter suppressCassandraObservationMetrics() {
        Set<String> suppressed = Set.of(
                "execute", "execute.active", "execute.cassandra.node.success",
                "prepare", "prepare.active");
        return MeterFilter.deny(id -> suppressed.contains(id.getName()));
    }
}
```

### Long-term

This opt-out is a **short-term** fix.

The longer-term goal is to make the reactive and sync observation layers coexist on the same `CqlSession` without producing duplicate observations.

This requires:
1. Remove `ObservationRegistry` from `RefreshingCqlSession` -> internal `delegate` is always the raw driver session
2. Make `RefreshingCqlSession` a non-primary bean under a qualified name (e.g. `refreshingCassandraSession`)
3. Keep `cassandraSession` as the `@Primary @Bean` name (preserving today's by-type and by-name injection behavior), but explicitly wrap `@Qualifier("refreshingCassandraSession")` `ObservableCqlSessionFactory.wrap(refreshingCassandraSession, registry)`
4. Add  `ObservableReactiveSessionFactoryBean` bean that explicitly takes `@Qualifier("refreshingCassandraSession")` (not the `@Primary` `cassandraSession`)
5. Manage every edge case creating still creating `CqlSession` bean on every condition combination for backwards compatibility:
   1. `@ConditionalOnBean(ObservationRegistry.class)` on the sync wrap and the reactive factory bean - still create `cassandraSession` somehow
   2. `@ConditionalOnClass(ReactiveSession.class)` on the reactive factory bean
   3. When `CassandraSslBundleConfiguration` does not load (no `spring.cassandra.ssl.bundle` and
      `spring.cassandra.ssl.enabled` not explicitly `false`): no `refreshingCassandraSession` -> need
      to still do the same with `ObservableReactiveSessionFactoryBean` and `ObservableCqlSessionFactory` somehow
6. Add `@Autowired(required = false) CassandraObservationConvention` to plug-in conventions for both `ObservableReactiveSessionFactoryBean` and `ObservableCqlSessionFactory`

## Auto configured beans

`CassandraAutoConfiguration` imports the following `@Configuration` classes (this order):

`CassandraConverterConfiguration` → `CassandraAdvancedConfiguration` →
`CassandraHealthConfiguration` → `CassandraMetricsConfiguration` →
`CassandraSslBundleConfiguration`.

### CassandraConverterConfiguration

| Bean | Type | Description |
|------|------|-------------|
| `cassandraCustomConversions` | `CassandraCustomConversions` | Registers converters for `CqlDuration` ↔ `java.time.Duration` for Spring Data Cassandra entity mapping. |

### CassandraAdvancedConfiguration

| Bean | Type | Description |
|------|------|-------------|
| `advancedProperties` | `DriverConfigLoaderBuilderCustomizer` (`@Primary`) | Binds `spring.cassandra.advanced.*` properties to the DataStax Java Driver. |

### CassandraHealthConfiguration

| Bean | Type | Description |
|------|------|-------------|
| `cassandraHealthContributor` | `HealthContributor` | Registers a Cassandra health indicator for each `CqlSession` bean. Exposed at `/actuator/health` when Spring Boot Actuator is on the classpath. Uses `NextHostRetryPolicy` for the health check query so failures fail over to the next host. |

### CassandraMetricsConfiguration

| Bean | Type | Description |
|------|------|-------------|
| `configLoaderBuilderCustomizer` | `DriverConfigLoaderBuilderCustomizer` | Registers Micrometer as the driver metrics factory, `TaggingMetricIdGenerator`, and the enabled session/node metric lists from `spring.cassandra.session-metrics` and `spring.cassandra.node-metrics`. **Only created when at least one of those lists is non-empty** (see [Driver metrics](#driver-metrics)). |

### CassandraSslBundleConfiguration

Loaded when **`spring.cassandra.ssl.bundle`** is set (any value, including empty for bundle name
only) **or** when **`spring.cassandra.ssl.enabled=false`** (plaintext / local Cassandra without
setting a dummy `spring.cassandra.ssl.bundle=` property).

| Bean | Type | Description |
|------|------|-------------|
| `cassandraSslCustomizer` | `CqlSessionBuilderCustomizer` | Configures SSL context from the named Spring Boot SSL bundle when the bundle name is non-empty; otherwise leaves the builder without client TLS (plaintext). |
| `cassandraObservationRequestTrackerCustomizer` | `CqlSessionBuilderCustomizer` | Registers `ObservationRequestTracker` on the `CqlSessionBuilder` (present when `ObservationRequestTracker` is on the classpath). |
| `cassandraSession` | `CqlSession` (`@Primary`) | `RefreshingCqlSession` that refreshes on SSL bundle updates and credential changes. Wrapped with `ObservableCqlSessionFactory` when an `ObservationRegistry` is present; apps can opt out by exposing a `RefreshingCqlSessionObservabilityProperties` bean with `enabled=false`. See [Observability](#observability-cql-tracing-spans). |

Apps can inject these beans and use them as shown below:

```java
@Service
@RequiredArgsConstructor
public class MyCassandraService {

    private final CqlSession cassandraSession;

    public void query() {
        var result = cassandraSession.execute("SELECT * FROM keyspace.table");
        // ...
    }
}
```
