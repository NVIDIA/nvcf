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

package com.nvidia.apikeys.service;

import static com.nvidia.apikeys.services.CredentialService.KEY_HINT_LENGHT;
import static com.nvidia.apikeys.services.CredentialService.KEY_SUFFIX_MASK;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.config.hmac.HmacEncoder.HMAC_SHA_3_256;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.atLeast;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.apikeys.TestData;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.config.exceptions.ApiKeyException;
import com.nvidia.apikeys.config.hmac.HmacEncoder;
import com.nvidia.apikeys.config.hmac.HmacWorkersPooledFactory;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.repositories.KeyRepository;
import com.nvidia.apikeys.services.CredentialService;
import com.nvidia.apikeys.validators.NakPropertiesValidator;
import com.nvidia.apikeys.vo.GeneratedKeyVo;
import java.security.SecureRandom;
import java.util.Base64;
import java.util.Collections;
import java.util.HashSet;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.pool2.impl.GenericKeyedObjectPool;
import org.apache.commons.pool2.impl.GenericKeyedObjectPoolConfig;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.Spy;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.util.JdkIdGenerator;

@Slf4j
@ExtendWith(MockitoExtension.class)
class CredentialServiceTest {

    @Spy
    private final HmacEncoder hmacEncoder = new HmacEncoder(new GenericKeyedObjectPool<>(
            new HmacWorkersPooledFactory(), new GenericKeyedObjectPoolConfig<>()));

    @Spy
    private final NakProperties nakProperties = NakProperties.builder()
            .dataDomainKey(TestData.DATA_DOMAIN_KEY)
            .keyPrefix("nvcfapi-test-")
            .build();

    @Spy
    private final JdkIdGenerator jdkIdGenerator = new JdkIdGenerator();

    @Mock
    private NakPropertiesValidator validatorMock;

    @Mock
    private KeyRepository keyRepositoryMock;

    private CredentialService service;

    @BeforeEach
    void beforeAll() {
        when(validatorMock.validate(nakProperties)).thenReturn(nakProperties);
        service = new CredentialService(
                hmacEncoder, validatorMock, nakProperties, jdkIdGenerator,
                keyRepositoryMock);
    }

    @Test
    void generateDataDomainKey() {
        SecureRandom secureRandom = new SecureRandom();
        byte[] bytes = secureRandom.generateSeed(136); // 1088 bits
        String key = Base64.getUrlEncoder().withoutPadding().encodeToString(bytes);
        log.info("new random key: {}", key);
    }

    @Test
    void shouldConvertEncoderException()
            throws Exception {
        HmacEncoder encoderMock = mock(HmacEncoder.class);
        service = new CredentialService(
                encoderMock, validatorMock, nakProperties, jdkIdGenerator,
                keyRepositoryMock);
        when(encoderMock.hmac(eq(HMAC_SHA_3_256), any(), any()))
                .thenThrow(new RuntimeException("encoding exception"));

        assertThrowsExceptionWithDetails(
                ApiKeyException.class, () -> service.generateApiKey(), "failed to process key");
    }

    @Test
    void generateApiKey_shouldGenerateKey() {
        GeneratedKeyVo generatedKeyVo = service.generateApiKey();
        assertThat(generatedKeyVo.getFormattedApiKey())
                .isNotNull()
                .isNotEmpty()
                .matches("nvcfapi-test-[a-zA-Z0-9-_]{64}")
                .hasSize(77);
        // 11 of prefix + 64 of key (43chars of 256bit key + 21 char of keyId prefix)

        assertThat(generatedKeyVo.getKeyHash())
                .isNotNull()
                .isNotEmpty()
                .hasSize(43);

        assertThat(generatedKeyVo.getKeySuffix())
                .isNotNull()
                .matches("nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}");

        assertThat(generatedKeyVo.getKeyId())
                .matches("([a-f0-9]{8}(-[a-f0-9]{4}){4}[a-f0-9]{8})");
    }

