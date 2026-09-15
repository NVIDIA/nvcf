# NV Boot Starter Reloadable Properties

File-based property reloading with Spring Cloud context refresh. Uses an
`EnvironmentPostProcessor` to load the properties file before PropertySourceLocators run,
allowing other libraries' BootstrapConfigurations to reference properties from the reloadable
file. Polls the file and triggers a context refresh when it changes.

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
        <artifactId>nv-boot-starter-reloadable-properties</artifactId>
    </dependency>
</dependencies>
```

## Configuration

Configure the library in application's `bootstrap.yaml` as shown below:

```yaml
nv-boot:
  reloadable-properties:
    enabled: true   # Default true
    file: file:/vault/secrets.json
    poll-duration: 5s   # Duration: 5s, PT5S, 300s, 5m, etc. Default: 300s
```

| Property | Type | Required | Default | Description                                                  |
|----------|------|----------|---------|--------------------------------------------------------------|
| `enabled` | boolean | No | `true`  | Whether reloadable properties are enabled                    |
| `file` | string | Yes (when enabled) | -       | Path to the properties file (e.g. `file:/path`, `file:path`) |
| `poll-duration` | Duration | No | `300s`  | Poll duration between reload checks (e.g. `5s`, `PT5S`, `5m`) |

## Auto Configured Beans

When `nv-boot.reloadable-properties.enabled` is true, the library registers the following
beans with Spring application context:

| Bean | Type | Description |
|------|------|-------------|
| `reloadablePropertiesConfigurationProvider` | `ReloadablePropertiesConfigurationProvider` | Reads file path and poll duration from `nv-boot.reloadable-properties` properties |
| `fileBasedPropertiesRefresher` | `FileBasedPropertiesRefresher` | Polls the properties file and triggers `ContextRefresher.refresh()` when it changes |
