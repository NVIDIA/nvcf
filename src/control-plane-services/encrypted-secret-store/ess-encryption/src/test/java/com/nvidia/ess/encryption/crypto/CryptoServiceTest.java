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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.when;

import tools.jackson.databind.ObjectMapper;
import com.google.gson.Gson;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import java.text.ParseException;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ThreadLocalRandom;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.RandomStringUtils;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Spy;
import org.mockito.junit.jupiter.MockitoExtension;
import reactor.core.publisher.Mono;

@Slf4j
@ExtendWith(MockitoExtension.class)
class CryptoServiceTest {

    private static final OctetSequenceKey MASTER_KEY = CryptoTestUtils.generateMasterEncryptionKey();
    public static final String MOCK_MASTER_KEY = CryptoTestUtils.encode(MASTER_KEY.toJSONObject());
    public static final String MOCK_ALL_MASTER_KEYS = CryptoTestUtils.encode(List.of(MASTER_KEY.toJSONObject()));
    private static final OctetSequenceKey NAMESPACE_KEY = CryptoTestUtils.generateEncryptionKey();
    public static final String MOCK_NAMESPACE = "dummy";

    @Spy
    private CryptoService cryptoService = new CryptoService();

    @Spy
    private CryptoProperties cryptoProperties = new CryptoProperties();

    @Mock
    private EncryptionKeyService encryptionKeyService;

    @Mock
    private ObjectMapper objectMapper;

    @Mock
    private EncryptionMetricsRegistry encryptionMetricsRegistry;


    @BeforeEach
    void setup() throws ParseException {
        cryptoService.setEncryptionKeyService(encryptionKeyService);
        cryptoService.setObjectMapper(objectMapper);
        setupKeys(MOCK_MASTER_KEY, MOCK_ALL_MASTER_KEYS);
    }

    public void setupKeys(String masterKey, String allMasterKeys) throws ParseException {
        cryptoProperties.setEncryptionMetricsRegistry(encryptionMetricsRegistry);
        cryptoProperties.setMasterKey(masterKey);
        cryptoProperties.setAllMasterKeys(allMasterKeys);

        cryptoProperties.init();
    }

    @Data
    @AllArgsConstructor
    static class ObjectToEncryptA {
        private String field1;
        private int field2;
        private boolean field3;

        static ObjectToEncryptA generate() {
            return new ObjectToEncryptA(RandomStringUtils.secure().nextAlphabetic(10), ThreadLocalRandom.current().nextInt(), ThreadLocalRandom.current().nextBoolean());
        }
    }

    @Data
    @AllArgsConstructor
    static class ObjectToEncryptB {
        private List<String> field4;
        private Map<String, String> field5;

        static ObjectToEncryptB generate() {
            return new ObjectToEncryptB(List.of(RandomStringUtils.secure().nextAlphabetic(10), RandomStringUtils.secure().nextAlphabetic(10)),
                    Map.of(RandomStringUtils.secure().nextAlphabetic(5), RandomStringUtils.secure().nextAlphabetic(15),
                            RandomStringUtils.secure().nextAlphabetic(7), RandomStringUtils.secure().nextAlphabetic(20)));
        }
    }

    private JsonObject buildListOfObjects() {

        List<Object> entities = new ArrayList<>();
        entities.add(ObjectToEncryptA.generate());
        entities.add(ObjectToEncryptB.generate());

        JsonObject jsonObject = new JsonObject();
        try {
            jsonObject.add("entities", new Gson().toJsonTree(entities));
        } catch (Exception e) {
            fail("Failed to build entities json object", e);
        }
        return jsonObject;
    }

