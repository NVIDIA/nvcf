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
import static com.nvidia.apikeys.TestData.INTROSPECTION_REQUEST_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.AssertionsForClassTypes.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.caching.CachingKeysLoader;
import com.nvidia.apikeys.caching.IntrospectionKeyOwnerValidator;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EmptySource;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.NullSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class IntrospectionValidatorTest {

    @Mock
    private CachingKeysLoader keyLoader;
    @Mock
    private IntrospectionKeyOwnerValidator introspectionKeyOwnerValidatorMock;
    @InjectMocks
    private IntrospectionValidator validator;

    @ParameterizedTest
    @NullSource
    @EmptySource
    @ValueSource(strings = "invalid")
    void validate_shouldThrowIfAudienceServiceMismatch(String audienceServiceId) {
        KeyVo key = KEY_VO_1.toBuilder()
                .audienceServiceIds(
                        audienceServiceId == null ? null : Set.of(audienceServiceId))
                .build();
        when(keyLoader.loadKeyByHash(API_KEY_HASH_1)).thenReturn(key);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(INTROSPECTION_REQUEST_1, API_KEY_HASH_1),
                "audience_service_id invalid");

        verifyNoInteractions(introspectionKeyOwnerValidatorMock);
    }

    @ParameterizedTest
    @EnumSource(value = KeyStatus.class, names = "ACTIVE", mode = EnumSource.Mode.EXCLUDE)
    void validate_shouldThrowIfServiceNotActive(KeyStatus keyStatus) {
        KeyVo key = KEY_VO_1.toBuilder()
                .keyStatus(keyStatus)
                .build();
        when(keyLoader.loadKeyByHash(API_KEY_HASH_1)).thenReturn(key);

        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> validator.validate(INTROSPECTION_REQUEST_1, API_KEY_HASH_1),
                "Key not found");

        verifyNoInteractions(introspectionKeyOwnerValidatorMock);
    }

    @Test
    void validate_shouldReturnValidatedKey() {
        when(keyLoader.loadKeyByHash(API_KEY_HASH_1)).thenReturn(KEY_VO_1);

        assertThat(validator.validate(INTROSPECTION_REQUEST_1, API_KEY_HASH_1))
                .isEqualTo(KEY_VO_1);

        verify(introspectionKeyOwnerValidatorMock).assertKeyOwnerValid(KEY_VO_1);
    }

    @Test
    void validate_shouldThrowIfAudienceServiceUndefined() {
        KeyVo key = KEY_VO_1.toBuilder()
                .audienceServiceIds(null)
                .build();
        when(keyLoader.loadKeyByHash(API_KEY_HASH_1)).thenReturn(key);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(INTROSPECTION_REQUEST_1, API_KEY_HASH_1),
                "audience_service_id invalid");
        verifyNoInteractions(introspectionKeyOwnerValidatorMock);
    }

    @Test
    void validate_shouldRethrowKnownExceptions() {
        when(keyLoader.loadKeyByHash(API_KEY_HASH_1)).thenReturn(KEY_VO_1);
        doThrow(new NotFoundException("user not found"))
                .when(introspectionKeyOwnerValidatorMock).assertKeyOwnerValid(KEY_VO_1);

        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                ()-> validator.validate(INTROSPECTION_REQUEST_1, API_KEY_HASH_1),
                "user not found");
    }

    @Test
    void validate_shouldThrowGenericExceptions() {
        when(keyLoader.loadKeyByHash(API_KEY_HASH_1)).thenReturn(KEY_VO_1);
        doThrow(new NullPointerException("null"))
                .when(introspectionKeyOwnerValidatorMock).assertKeyOwnerValid(KEY_VO_1);

        assertThrows(
                IllegalStateException.class,
                ()-> validator.validate(INTROSPECTION_REQUEST_1, API_KEY_HASH_1),
                "Unexpected error during key introspection");
    }

}
