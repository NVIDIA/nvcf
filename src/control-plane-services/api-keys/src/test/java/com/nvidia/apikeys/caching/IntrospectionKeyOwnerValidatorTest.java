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

package com.nvidia.apikeys.caching;

import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Optional;
import java.util.stream.IntStream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class IntrospectionKeyOwnerValidatorTest {

    @Mock
    private KeysDao keysDao;

    @InjectMocks
    private IntrospectionKeyOwnerValidator validator;

    @Test
    void assertKeyOwnerValid_shouldFetchUserInfo() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(Optional.of(KEY_OWNER_VO_1));

        IntStream.range(0, 100).forEach(i -> validator.assertKeyOwnerValid(KEY_VO_1));
        validator.invalidateCaches();
        IntStream.range(0, 100).forEach(i -> validator.assertKeyOwnerValid(KEY_VO_1));

        verify(keysDao, times(2))
                .getKeyOwner(USER, USER_KEY_OWNER_ID_1);
    }

    @Test
    void assertKeyOwnerValid_shouldThrowIfNoKeyOwner() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.empty());
        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> validator.assertKeyOwnerValid(KEY_VO_1),
                "Invalid key owner");
    }

    @Test
    void assertKeyOwnerValid_shouldThrowIfNotActive() {
        KeyOwnerVo suspendedOwner = KEY_OWNER_VO_1.toBuilder()
                .ownerStatus(KeyOwnerStatus.SUSPENDED)
                .build();
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(Optional.of(suspendedOwner));

        assertThrowsExceptionWithDetails(
                NotFoundException.class, () -> validator.assertKeyOwnerValid(KEY_VO_1),
                "Key not found.");
    }

    @Test
    void assertKeyOwnerValid_shouldHandleCacheLoadFailure() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenThrow(new RuntimeException("Database error"));

        IllegalStateException exception = assertThrows(
                IllegalStateException.class,
                () -> validator.assertKeyOwnerValid(KEY_VO_1)
        );
        assertThat(exception.getMessage()).isEqualTo("Failed to retrieve key owner details");
        assertThat(exception.getCause()).isInstanceOf(RuntimeException.class);
        assertThat(exception.getCause().getMessage()).contains("Database error");
    }

    @Test
    void invalidateCaches_shouldForceCacheReload() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(Optional.of(KEY_OWNER_VO_1));

        // First call - cache miss
        validator.assertKeyOwnerValid(KEY_VO_1);
        
        // Second call - cache hit
        validator.assertKeyOwnerValid(KEY_VO_1);
        
        // Invalidate cache
        validator.invalidateCaches();
        
        // Third call - cache miss after invalidation
        validator.assertKeyOwnerValid(KEY_VO_1);

        verify(keysDao, times(2))
                .getKeyOwner(USER, USER_KEY_OWNER_ID_1);
    }

    @Test
    void assertKeyOwnerValid_shouldHandleNullKeyOwnerType() {
        KeyVo invalidKey = KEY_VO_1.toBuilder()
                .ownerType(null)
                .build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertKeyOwnerValid(invalidKey),
                "Invalid key owner"
        );
    }

    @Test
    void assertKeyOwnerValid_shouldHandleNullKeyOwnerId() {
        KeyVo invalidKey = KEY_VO_1.toBuilder()
                .ownerId(null)
                .build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertKeyOwnerValid(invalidKey),
                "Invalid key owner"
        );
    }

    @Test
    void assertKeyOwnerValid_shouldHandleCacheSizeLimit() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(Optional.of(KEY_OWNER_VO_1));

        // Create 1000 different keys to reach cache size limit
        for (int i = 0; i < 1000; i++) {
            KeyVo key = KEY_VO_1.toBuilder()
                    .ownerId("user-" + i)
                    .build();
            KeyOwnerVo owner = KEY_OWNER_VO_1.toBuilder()
                    .ownerId("user-" + i)
                    .build();
            when(keysDao.getKeyOwner(USER, "user-" + i))
                    .thenReturn(Optional.of(owner));
            validator.assertKeyOwnerValid(key);
        }

        // Verify that the cache is working correctly even at capacity
        validator.assertKeyOwnerValid(KEY_VO_1);
        verify(keysDao, times(1))
                .getKeyOwner(USER, USER_KEY_OWNER_ID_1);
    }
}