    @Test
    void crypto_EncryptDecryptString_Success() {
        String secretString = "MySecret@12345$";
        String encryptedString = "";
        when(encryptionKeyService.getEncryptionKey(anyString()))
                .thenReturn(Mono.just(NAMESPACE_KEY));

        when(encryptionKeyService.getDecryptionKey(anyString(), anyString()))
                .thenReturn(Mono.just(NAMESPACE_KEY));
        try {
            encryptedString = cryptoService.encrypt(MOCK_NAMESPACE, secretString);
        } catch (Throwable e) {
            fail("Failed to encrypt secrets", e);
        }
        assertNotNull(encryptedString);
        try {
            assertEquals(secretString, cryptoService.decrypt(MOCK_NAMESPACE, encryptedString));
        } catch (Throwable e) {
            fail("Failed to decrypt the secrets", e);
        }
    }

    @Test
    void crypto_EncryptDecryptJSONObject_Success() {
        JsonObject jsonObject = buildListOfObjects();
        String encryptedString = "";
        when(encryptionKeyService.getEncryptionKey(anyString()))
                .thenReturn(Mono.just(NAMESPACE_KEY));

        when(encryptionKeyService.getDecryptionKey(anyString(), anyString()))
                .thenReturn(Mono.just(NAMESPACE_KEY));
        try {
            encryptedString = encrypt(MOCK_NAMESPACE, jsonObject);
        } catch (Exception e) {
            fail("Failed to encrypt plaintext", e);
        }
        assertNotNull(encryptedString);
        try {
            assertEquals(jsonObject.toString(), cryptoService.decrypt(MOCK_NAMESPACE, encryptedString));
        } catch (Throwable e) {
            fail("Failed to decrypt the ciphertext", e);
        }
    }

    @Test
    void crypto_EncryptWithInvalidNamespaceKey_ThrowsEncryptionException() {
        JsonObject jsonObject = buildListOfObjects();
        byte[] invalidKeyBytes = new byte[10]; // not correct length
        OctetSequenceKey invalidKey = new OctetSequenceKey.Builder(invalidKeyBytes).build();
        when(encryptionKeyService.getEncryptionKey(anyString()))
                .thenReturn(Mono.just(invalidKey));

        Assertions.assertThrows(EncryptionException.class, () -> encrypt(MOCK_NAMESPACE, jsonObject));
    }

    @Test
    void crypto_EncryptLargePayload_Succeeds() throws Throwable {
        when(encryptionKeyService.getEncryptionKey(anyString()))
                .thenReturn(Mono.just(NAMESPACE_KEY));

        String encryptedString = cryptoService.encrypt(MOCK_NAMESPACE, RandomStringUtils.secure().nextAlphabetic(1000));
        assertNotNull(encryptedString);
    }

    @Test
    void crypto_EncryptDecryptEncryptedStringCorrupted_ThrowsEncryptionException() {
        String secretString = "MySecret@12345$";
        String encryptedString = "";
        when(encryptionKeyService.getEncryptionKey(anyString()))
                .thenReturn(Mono.just(NAMESPACE_KEY));

        when(encryptionKeyService.getDecryptionKey(anyString(), anyString()))
                .thenReturn(Mono.just(NAMESPACE_KEY));
        try {
            encryptedString = cryptoService.encrypt(MOCK_NAMESPACE, secretString);
        } catch (Throwable e) {
            fail("Failed to encrypt secrets", e);
        }
        assertNotNull(encryptedString);

        // Simulate corrupted encrypted string.
        encryptedString += "ABCDEaaaabbbbbbWxypaqRRAbBjunkbadAsSassdadsdsdsadasdrrtrtrwetrtregtfsdfdhththtrerdfdffghghgf12345==";

        // Decrypt is expected to fail as key used to encrypt is not available now.
        // Immutable string required to pass it to lambda in assertThrows.
        final String immutableEncryptedString = encryptedString;
        Exception exception = assertThrows(EncryptionException.class, () -> cryptoService.decrypt(MOCK_NAMESPACE, immutableEncryptedString));
        assertTrue(exception.getMessage().contains("Failed to decrypt"));
    }

    @SneakyThrows
    private String encrypt(String namespace, JsonElement joData) {
        return cryptoService.encrypt(namespace, joData.toString());
    }
}