    @Test
    void generateApiKey_shouldGenerateKeyAgainIfHashAlreadyExists() {
        when(keyRepositoryMock.findByKeyHash(anyString()))
                .thenReturn(Optional.of(Mockito.mock(KeyModel.class)))
                .thenReturn(Optional.of(Mockito.mock(KeyModel.class)))
                .thenReturn(Optional.empty());

        GeneratedKeyVo generatedKeyVo = service.generateApiKey();
        assertThat(generatedKeyVo.getFormattedApiKey())
                .isNotNull()
                .isNotEmpty()
                .matches("nvcfapi-test-[a-zA-Z0-9-_]{64}")
                .hasSize(77);
        // 11 of prefix + 64 of key (43chars of 256bit key + 21 char of keyId prefix)

        assertThat(generatedKeyVo.getKeyHash())
                .isNotNull()
                .isNotEmpty()
                .hasSize(43);

        assertThat(generatedKeyVo.getKeySuffix())
                .isNotNull()
                .matches("nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}");

        assertThat(generatedKeyVo.getKeyId())
                .matches("([a-f0-9]{8}(-[a-f0-9]{4}){4}[a-f0-9]{8})");

        verify(keyRepositoryMock, times(3)).findByKeyHash(anyString());
    }

    @Test
    void generateApiKey_shouldThrowIfUnableToGenerateUniqueHash() {
        when(keyRepositoryMock.findByKeyHash(anyString()))
                .thenReturn(Optional.of(Mockito.mock(KeyModel.class)));

        assertThat(assertThrows(
                ApiKeyException.class,
                () -> service.generateApiKey()
        )).hasMessageContaining("Failed to generate unique key, please retry");

        verify(keyRepositoryMock, times(50)).findByKeyHash(anyString());
    }

    @Test
    void getKeyHash() {
        GeneratedKeyVo generatedKeyVo = service.generateApiKey();

        String formattedApiKey = generatedKeyVo.getFormattedApiKey();
        int prefixLength = nakProperties.getKeyPrefix().length();
        int keyBodyLength = 43;

        String partialHash = formattedApiKey.substring(keyBodyLength + prefixLength);
        String keyBody = formattedApiKey.substring(prefixLength, keyBodyLength + prefixLength);

        assertThat(service.getKeyHash(keyBody))
                .startsWith(partialHash)
                .hasSize(keyBodyLength);
    }

    @Test
    void getKeyHash_shouldHandleValidBase64Input() {
        String validBase64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
        String hash = service.getKeyHash(validBase64);
        assertThat(hash)
                .isNotNull()
                .isNotEmpty()
                .hasSize(43);
    }

    @Test
    void generateApiKey_shouldGenerateUniqueKeys() {
        GeneratedKeyVo key1 = service.generateApiKey();
        GeneratedKeyVo key2 = service.generateApiKey();
        
        assertThat(key1.getKeyHash()).isNotEqualTo(key2.getKeyHash());
        assertThat(key1.getKeyId()).isNotEqualTo(key2.getKeyId());
        assertThat(key1.getFormattedApiKey()).isNotEqualTo(key2.getFormattedApiKey());
    }

    @Test
    void generateApiKey_shouldGenerateKeysWithCorrectFormat() {
        GeneratedKeyVo key = service.generateApiKey();
        
        // Check key format
        assertThat(key.getFormattedApiKey())
                .startsWith(nakProperties.getKeyPrefix())
                .hasSize(77); // prefix + key body + key ID suffix
        
        // Check key suffix format
        assertThat(key.getKeySuffix())
                .startsWith(nakProperties.getKeyPrefix())
                .contains(KEY_SUFFIX_MASK)
                .hasSize(nakProperties.getKeyPrefix().length() + 
                        KEY_SUFFIX_MASK.length() + KEY_HINT_LENGHT);
    }

    @Test
    void generateApiKey_shouldHandleMaximumAttemptsExceeded() {
        when(keyRepositoryMock.findByKeyHash(anyString()))
                .thenReturn(Optional.of(mock(KeyModel.class)));

        assertThrowsExceptionWithDetails(
                ApiKeyException.class,
                () -> service.generateApiKey(),
                "Failed to generate unique key, please retry"
        );

        verify(keyRepositoryMock, atLeast(10)).findByKeyHash(anyString());
    }

