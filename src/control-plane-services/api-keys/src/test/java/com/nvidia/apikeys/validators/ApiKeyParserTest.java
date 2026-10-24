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

package com.nvidia.apikeys.validators;

import static com.nvidia.apikeys.TestData.API_KEY_HASH_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.AssertionsForClassTypes.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.services.CredentialService;
import com.nvidia.boot.exceptions.BadRequestException;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EmptySource;
import org.junit.jupiter.params.provider.NullSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ApiKeyParserTest {

    @Mock
    private CredentialService credentialServiceMock;

    private ApiKeyParser keyParser;


    @BeforeEach
    public void init() {
        keyParser = new ApiKeyParser(credentialServiceMock, "prefix-");
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "prefix-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_",
            "prefix-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_",
            "prefix-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_",
            "prefix--abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
    })
    void assertKeyFormatValid(String key) {
        String keyBody = key.substring(7, 50);
        String keyHashPrefix = key.substring(50);
        String fullHash = keyHashPrefix + "suffix";
        when(credentialServiceMock.getKeyHash(keyBody)).thenReturn(fullHash);

        assertThat(keyParser.rawApiKeyToHash(key)).isEqualTo(fullHash);
    }

    @ParameterizedTest
    @ValueSource(strings = {
            // wrong prefix
            "prefiX-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_",
            // over length limit
            "prefix-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-__",
            // under length limit
            "prefix-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-",
            // no prefix
            "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_",
            // spec chars
            "prefix-!bcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_",
            "prefix-+abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
    })
    void assertKeyFormatValid_throwsWhenInvalid(String key) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> keyParser.rawApiKeyToHash(key),
                "api key format invalid");
    }

    @NullSource
    @EmptySource
    @ParameterizedTest
    void assertKeyFormatValid_throwsWhenEmpty(String key) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> keyParser.rawApiKeyToHash(key), "api key empty");
    }

    @Test
    void validate_shouldThrowIfHashPrefixMismatch() {
        String apiKey = "prefix-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
        when(credentialServiceMock.getKeyHash("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq"))
                .thenReturn(API_KEY_HASH_1);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> keyParser.rawApiKeyToHash(apiKey), "Invalid key");
    }

}
