# NV Boot Starter Registries

Container, Helm, Model, and Resource registry clients for Docker, ECR, NGC, Harbor, ACR (Azure),
OCI, Volcengine, and Artifactory. Validates artifact presence, fetches sizes, and resolves
pre-signed URLs.

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
        <artifactId>nv-boot-starter-registries</artifactId>
    </dependency>
</dependencies>
```

## Configuration

Configure recognized registries under an app-specific path that is provided by 
`RegistryConfigPathProvider` (e.g. `nvcf.registries` or `nvct.registries`):

```yaml
nvcf:
  registries:
    recognized:
      container:
        ngc:
          name: NGC Private Registry
          hostname: nvcr.io
          call-timeout: 30s
          read-timeout: 30s
          write-timeout: 30s
          connection-timeout: 10s
          credential-validation:
            enabled: false    # true by default -- impacts all the accounts
          artifact-validation:
            enabled: false    # true by default -- impacts all the accounts
        custom:
          name: Custom Registry
          hostname: custom.io
      model:
        ngc:
          name: NGC Private Registry
          hostname: api.ngc.nvidia.com
          oauth2:
            base-url: https://authn.nvidia.com/token
            group-scope: org-id
      # Similar for helm, resource
```

By default artifacts are validated when a new function/task is created. However, it can be
turned off on per-registry basis as shown above. This impacts all the accounts/tenants
that are using  the registry.

By default credentials are validated when a new registry-credential is created or updated.
However, it can be turned off on per-registry basis as shown above. This impacts all the
accounts/tenants that are using the registry.

## Required App Provided Beans

The starter library relies on the app to register the following bean(s) with Spring application 
context to be able to use this library:

| Bean | Type | Description |
|------|------|-------------|
| `RegistryConfigPathProvider` | `RegistryConfigPathProvider` | **Required.** Returns the base config path for registry properties (e.g. `"nvcf.registries"`). |

Here is a snippet showing how an app can register `RegistryConfigPathProvider` with
Spring application context:

```java
@Configuration
public class RegistryConfig {

    @Bean
    public RegistryConfigPathProvider registryConfigPathProvider() {
        return () -> "nvcf.registries";
    }
}
```

## Auto Configured Beans - Library Provided

Using the app registered bean(s) shown above, the library autoconfigures and registers 
following beans with the Spring application context:

| Bean | Type | Description                                                                        |
|------|------|------------------------------------------------------------------------------------|
| `modelRegistryService` | `ModelRegistryService` | Validates model artifacts, fetches sizes, resolves pre-signed URLs for models.  |
| `resourceRegistryService` | `ResourceRegistryService` | Same for resources.                                                             |
| `helmRegistryService` | `HelmRegistryService` | Validates Helm charts (OCI, Docker, ECR, NGC, Harbor, ACR, Volcengine, Artifactory). |
| `containerRegistryService` | `ContainerRegistryService` | Validates container images across supported registries.                            |
| `registryLookupService` | `RegistryLookupService` | Maps registry hostnames to names and vice versa.                                   |
| `registryMapperService` | `RegistryMapperService` | Maps hostnames, encodes/decodes credentials, URL helpers.                          |
| `registryConfigurationProperties` | `RegistryConfigurationProperties` | Bound registry configuration.                                                      |

The app can then inject the library configured and registered beans as shown below:

```java
@Service
@RequiredArgsConstructor
public class MyArtifactService {

    private final ModelRegistryService modelRegistryService;
    private final ContainerRegistryService containerRegistryService;
    private final RegistryLookupService registryLookupService;

    public void validateModel(String artifactUrl, String apiKey) {
        modelRegistryService.validateArtifact(artifactUrl, apiKey);
    }

    public void validateContainer(String imageUrl, String base64Secret) {
        containerRegistryService.validateArtifact(imageUrl, base64Secret);
    }

    public String getRegistryName(String hostname) {
        return registryLookupService.getModelRegistryNameByHostname(hostname);
    }
}
```

All service beans are `@RefreshScope` and will refresh when configuration changes
(e.g. via Spring Cloud Config or reloadable properties).
