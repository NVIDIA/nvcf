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
package com.nvidia.ess.encryption.config.properties;

import static com.nvidia.ess.encryption.crypto.CryptoTestUtils.encode;
import static com.nvidia.ess.encryption.crypto.CryptoTestUtils.generateMasterEncryptionKey;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.Mockito.spy;
import static org.mockito.Mockito.timeout;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.jwk.KeyType;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.text.ParseException;
import java.time.Duration;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.boot.context.properties.source.InvalidConfigurationPropertyValueException;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Slf4j
@ExtendWith(MockitoExtension.class)
class CryptoPropertiesTest {

    private final CryptoProperties cryptoProperties = new CryptoProperties();

    @Mock
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @Mock
    private EncryptionProperties encryptionProperties;

    private static final OctetSequenceKey mockMasterKey = generateMasterEncryptionKey();


    @BeforeEach
    void init() {
        cryptoProperties.setEncryptionMetricsRegistry(encryptionMetricsRegistry);
        cryptoProperties.setEncryptionProperties(encryptionProperties);
    }

    public static Stream<Arguments> failedMasterKeySetupParams() {
        return Stream.of(
                Arguments.of("", IllegalArgumentException.class),
                Arguments.of("notebase64encoded", ParseException.class),
                Arguments.of(encode("base64encoded"), ParseException.class),
                Arguments.of(encode(Collections.emptyList()), ParseException.class),
                Arguments.of(encode(Map.of("key", "value")), ParseException.class),
                Arguments.of(encode(
                                Map.of(
                                        "kty", KeyType.RSA.getValue(),
                                        "k", "key",
                                        "kid", "id"
                                )),
                        ParseException.class),
                Arguments.of(encode(
                                Map.of(
                                        "kty", "dummy",
                                        "k", "key",
                                        "kid", "id"
                                )),
                        ParseException.class)
        );
    }

    public static Stream<Arguments> failedAllMasterKeysSetupParams() {
        return Stream.of(
                Arguments.of("", IllegalArgumentException.class),
                Arguments.of("notebase64encoded", IllegalStateException.class),
                Arguments.of(encode("base64encoded"), IllegalStateException.class),
                Arguments.of(encode(
                                Map.of(
                                        "kty", KeyType.OCT.getValue(),
                                        "k", "key",
                                        "kid", "id"
                                )),
                        IllegalStateException.class),
                // user defined exception
                Arguments.of(encode(Collections.emptyList()), IllegalArgumentException.class),
                Arguments.of(encode(Collections.emptyMap()), IllegalStateException.class),
                Arguments.of(encode(List.of("some value")), ParseException.class),
                Arguments.of(encode(List.of(Map.of("key", "value"))), ParseException.class),
                Arguments.of(encode(
                                List.of(
                                        Map.of(
                                                "kty", KeyType.RSA.getValue(),
                                                "k", "key",
                                                "kid", "id"
                                        )
                                )),
                        ParseException.class),
                Arguments.of(encode(
                                List.of(
                                        Map.of(
                                                "kty", "dummy",
                                                "k", "key",
                                                "kid", "id"
                                        )
                                )),
                        ParseException.class)
        );
    }

    @ParameterizedTest
    @MethodSource("failedMasterKeySetupParams")
    void crypto_InitMasterKey_OnSkipKeySetup_ThrowsException(String masterKey, Class<? extends  Exception> eClass) {
        cryptoProperties.setMasterKey(masterKey);
        Assertions.assertThrows(eClass, () -> cryptoProperties.init());
    }

    @ParameterizedTest
    @MethodSource("failedAllMasterKeysSetupParams")
    void crypto_InitAllMasterKeys_OnSkipKeySetup_ThrowsException(String allMasterKeys, Class<? extends  Exception> eClass) {
        cryptoProperties.setMasterKey(encode(mockMasterKey.toJSONObject()));
        cryptoProperties.setAllMasterKeys(allMasterKeys);
        Assertions.assertThrows(eClass, () -> cryptoProperties.init());
    }

    public static Stream<Arguments> failedKeysSetupParams() {
        return Stream.concat(
                Stream.of(CryptoTestUtils.generateEncryptionKey(UUID.randomUUID().toString()),
                                CryptoTestUtils.generateEncryptionKey())
                        .flatMap(currentKey -> Stream.of(
                                Arguments.of(
                                        encode(currentKey.toJSONObject()),
                                        encode(List.of(currentKey.toJSONObject())),
                                        InvalidConfigurationPropertyValueException.class),
                                Arguments.of(encode(currentKey.toJSONObject()),
                                        encode(List.of(currentKey.toJSONObject())),
                                        InvalidConfigurationPropertyValueException.class)
                        )),
                Stream.of(
                        Arguments.of(encode(mockMasterKey.toJSONObject()),
                                encode(List.of(generateMasterEncryptionKey().toJSONObject())),
                                InvalidConfigurationPropertyValueException.class),
                        Arguments.of(encode(mockMasterKey.toJSONObject()),
                                encode(List.of(new OctetSequenceKey.Builder(
                                        generateMasterEncryptionKey())
                                        .keyID(mockMasterKey.getKeyID())
                                        .build().toJSONObject())),
                                InvalidConfigurationPropertyValueException.class
                        ),
                        Arguments.of(encode(new OctetSequenceKey.Builder(new byte[10]).build().toJSONObject()),
                                encode(List.of(mockMasterKey.toJSONObject())),
                                InvalidConfigurationPropertyValueException.class)
                )
        );
    }

