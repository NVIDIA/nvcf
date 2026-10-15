# NV Boot Mock Servers Test

WireMock-based mock servers for container registries such as Docker, ECR, NGC, Harbor, ACR, OCI,
Volcengine, Artifactory, etc. and OAuth2 Token Server. Used for integration testing.

## Adding as a Dependency

Add this library as a `test` scope dependency to your application's or test module's `pom.xml`:

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
        <artifactId>nv-boot-mock-servers-test</artifactId>
        <scope>test</scope>
    </dependency>
</dependencies>
```

## Auto Configured Beans

This library does not autoconfigure and register any beans in the Spring application
context.

This module provides **mock server utilities and test fixtures** for use in `@SpringBootTest` 
or JUnit tests. It does not auto-configure beans. Use the mock classes from
package `com.nvidia.boot.mock.*` (e.g. `com.nvidia.boot.mock.docker`, `com.nvidia.boot.mock.azure`,
`com.nvidia.boot.mock.ngc`) to set up WireMock stubs for registry endpoints.

### Test Usage

Reference `BootTestConstants` and the mock server helpers when writing tests that need to validate
registry client behavior against simulated registry responses.
