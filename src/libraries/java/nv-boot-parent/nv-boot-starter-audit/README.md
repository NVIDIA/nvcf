# NV Boot Starter Audit

Audit logging for applications as per NVIDIA Audit Specification. This starter ships a single 
concrete JSON-based audit path(`AuditService`, `AuditEventPayload`, `BootAuditEvent`) that works
the same in open-source and managed deployments. Optional HMAC signing is enabled when the 
application registers an `AuditProperties` bean (see below).

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
        <artifactId>nv-boot-starter-audit</artifactId>
    </dependency>
</dependencies>
```

> **Note:** Replace `${nv-boot.version}` with the desired nv-boot version, or use a property from your parent/BOM.

## Auto Configured Beans - Library Provided

This starter module autoconfigures an `AuditService` bean using `AuditAutoConfiguration`. Apps 
can inject `AuditService` and use the builder to dispatch audit events as shown below:

```java
@Service
@RequiredArgsConstructor
public class MyService {

    private final AuditService auditService;

    public void createResource(Resource resource) {
        auditService.audit(
            auditService.auditEventPayloadBuilder()
                .operation("CREATE")
                .type("RESOURCE")
                .objectId(resource.getId())
                .state("CREATED")
                .summary("Created resource " + resource.getName())
        );
    }
}
```

## Optional: Register `AuditProperties` for HMAC signing

`AuditService` is always registered by auto-configuration. By default it produces **unsigned**
payloads and publishes `BootAuditEvent` with a **null** top-level `hmac`.

You can optionally add a Spring bean of type `com.nvidia.boot.audit.AuditProperties` (this type
is **not** `@ConfigurationProperties`; the application owns binding from YAML, Vault, or other
sources). When the bean is present and both `hmacKeys` and `hmacKid` are non-blank, `AuditService`
switches to **signed** mode: `auditEventPayloadBuilder()` uses `AuditEventPayload.signedBuilder`,
and each published event includes a top-level `hmac` over the serialized payload.

### HMAC format and computation

Signing uses **HMac-SHA3-512** via the **BouncyCastle** (`BC`) JCE provider. Every formatted
value (top-level `hmac`, payload `hmacBefore`, payload `hmacAfter`) uses the same string shape:

`{algorithm}:{kid}:{base64(hmac_bytes)}`

The MAC input is **not** the raw JSON text. It is the **four big-endian bytes** of
`JsonNode.hashCode()` for the relevant Jackson `JsonNode` (the serialized payload tree for the
event-level `hmac`, or the `jsonBefore` / `jsonAfter` node for the payload fields). Verifiers must
use the same tree (parse JSON with a compatible `JsonMapper` / node equality) so `hashCode()`
matches. Choose HMAC key material with adequate length for SHA3-512 (for example 32+ random bytes).

Example (illustrative base64 suffix):

```json
"hmac": "HMac-SHA3-512:audit-key-v1:dGVzdCBobWFjIHZhbHVl..."
```

When the audit builder includes **`jsonBefore`** and/or **`jsonAfter`** (`JsonNode` state
snapshots), the serialized payload may also include **`hmacBefore`** and **`hmacAfter`**, using
the same **HMac-SHA3-512** `{algorithm}:{kid}:base64(...)` format over those nodes’
`hashCode()`-derived inputs, so consumers can verify state transitions. If only
`jsonBefore` is set, only `hmacBefore` is populated; if only `jsonAfter` is set, only `hmacAfter`
is populated. When both are set, the payload also carries **`stateSummary`** and
**`historySummary`** (RFC 6902 JSON Patch strings) derived from those nodes.

The following snippet shows how apps can bind secrets from Vault to app-specific configuration
properties:

```yaml
myapp:
  audit:
    hmac:
      kid: ${kv.audit-hmac-kid}
      keys: ${kv.audit-hmac-keys}
```

Here is a sample snippet showing how apps can add `AuditProperties` to the Spring application
context:

```java
@Configuration
public class AuditConfiguration {

    @Bean
    public AuditProperties auditProperties(
            @Value("${myapp.audit.hmac.keys}") String hmacKeys,
            @Value("${myapp.audit.hmac.kid}") String hmacKid) {
        var props = new AuditProperties();
        props.setHmacKeys(hmacKeys);
        props.setHmacKid(hmacKid);
        return props;
    }
}
```

### HMAC key store format

The `hmacKeys` value must be **Base64-encoded** JSON:

```json
{
  "keys": [
    { "kid": "audit-key-v1", "key": "base64-encoded-hmac-key" },
    { "kid": "audit-key-v2", "key": "base64-encoded-hmac-key-2" }
  ]
}
```

The `hmacKid` field selects which key ID from that store is used for signing.

## Security considerations

HMAC values in this starter are computed for **NVIDIA compatibility** and **wire-level
verification** (same algorithm, string format, and `JsonNode.hashCode()` canonicalization as
internal NVIDIA audit utilities). They help consumers confirm that an event was produced with the
expected signing key and that the JSON tree they parsed matches what the signer hashed.

They do **not** provide the same guarantees as an HMAC over the full serialized JSON bytes:

- **32-bit digest space:** The MAC input is only four big-endian bytes derived from
  `JsonNode.hashCode()`, not the entire document. Distinct JSON payloads can, in principle,
  collide on `hashCode()` and therefore produce identical HMACs (collision resistance is far
  weaker than SHA3-512 over the full payload).
- **Jackson / JVM coupling:** Verifiers must parse JSON into a `JsonNode` compatible with the
  signer’s expectations. While Jackson’s `JsonNode` contract is stable for a given library
  version, treating `hashCode()` as a long-lived cross-version fingerprint without validation
  risks mismatch if serialization or node semantics ever diverge.
- **Threat model:** Treat these HMACs as a **tamper-evident checksum on the agreed
  canonicalization**, not as a standalone proof of unique payload identity for high-assurance
  compliance. Where you need strong integrity over raw bytes, add an additional layer (for
  example a conventional HMAC or signature over the exact octets you store or transmit).

## Behavior

- **AuditService** publishes `BootAuditEvent` for custom listeners; with `AuditProperties` as above, events carry a top-level `hmac` when signing is active, and the payload may include `hmacBefore` / `hmacAfter` (plus optional `stateSummary` / `historySummary`) when `jsonBefore` / `jsonAfter` are supplied on the builder
- **BootAuditEventListener** asynchronously logs `BootAuditEvent` payloads to stdout with an `[AUDIT]` prefix (registered by auto-configuration)
- Extracts actor/subject from JWT when `spring-boot-starter-oauth2-resource-server` is on the classpath
