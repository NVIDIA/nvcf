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
package com.nvidia.ess.encryption.it;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.when;

import tools.jackson.core.exc.InputCoercionException;
import tools.jackson.core.type.TypeReference;
import tools.jackson.databind.ObjectMapper;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.crypto.CryptoService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.exceptions.BadJWEException;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import java.util.HashMap;
import java.util.Map;
import java.util.UUID;
import lombok.SneakyThrows;
import org.apache.commons.lang3.RandomStringUtils;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.test.context.web.WebAppConfiguration;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.function.Tuple3;

@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = "spring.profiles.active:it")
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class CryptoServiceIT {

    @Autowired
    private CryptoService cryptoService;

    @Autowired
    private EncryptionKeyService encryptionKeyService;

    @MockitoSpyBean
    private ObjectMapper objectMapper;


    @Test
    @Order(1)
    void syncEncryptDecrypt_succeeds() throws Exception {
        String namespace = UUID.randomUUID().toString();
        String plaintext = RandomStringUtils.secure().nextAlphabetic(30, 100);
        String ciphertext = cryptoService.encrypt(namespace, plaintext);

        assertEquals(plaintext, cryptoService.decrypt(namespace, ciphertext));
    }


    @Test
    @Order(2)
    void asyncEncryptDecrypt_succeeds() {
        String namespace = UUID.randomUUID().toString();
        String plaintext = RandomStringUtils.secure().nextAlphabetic(30, 100);

        Mono<String> decryptedMono = cryptoService.asyncEncrypt(namespace, plaintext)
                .flatMap(ciphertext -> cryptoService.asyncDecrypt(namespace, ciphertext));

        StepVerifier.create(decryptedMono)
                .expectNext(plaintext)
                .expectComplete()
                .verify();
    }

    @SneakyThrows
    @Test
    @Order(3)
    void asyncEncrypt_onObjectMapperFailure_throwsEncryptionException() {
        String namespace = UUID.randomUUID().toString();

        when(objectMapper.writeValueAsString(any())).thenThrow(new InputCoercionException(null, "mock exception", null, null));

        Mono<String> encryptMono = cryptoService.asyncEncrypt(namespace, new HashMap<>());

        StepVerifier.create(encryptMono)
                .expectError(EncryptionException.class)
                .verify();
    }

    @Test
    @Order(4)
    void asyncDecrypt_onInvalidJWEString_throwsBadJWEException() {
        String namespace = UUID.randomUUID().toString();

        Mono<String> decryptedMono = cryptoService.asyncDecrypt(namespace, "not-jwe-string");

        StepVerifier.create(decryptedMono)
                .expectError(BadJWEException.class)
                .verify();
    }

    @Test
    @Order(5)
    void asyncEncryptDecrypt_onMatchingTypeReference_succeeds() {
        String namespace = UUID.randomUUID().toString();
        Map<String, String> plaintextObject = Map.of(RandomStringUtils.secure().nextAlphabetic(30, 100),
                RandomStringUtils.secure().nextAlphabetic(30, 100));

        Mono<Map<String, String>> decryptedMono =
                cryptoService.asyncEncrypt(namespace, plaintextObject)
                        .flatMap(ciphertext -> cryptoService.asyncDecrypt(namespace, ciphertext,
                                new TypeReference<>() {
                                }));

        StepVerifier.create(decryptedMono)
                .expectNext(plaintextObject)
                .expectComplete()
                .verify();
    }

    @Test
    @Order(6)
    void asyncEncryptDecrypt_onMismatchingTypeReference_throwsEncryptionException() {
        String namespace = UUID.randomUUID().toString();
        Map<String, String> plaintextObject = Map.of(RandomStringUtils.secure().nextAlphabetic(30, 100),
                RandomStringUtils.secure().nextAlphabetic(30, 100));

        Mono<Map<String, Long>> decryptedMono =
                cryptoService.asyncEncrypt(namespace, plaintextObject)
                        .flatMap(ciphertext -> cryptoService.asyncDecrypt(namespace, ciphertext,
                                new TypeReference<>() {
                                }));

        StepVerifier.create(decryptedMono)
                .expectError(EncryptionException.class)
                .verify();
    }

    @Test
    @Order(7)
    void syncEncryptDecrypt_payloadWithWrongKid_throwsMissingKeyException() throws Exception {
        String namespace1 = UUID.randomUUID().toString();
        String namespace2 = UUID.randomUUID().toString();
        String plaintext = RandomStringUtils.secure().nextAlphabetic(30, 100);
        String namespace1Ciphertext = cryptoService.encrypt(namespace1, plaintext);

        Assertions.assertThrows(MissingKeyException.class,
                () -> cryptoService.decrypt(namespace2, namespace1Ciphertext));
    }


    @Test
    @Order(8)
    void asyncEncryptAndGetKidAndDecrypt_onString_succeeds() {
        String namespace = UUID.randomUUID().toString();
        String plaintext = RandomStringUtils.secure().nextAlphabetic(30, 100);

        Mono<Tuple3<String, String, OctetSequenceKey>> decryptedMono = cryptoService.asyncEncryptAndGetKid(namespace, plaintext)
                .flatMap(tuple -> Mono.zip(
                        // decrypt to compare
                        cryptoService.asyncDecrypt(namespace, tuple.getT1()),
                        // pass forward the kid
                        Mono.just(tuple.getT2()),
                        // get the encryption key to compare
                        encryptionKeyService.getEncryptionKey(namespace)
                ));

        StepVerifier.create(decryptedMono)
                .assertNext(tuple -> {
                    assertEquals(plaintext, tuple.getT1());
                    assertEquals(tuple.getT3().getKeyID(), tuple.getT2());
                })
                .expectComplete()
                .verify();
    }

    @Test
    @Order(9)
    void asyncEncryptAndGetKid_onMapType_succeeds() {
        String namespace = UUID.randomUUID().toString();
        Map<String, String> plaintextObject = Map.of(RandomStringUtils.secure().nextAlphabetic(30, 100),
                RandomStringUtils.secure().nextAlphabetic(30, 100));

        Mono<Tuple3<Map<String, String>, String, OctetSequenceKey>> decryptedMono = cryptoService.asyncEncryptAndGetKid(namespace, plaintextObject)
                .flatMap(tuple -> Mono.zip(
                        // decrypt to compare
                        cryptoService.asyncDecrypt(namespace, tuple.getT1(), new TypeReference<>() {}),
                        // pass forward the kid
                        Mono.just(tuple.getT2()),
                        // get the encryption key to compare
                        encryptionKeyService.getEncryptionKey(namespace)
                ));

        StepVerifier.create(decryptedMono)
                .assertNext(tuple -> {
                    assertEquals(plaintextObject, tuple.getT1());
                    assertEquals(tuple.getT3().getKeyID(), tuple.getT2());
                })
                .expectComplete()
                .verify();
    }
}