    @ParameterizedTest
    @MethodSource("failedKeysSetupParams")
    void crypto_InitKeys_ThrowsException(String masterKey, String allMasterKey, Class<? extends  Exception> eClass) {
        cryptoProperties.setMasterKey(masterKey);
        cryptoProperties.setAllMasterKeys(allMasterKey);
        Assertions.assertThrows(eClass, () -> cryptoProperties.init());
    }


    public static Stream<Arguments> successKeysSetupParams() {
        return Stream.of(
                        // UUIDv4
                        CryptoTestUtils.generateEncryptionKey(UUID.randomUUID().toString()),
                        // Not UUID
                        CryptoTestUtils.generateEncryptionKey())
            .flatMap(additionalMek -> Stream.of(
                Arguments.of(
                    encode(mockMasterKey.toJSONObject()),
                    encode(List.of(mockMasterKey.toJSONObject(),
                        additionalMek.toJSONObject())),
                    mockMasterKey, 2),
                Arguments.of(encode(mockMasterKey.toJSONObject()),
                    encode(List.of(mockMasterKey.toJSONObject())),
                    mockMasterKey, 1)
            ));
    }

    @ParameterizedTest
    @MethodSource("successKeysSetupParams")
    void crypto_InitKeys_ShouldSucceed(String encodedCurrentMasterKey, String encodedMasterKeysList, OctetSequenceKey expectedMasterKey, int masterKeysListSize) {
        cryptoProperties.setMasterKey(encodedCurrentMasterKey);
        cryptoProperties.setAllMasterKeys(encodedMasterKeysList);

        Assertions.assertDoesNotThrow(cryptoProperties::init);

        assertEquals(expectedMasterKey, cryptoProperties.getActualParsedMasterKey());
        assertEquals(masterKeysListSize, cryptoProperties.getParsedAllMasterKeys().size());
        assertEquals(expectedMasterKey, cryptoProperties.getParsedAllMasterKeys().get(expectedMasterKey.getKeyID()));
    }


    public static Stream<Arguments> keysAndGracePeriodArgs() {
        var newMek = generateMasterEncryptionKey();
        return Stream.of(
                Arguments.of(
                        encode(newMek.toJSONObject()),
                        encode(List.of(mockMasterKey.toJSONObject(),
                                newMek.toJSONObject())),
                        Duration.ofDays(10),
                        mockMasterKey),
                Arguments.of(
                        encode(newMek.toJSONObject()),
                        encode(List.of(mockMasterKey.toJSONObject(),
                                newMek.toJSONObject())),
                        Duration.ZERO,
                        newMek),
                Arguments.of(
                        encode(newMek.toJSONObject()),
                        encode(List.of(newMek.toJSONObject())),
                        Duration.ofDays(10),
                        newMek),
                Arguments.of(
                        encode(newMek.toJSONObject()),
                        encode(List.of(newMek.toJSONObject())),
                        Duration.ZERO,
                        newMek)
        );
    }


    @ParameterizedTest
    @MethodSource("keysAndGracePeriodArgs")
    void getValidMek_onKeysAndGracePeriod_succeed(String masterKey, String allMasterKey, Duration gracePeriod, OctetSequenceKey expectedMek) {
        cryptoProperties.setMasterKey(masterKey);
        cryptoProperties.setAllMasterKeys(allMasterKey);

        Assertions.assertDoesNotThrow(() -> cryptoProperties.init());

        when(encryptionProperties.getMekRotationGracePeriod())
                .thenReturn(gracePeriod);

        Assertions.assertEquals(expectedMek, cryptoProperties.getValidMek());
    }
}


@Slf4j
class CryptoPropertiesRefreshTest {
    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withUserConfiguration(TrivialConfiguration.class);

    @TestConfiguration
    @Configuration
    static class TrivialConfiguration {
        @Bean
        public CryptoProperties cryptoProperties() {
            var cryptoProperties = new CryptoProperties();
            var key = generateMasterEncryptionKey();
            cryptoProperties.setMasterKey(encode(key.toJSONObject()));
            cryptoProperties.setAllMasterKeys(encode(List.of(key.toJSONObject())));

            return spy(cryptoProperties);
        }

        @Bean
        public EncryptionProperties encryptionProperties() {
            return new EncryptionProperties();
        }

        @Bean
        public MeterRegistry meterRegistry() {
            return new SimpleMeterRegistry();
        }

        @Bean
        public EncryptionMetricsRegistry encryptionMetricsRegistry() {
            return new EncryptionMetricsRegistry();
        }
    }

    @Test
    void onRefreshScopeRefreshed_onPublishedRefreshScopeRefreshedEvent() {
        contextRunner.run(context -> {
            CryptoProperties cryptoProperties = context.getBean(CryptoProperties.class);

            context.publishEvent(new RefreshScopeRefreshedEvent());

            verify(cryptoProperties, timeout(2000).times(1))
                    .onRefreshScopeRefreshed(Mockito.any(RefreshScopeRefreshedEvent.class));
        });
    }
}