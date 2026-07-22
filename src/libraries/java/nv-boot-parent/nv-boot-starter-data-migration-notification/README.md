# NV Boot Starter Data Migration Notification

CloudEvents-based data migration notifications for NV applications. The starter builds
data migration payloads, serializes them as CloudEvents, and publishes them to NATS using
a caller-provided `Connection`.

**Apps must register a `DataMigrationNotificationProperties` bean in the Spring context.**

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
        <artifactId>nv-boot-starter-data-migration-notification</artifactId>
    </dependency>
</dependencies>
```

## Registering DataMigrationNotificationProperties

Apps must provide a `DataMigrationNotificationProperties` bean from
`com.nvidia.boot.migration.notification.service`. `DataMigrationNotificationAutoConfiguration`
is conditional on this bean and backs off when it is missing.

### ConfigurationProperties binding

Bind from `application.yml` using an app-specific custom prefix:

```java
@Configuration
public class DataMigrationNotificationConfig {

    @Bean
    @ConfigurationProperties(prefix = "app.data-migration-notification")
    public DataMigrationNotificationProperties dataMigrationNotificationProperties() {
        return new DataMigrationNotificationProperties();
    }
}
```

```yaml
app:
  data-migration-notification:
    name: ${spring.application.name}
    version: ${spring.application.version}
    hostname: ${HOSTNAME:localhost}
```

## Required App Provided Beans

The starter library relies on the app to register the following bean(s) with Spring application
context to be able to use this library:

| Bean                                  | Type                                  | Description |
|---------------------------------------|---------------------------------------|-------------|
| `dataMigrationNotificationProperties` | `DataMigrationNotificationProperties` | **Required.** Application identity used in CloudEvent source, payloads, and NATS subjects. |
| `jsonMapper`                          | `JsonMapper`                          | Required by `DataMigrationNotificationService`; Spring Boot usually provides this when Jackson is on the classpath. |

The notification module does not own the NATS connection lifecycle. Callers pass a
`Connection` to the `DataMigrationNotificationService` notification method for each lifecycle
event.

## Usage

Create or inject a NATS `Connection` in your application:

```java
@Configuration
public class NatsConfiguration {

    @Bean(destroyMethod = "close")
    public Connection natsConnection(@Value("${nats.url}") String natsUrl)
            throws IOException, InterruptedException {
        return Nats.connect(natsUrl);
    }
}
```

Annotate migration tasks with `@DataMigration` to document schema changes and cleanup
expectations. The annotation is retained at runtime and can be discovered by migration
tooling or application code.

```java
@DataMigration(
        keyspace = "nvcf_api",
        newTables = {"table1", "table2"},
        newColumns = {"table1.field1"},
        requiresTables = {"gpu_specifications"},
        description = "Migrate GPU specs from functions_deployment_v2 to gpu_specifications")
public class AsyncFooMigration {
}
```

`@DataMigration` attributes:

| Attribute | Required | Description |
|-----------|----------|-------------|
| `keyspace` | Yes | Cassandra keyspace affected by the migration. |
| `newTables` | No | Tables created or introduced by the migration. |
| `newColumns` | No | Columns created or introduced by the migration, usually in `table.column` format. |
| `requiresTables` | No | Existing tables that must be present before the migration can run. |
| `description` | Yes | Human-readable summary of the migration's purpose. |

Dispatch a data migration notification:

```java
import com.nvidia.boot.migration.notification.service.DataMigrationNotificationService;
import io.nats.client.Connection;

@Service
@RequiredArgsConstructor
public class MyMigrationTask {

  private final DataMigrationNotificationService notificationService;
  private final Connection natsConnection;

  public void run() {
    notificationService.notifyOnStart(natsConnection, taskName, Optional.of("Starting migration task"));
    try {
      // do migration stuff
      notificationService.notifyOnEnd(natsConnection, taskName, Optional.of("Migration task completed"));
    } catch (Exception e) {
      notificationService.notifyOnError(natsConnection, taskName, Optional.ofNullable(e.getMessage()));
      // handle error
    }
  }
}
```

Available notification methods:

| Method | Type |
|--------|------|
| `notifyOnStart(Connection, String, Optional<String>)` | `START` |
| `notifyOnEnd(Connection, String, Optional<String>)` | `END` |
| `notifyOnError(Connection, String, Optional<String>)` | `ERROR` |

Use `Optional.empty()` when there is no message. `DataMigrationType` values are `START`,
`END`, and `ERROR`.

## Behavior

- `DataMigrationNotificationService` creates CloudEvents with:
  - random UUID event id
  - source format: `{name}:{version}@{hostname}` from `DataMigrationNotificationProperties`
  - event type `data-migration`
  - content type `application/json`
  - UTC timestamp
- `DataMigrationNotificationService` publishes the serialized CloudEvent to NATS and flushes
  the connection with a 5 second timeout.
- Send failures are logged and not rethrown.
- `DataMigrationNotificationService` builds NATS subjects as:

```text
{applicationName}.{taskName}.{type}
```

For example:

```text
my-service.backfill.END
```

`applicationName`, `taskName`, and `type` are used as NATS subject tokens. These values must
not contain `.`, `*`, or `>`; invalid values fail before CloudEvent serialization or NATS
publishing.

## Testing

Run module tests:

```bash
mvn -pl nv-boot-starter-data-migration-notification test
```

- **Unit tests** cover `DataMigrationNotificationAutoConfiguration` conditions and backoff.
- **Unit tests** cover `DataMigrationNotificationService` CloudEvent creation, payload
  generation, NATS subject generation and validation, publish, and flush behavior.