    @Test
    void getKeyHash_shouldHandleDifferentKeyLengths() {
        // Test with different valid base64 lengths
        String[] testKeys = {
            "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_",
            "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
            "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_"
        };

        for (String key : testKeys) {
            String hash = service.getKeyHash(key);
            assertThat(hash)
                    .isNotNull()
                    .isNotEmpty()
                    .hasSize(43);
        }
    }

    @Test
    void constructor_shouldThrowIfConfigValidationFails() {
        when(validatorMock.validate(nakProperties))
                .thenThrow(new BadRequestException("Invalid config"));

        assertThrows(
                BadRequestException.class,
                () -> new CredentialService(
                        hmacEncoder, validatorMock, nakProperties, jdkIdGenerator,
                        keyRepositoryMock)
        );
    }

    @Test
    void generateApiKey_shouldHandleEmptyKeyPrefix() {
        NakProperties configWithEmptyPrefix = NakProperties.builder()
                .dataDomainKey(TestData.DATA_DOMAIN_KEY)
                .keyPrefix("")
                .build();
        when(validatorMock.validate(configWithEmptyPrefix)).thenReturn(configWithEmptyPrefix);
        
        CredentialService serviceWithEmptyPrefix = new CredentialService(
                hmacEncoder, validatorMock, configWithEmptyPrefix, jdkIdGenerator,
                keyRepositoryMock);

        GeneratedKeyVo key = serviceWithEmptyPrefix.generateApiKey();
        assertThat(key.getFormattedApiKey())
                .doesNotStartWith("nvcfapi-test-")
                .hasSize(64); // key body + key ID suffix
    }

    @Test
    void generateApiKey_shouldHandleLongKeyPrefix() {
        String longPrefix = "nvcfapi-test-very-long-prefix-";
        NakProperties configWithLongPrefix = NakProperties.builder()
                .dataDomainKey(TestData.DATA_DOMAIN_KEY)
                .keyPrefix(longPrefix)
                .build();
        when(validatorMock.validate(configWithLongPrefix)).thenReturn(configWithLongPrefix);
        
        CredentialService serviceWithLongPrefix = new CredentialService(
                hmacEncoder, validatorMock, configWithLongPrefix, jdkIdGenerator,
                keyRepositoryMock);

        GeneratedKeyVo key = serviceWithLongPrefix.generateApiKey();
        assertThat(key.getFormattedApiKey())
                .startsWith(longPrefix)
                .hasSize(longPrefix.length() + 64); // prefix + key body + key ID suffix
    }

    @Test
    void getKeyHash_shouldHandleInvalidBase64Input() {
        assertThrows(
                IllegalArgumentException.class,
                () -> service.getKeyHash("invalid-base64!@#$")
        );
    }

    @Test
    void generateApiKey_shouldCompleteWithinReasonableTime() {
        long startTime = System.currentTimeMillis();
        GeneratedKeyVo key = service.generateApiKey();
        long endTime = System.currentTimeMillis();
        
        assertThat(endTime - startTime).isLessThan(1000); // Should complete within 1 second
        assertThat(key.getFormattedApiKey()).isNotNull();
    }

    @Test
    void generateApiKey_shouldHandleConcurrentGeneration() throws InterruptedException {
        int threadCount = 10;
        CountDownLatch latch = new CountDownLatch(threadCount);
        Set<String> generatedKeys = Collections.synchronizedSet(new HashSet<>());
        
        for (int i = 0; i < threadCount; i++) {
            new Thread(() -> {
                try {
                    GeneratedKeyVo key = service.generateApiKey();
                    generatedKeys.add(key.getKeyHash());
                } finally {
                    latch.countDown();
                }
            }).start();
        }
        
        latch.await(5, TimeUnit.SECONDS);
        assertThat(generatedKeys).hasSize(threadCount);
    }

    @Test
    void generateApiKey_shouldGenerateKeysWithCorrectEntropy() {
        GeneratedKeyVo key = service.generateApiKey();
        String keyBody = key.getFormattedApiKey()
                .substring(nakProperties.getKeyPrefix().length(), 
                          nakProperties.getKeyPrefix().length() + 43);
        
        byte[] decodedKey = Base64.getUrlDecoder().decode(keyBody);
        assertThat(decodedKey).hasSize(CredentialService.API_KEY_ENTROPY_BYTES);
    }

}
