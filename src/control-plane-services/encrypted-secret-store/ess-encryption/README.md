# ESS Encryption

Contains interface for encryption/decryption using per namespace keys and rotation configurations

## Prerequisites

* C*
* Spring Boot
* Master Encryption Key

## Master Encryption Key

Through properties `kv.crypto.masterKey` and `kv.crypto.allMasterKeys`, Master Encryption Key is loaded for encryption operations.

* `kv.crypto.masterKey` is a base64 encoded JWK `{"kty":"oct","kid":"uuid-v1","k":"keymaterial"}`
* `kv.crypto.allMasterKeys` is a base64 encoded json array of JWKs `[{"kty":"oct","kid":"uuid-v1","k":"keymaterial"}]`

JWK's `kid` must be a UUID v1 so its timestamp can be extracted.

### Grace period after MEK rotation

There is a grace period of `48 hours` (controlled by `encryption.mekRotationGracePeriod`) after MEK rotation during which the new MEK will be available for decryption, but not on encryption flows. This means that encryption flows will continue to use the MEK that was "current" before the rotation (exception is when first MEK is created without rotation).

Grace period is required to avoid MEK synchronization issues between multiple pods and applications due to Vault usage.

* Vault agent default lease duration is 24 hours
* Spring reloads properties every `nv-boot.reloadable-properties.poll-duration` (normally 15 minutes)
* Start alerting after 24 hours if not reloaded
* Give another 24 hours to address the issue

## Encryption operations

```java
import com.nvidia.ess.encryption.crypto.CryptoService;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Mono;

@Service
public class ServiceClass {

    @Autowired
    private CryptoService cryptoService;

    // Encrypt
    public String getEncryptedData(String namespace, Object plaintext) {
        return cryptoService.encrypt(namespace, plaintext);
    }

    public Mono<String> getAsyncEncryptedData(String namespace, Object plaintext) {
        return cryptoService.asyncEncrypt(namespace, plaintext);
    }

    // Decrypt
    public String getDecryptedData(String namespace, String ciphertext) {
        return cryptoService.decrypt(namespace, ciphertext);
    }

    public Mono<String> getAsyncDecryptedData(String namespace, String ciphertext) {
        return cryptoService.asyncDecrypt(namespace, ciphertext);
    }
}
```

## Encryption key rotation
Library exposes some interfaces and default behaviors for encryption key rotations. Rotating keys does not affect the ability to decrypt previously encrypted data.

Example:
1. Namespace `nvidia` has encryption key version 1 `nvidiaV1`
2. `Service A` encrypts plaintext `P` using `nvidiaV1` into ciphertext `C`
3. Encryption key for namespace `nvidia` is rotated into version 2 `nvidiaV2`
4. `Service A` wants to decrypt ciphertext `C`. Although the _current_ key is `nvidiaV2`, `nvidiaV1` can still be used for decryption

There are currently no hooks for re-encrypting data when rotating keys.

### Scheduled
A default key rotation service is exposed as `KeyRotatorScheduledService`. It is controlled by property `encryption.rotation.scheduled.enabled` defaulting to false if missing.

Its function is to scan all encryption keys and determine if each needs to be rotated.

### (Optional) On demand
If a key needs to be rotated manually / on demand, then `EncryptionKeyRotationService.rotateEncryptionKey()` can be used.

## Encryption key re-encryption
Library re-encrypts encryption key using loaded Master Encryption Key.

### Scheduled
A default key rotation service is exposed as `KeyReencryptionScheduledService`. It is controlled by property `encryption.reencryption.scheduled.enabled` defaulting to false if missing.

Additionally, the configured scheduled service will only work if Spring boot is running in reactive mode, i.e. `spring.main.web-application-type=reactive`

## Rollout
Since SMS uses a single key version of `CryptoService` for all namespaces, this library supports multiple layers of backwards compatibility. The behavior takes precedence in the order listed:
1. `encryption.rollout.enabled` - if `false`, then a single global encryption key will be used for all operations
2. `encryption.rollout.useAllowList` - if `true`
   * use per namespace encryption key
   * allow decryption of data that was encrypted using a global key
   * use encryption key only if namespace is in allow list
3. `encryption.rollout.useDefaultKey` - if `true`
    * use per namespace encryption key
    * allow decryption of data that was encrypted using a global key
4. If all above are not matched, then
    * use per namespace encryption key

## C* tables
By default, library expects Spring Data Cassandra connection. There are 2 required tables in the specified keyspace: `encryption_keys_by_kid` and `encryption_keys_by_timestamp`. The names are overridable with `encryption.tableNameByKid` and `encryption.tableNameByTimestamp`, but don't override them unless required

## Caching
Encryption and decryption keys will be cached using Caffeine directly without using Spring Cache because of issues with caching Mono/Flux https://www.baeldung.com/spring-webflux-cacheable

## Example
For full properties spec, look at `EncryptionProperties` class.

Example of full configuration needed by this library
```yaml
kv:
   defaultkey: # global key - used as part of `encryption.rollout.useDefaultKey`
      defaultKey: base64encodedJsonJWK1
      allDefaultKeys: base64encodedJsonArrayJWKs1
   crypto: # master key - used to encrypt encryption keys
      masterKey: base64encodedJsonJWK2
      allMasterKeys: base64encodedJsonArrayJWKs2

encryption:
  # Table names have defaults, specify only if table name is different
#  tableNameByKid: encryption_keys_by_kid
#  tableNameByTimestamp: encryption_keys_by_timestamp
  mekRotationGracePeriod: 48h
  rollout:
    enabled: true
    useDefaultKey: true
    useAllowList: true
    allowList: "org1,org2"
  rotation:
    scheduled:
      enabled: true
      period: 77d
      fetchSize: 100 # Don't set to > 200. Beyond that Node might not be able to handle the resultset and become unavailable due to the scatter-gather pagination query.
      backpressurePageCount: 4
      cron: "${random.int[0,60)} ${random.int[0,60)} ${random.int[0,24)} * * * *" # Every day at random hour, minute and second. To reduce likelihood of collision, randomize day as well
  reencryption:
     scheduled:
        enabled: true
        period: 77d
        fetchSize: 100 # Don't set to > 200. Beyond that Node might not be able to handle the resultset and become unavailable due to the scatter-gather pagination query.
        backpressurePageCount: 4
        cron: "${random.int[0,60)} ${random.int[0,60)} ${random.int[0,24)} * * * *" # Every day at random hour, minute and second. To reduce likelihood of collision, randomize day as well
  cache:
    encryption:
      ttl: 60m
      maxSize: 256
    decryption:
      ttl: 60m
      maxSize: 1024
```


## Observability
Look at [metrics](operations/custom_metrics.md) and [tracing](operations/tracing.md) for details.
