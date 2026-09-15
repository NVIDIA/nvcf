/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.ess.encryption.crypto;

import tools.jackson.core.JacksonException;
import tools.jackson.core.type.TypeReference;
import tools.jackson.databind.ObjectMapper;
import com.nimbusds.jose.CompressionAlgorithm;
import com.nimbusds.jose.EncryptionMethod;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWEAlgorithm;
import com.nimbusds.jose.JWEHeader;
import com.nimbusds.jose.JWEObject;
import com.nimbusds.jose.Payload;
import com.nimbusds.jose.crypto.DirectDecrypter;
import com.nimbusds.jose.crypto.DirectEncrypter;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.exceptions.BadJWEException;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import java.text.ParseException;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import reactor.core.Exceptions;
import reactor.core.publisher.Mono;
import reactor.core.scheduler.Schedulers;
import reactor.util.function.Tuple2;

/*
 * This service manages encryption and decryption of secrets to store the
 * secrets encrypted in DB.
 *  - A kid (key id) is associated with each key to identify the versioning of the key.
 *  - per namespace keys are fetched from EncryptionKeyService backed by C*
 *  - This service uses JWE based encryption/decryption
 *     - JWEObject (https://www.javadoc.io/doc/com.nimbusds/nimbus-jose-jwt/2.21/com/nimbusds/jose/JWEObject.html)
 *       contains an unencrypted header and an encrypted body.
 *     - String representation of the JWEObject is stored in the DB.
 *     - Secret data is encrypted and stored in the JWEObject body.
 *     - KeyId corresponding to the key used for encryption is stored in the
 *       unencryped header to always know which key to use during decryption.
 *  - Same JWEObject is used to send the encrypted secrets on the wire
 *    (for e.g when JobService retrieves the secrets and send it to Compute).
 *  - When decrypt request is received, correspondent KeyId in the header of the
 *    JWEObject received on the wire can be used to decrypt the secret.
 *
 * TODO: Support forced key rotation and forced encryption of all the secrets in
 *       DB can be supported in the later releases using a REST API. With the
 *       forced rotation, all the secrets in the DB need to be fetched, decrypted
 *       with their old keys, encrypted with the new key and stored to DB.
 *       This can be an expensive process, but might be required if the old keys
 *       are compromised.
 */
@Slf4j
@Service
@RefreshScope
public class CryptoService {

    private static final Integer DATA_COMPRESSION_THRESHOLD = 200;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionKeyService encryptionKeyService;

    @Setter(onMethod_ = {@Autowired})
    private ObjectMapper objectMapper;

    private Mono<OctetSequenceKey> getNamespaceKey(String namespace) {
        return encryptionKeyService.getEncryptionKey(namespace);
    }

    private Mono<OctetSequenceKey> getNamespaceKeyForDecrypt(
            String secretNamespace,
            String keyID
    ) {
        return encryptionKeyService.getDecryptionKey(secretNamespace, keyID);
    }

    /*
     * ------- Encryption -------
     */

    /**
     * Encrypt a string data and return and stringified JWEObject.
     *
     * @deprecated use {@link #asyncEncrypt(String, String)} instead.
     */
    @Deprecated(since = "0.0.1", forRemoval = false)
    public String encrypt(String namespace, String plaintext) {
        try {

            return asyncEncrypt(namespace, plaintext)
                    // assuming this will be fast and non-blocking
                    .subscribeOn(Schedulers.boundedElastic())
                    .block();
        } catch (RuntimeException e) {
            throw unwrapBlockingCryptoException(e);
        }
    }



    /**
     * Encrypt a string plaintext. Return a tuple of ciphertext and kid (of encryption key)
     *
     * @param namespace namespace within which to encrypt
     * @param plaintext stringified plaintext
     * @return Tuple(ciphertext, kid)
     *
     * @throws MissingMasterKeyException if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException       generic encryption exception related to parsing and
     *                                   encryption
     */
    public Mono<Tuple2<String, String>> asyncEncryptAndGetKid(String namespace, String plaintext) {
        // Create the header with the KeyId corresponding to the masterKey used
        // for encryption.
        return getNamespaceKey(namespace)
                .flatMap(namespaceKey -> {
                    try {
                        return Mono.zip(Mono.just(encrypt(namespaceKey, plaintext)), Mono.just(namespaceKey.getKeyID()));
                    } catch (JOSEException e) {
                        log.error("Unexpected error: key found, but unable to encrypt", e);
                        return Mono.error(() -> new EncryptionException("Failed to encrypt", e));
                    }
                });
    }

    /**
     * Encrypt any plaintext. Return a tuple of ciphertext and kid (of encryption key)
     *
     * @param namespace namespace within which to encrypt
     * @param objectPlaintext any plaintext object
     * @return Tuple(ciphertext, kid)
     *
     * @throws MissingMasterKeyException if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException       generic encryption exception related to parsing, encryption
     *                                   and serialization
     */
    public Mono<Tuple2<String, String>> asyncEncryptAndGetKid(String namespace, Object objectPlaintext) {
        String plaintextString;
        try {
            plaintextString = objectMapper.writeValueAsString(objectPlaintext);
        } catch (JacksonException e) {
            log.error("Unexpected error: ObjectMapper unable to serialize for encryption", e);
            return Mono.error(() -> new EncryptionException("Unable to serialize for encryption", e));
        }

        return asyncEncryptAndGetKid(namespace, plaintextString);
    }

    /**
     * Encrypt string plaintext and return ciphertext
     *
     * @see CryptoService#asyncEncryptAndGetKid(String, String)
     */
    public Mono<String> asyncEncrypt(String namespace, String plaintext) {
        return asyncEncryptAndGetKid(namespace, plaintext).map(Tuple2::getT1);
    }

