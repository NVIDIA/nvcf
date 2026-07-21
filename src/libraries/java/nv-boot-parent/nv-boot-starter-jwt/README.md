# NV Boot Starter JWT

This is a library that allows operations on JWT:
* Signing
* Verifying signature
* Encrypt JWE
* Decrypt JWE

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
        <artifactId>nv-boot-starter-jwt</artifactId>
    </dependency>
</dependencies>
```

## Spring Context Configuration

### Beans the Application Must Provide

The library requires the following beans to be present in the Spring context before it can activate:

| Bean | Type | Description |
|------|------|-------------|
| `privateJwks` | `PrivateJwksString` | Holds the JSON string content of the private JWK Set. Used for signing JWTs and encrypting/decrypting payloads. |
| `jweKeysMapping` | `JweKeysMapping` | Maps keyset names (used in application code) to JWE key IDs (`kid`) in the JWK Set. |
| `encryptedModelConverterProperties` | `EncryptedModelConverterProperties` | *(Optional)* Provide this bean to activate the `EncryptedModelConverter`. Sets the base package to scan for `@ValueObject`-annotated classes. |

**Example configuration:**

```java
@Configuration
public class JwtConfiguration {

    @Bean
    public PrivateJwksString privateJwks() throws IOException {
        // Load from file, Vault, or other secure source
        ClassPathResource keyResource = new ClassPathResource("keys/jwks_private.json");
        return new PrivateJwksString(
                new String(keyResource.getInputStream().readAllBytes()));
    }

    @Bean
    public JweKeysMapping jweKeysMapping() {
        return JweKeysMapping.builder()
                .keysMapping(Map.of("my-keyset-name", "kid-from-jwks"))
                .build();
    }

    // Optional: provide to enable EncryptedModelConverter
    @Bean
    public EncryptedModelConverterProperties encryptedModelConverterProperties(
            @Value("${my-app.jwt.base-package}") String basePackage) {
        var props = new EncryptedModelConverterProperties();
        props.setBasePackage(basePackage);
        return props;
    }
}
```

### Auto Configuration

The library uses `JwtAutoConfiguration` as the single entry point, which imports `JwksConfiguration`
(in the `configuration` package).

### Beans the Library Auto-Configures

When `PrivateJwksString` and `JweKeysMapping` are present, the library registers these beans:

| Bean | Type                      | Description                                                                                                                                               |
|------|---------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------|
| `jwkSet` | `JWKSet`                  | Parsed JWK Set from `PrivateJwksString`.                                                                                                                  |
| `jwtService` | `JwtService`              | JWT signing, verification, encryption, and decryption.                                                                                                    |
| `genericEncryptedJsonMapper` | `JsonMapper`              | JsonMapper configured for encrypted model conversion. Only created if no bean with this name exists.                           |
| `encryptedModelConverter` | `EncryptedModelConverter` | Converts between model and value object types with support for encrypted fields. Only created when an `EncryptedModelConverterProperties` bean is present. |


## Usage
### Generating keys
* [generate EC (private key)](./src/test/java/com/nvidia/boot/jwt/services/JwtServiceTest.java#L201)
* [generate AES (symmetric key)](../src/test/java/com/nvidia/boot/jwt/services/JwtServiceTest.java#L220)

### Getting JwtService
`JwtService` has two dependencies:
* key-set of private keys
* key type to id mapping

The key mapping is needed for your services to be able to rotate encryption keys. Consumer service
will use key type and JwtService will resolve kid using the mapping. This way your app services
don't deal with key ids directly. If you do need to use particular key, you still can.

See [JwtServiceTest](../main/src/test/java/com/nvidia/boot/jwt/services/JwtServiceTest.java)
for more usage examples.