    /**
     * Encrypt any plaintext and return ciphertext
     *
     * @see CryptoService#asyncEncryptAndGetKid(String, Object)
     */
    public Mono<String> asyncEncrypt(String namespace, Object objectPlaintext) {
        return asyncEncryptAndGetKid(namespace, objectPlaintext).map(Tuple2::getT1);
    }


    /*
     * ------- Decryption -------
     */

    /**
     * Decrypt a JWE string into a string plaintext.
     *
     * @deprecated use {@link #asyncDecrypt(String, String)} instead.
     */
    @Deprecated(since = "0.0.1", forRemoval = false)
    public String decrypt(String secretNamespace, String jweString) {
        try {
            return asyncDecrypt(secretNamespace, jweString)
                    // assuming this will be fast and non-blocking
                    .subscribeOn(Schedulers.boundedElastic())
                    .block();
        } catch (RuntimeException e) {
            throw unwrapBlockingCryptoException(e);
        }

    }

    private RuntimeException unwrapBlockingCryptoException(RuntimeException e) {
        // Reactive operators wrap thrown exceptions; expose the original runtime exception to callers.
        Throwable t = Exceptions.unwrap(e);
        if (t instanceof RuntimeException runtimeException) {
            return runtimeException;
        }
        return new EncryptionException(t);
    }

    /**
     * Decrypt a JWE string into a string plaintext
     *
     * @param namespace namespace within which to decrypt
     * @param jweString ciphertext in JWE format
     * @return plaintext in a string format
     *
     * @throws BadJWEException       if {@code jweString} is not a JWE string
     * @throws MissingKeyException       if encryption key corresponding to {@code jweString} is
     *                                   missing amongst {@code namespace} encryption keys
     * @throws MissingMasterKeyException if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException       generic encryption exception related to parsing and
     *                                   decryption
     */
    public Mono<String> asyncDecrypt(String namespace, String jweString) {
        // Parse String to JWE object.
        // JWEHeader is unencrypted and Payload is encrypted.
        JWEObject jweObject;
        try {
            jweObject = JWEObject.parse(jweString);
        } catch (ParseException e) {
            log.error("Provided payload for decryption is not a JWE string", e);
            return Mono.error(() -> new BadJWEException("Provided payload for decryption is not a JWE", e));
        }

        // Get  master key for decryption
        return getNamespaceKeyForDecrypt(namespace, jweObject.getHeader().getKeyID())
                .flatMap(namespaceKey -> {
                    try {
                        return Mono.just(decrypt(namespaceKey, jweString));
                    } catch (JOSEException | ParseException e) {
                        log.error("Unexpected error: key found, but unable to decrypt", e);
                        return Mono.error(() -> new EncryptionException("Failed to decrypt", e));
                    }
                });
    }


    /**
     * Decrypt a JWE string and deserialize into specified Object type
     *
     * @param namespace namespace within which to decrypt
     * @param jweString ciphertext in JWE format
     * @param typeReference type reference to deserialize with
     * @param <T> type into which to deserialize the plaintext
     * @return plaintext deserialized into specified Object Type
     *
     * @throws BadJWEException           if {@code jweString} is not a JWE string
     * @throws MissingKeyException       if encryption key corresponding to {@code jweString} is
     *                                   missing amongst {@code namespace} encryption keys
     * @throws MissingMasterKeyException if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException       generic encryption exception related to parsing, decryption
     *                                   and deserialization
     */
    public <T> Mono<T> asyncDecrypt(String namespace, String jweString, TypeReference<T> typeReference) {
        return asyncDecrypt(namespace, jweString)
                .flatMap(plaintext -> {
                    try {
                        return Mono.just(objectMapper.readValue(plaintext, typeReference));
                    } catch (JacksonException e) {
                        log.error("Unexpected error: ObjectMapper unable to deserialize for decryption", e);
                        return Mono.error(() -> new EncryptionException("Unable to deserialize for decryption", e));
                    }
                });
    }

    /*
     * ------- Utils -------
     * Keeping in the same class (might split into a separate utils class later)
     */
    public static JWEHeader.Builder getJWEBuilder(OctetSequenceKey key, String data) {
        // Use compression only for larger payloads
        return data.length() >= DATA_COMPRESSION_THRESHOLD ?
                new JWEHeader.Builder(JWEAlgorithm.DIR, EncryptionMethod.A256GCM)
                        .keyID(key.getKeyID())
                        .compressionAlgorithm(CompressionAlgorithm.DEF)
                :
                new JWEHeader.Builder(JWEAlgorithm.DIR, EncryptionMethod.A256GCM)
                        .keyID(key.getKeyID());
    }

    // Encrypt a string data and return and stringified JWEObject.
    public static String encrypt(OctetSequenceKey key, String data) throws JOSEException {
        JWEHeader header = getJWEBuilder(key, data).build();

        // Set the payload.
        Payload payload = new Payload(data);

        // Create the JWE object and encrypt it
        JWEObject jweObject = new JWEObject(header, payload);
        jweObject.encrypt(new DirectEncrypter(key));

        // Serialise to compact JOSE form.
        return jweObject.serialize();
    }


    public static String decrypt(OctetSequenceKey key, String jweString)
            throws ParseException, JOSEException {
        // Parse String to JWE object.
        // JWEHeader is unencrypted and Payload is encrypted.
        JWEObject jweObject = JWEObject.parse(jweString);

        // Decrypt using the key corresponding to the KeyId in the JWEHeader.
        jweObject.decrypt(new DirectDecrypter(key));

        // Get the decrypted payload as string.
        return jweObject.getPayload().toString();
    }
}
