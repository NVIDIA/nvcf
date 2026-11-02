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

import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.services.KeysV2Service;
import com.nvidia.apikeys.vo.IntrospectionKeyVo;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import java.util.stream.IntStream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class IntrospectionKeyOwnerValidatorTest {

    private static final IntrospectionKeyVo KEY_1 = new IntrospectionKeyVo(NCA_ID_1, KEY_VO_1);
    private static final String OTHER_SERVICE_ID = "other-service";

    @Mock
    private KeysV2Service keysV2Service;

    @InjectMocks
    private IntrospectionKeyOwnerValidator validator;

    private void givenStatus(String ncaId, String ownerId, String issuerServiceId,
                             KeyOwnerStatus status) {
        when(keysV2Service.getEffectiveOwnerStatus(ncaId, USER, ownerId, issuerServiceId))
                .thenReturn(status);
    }

    @Test
    void assertKeyOwnerValid_shouldCacheOwnerStatus() {
        givenStatus(NCA_ID_1, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.ACTIVE);

        IntStream.range(0, 100).forEach(i -> validator.assertKeyOwnerValid(KEY_1));
        validator.invalidateCaches();
        IntStream.range(0, 100).forEach(i -> validator.assertKeyOwnerValid(KEY_1));

        verify(keysV2Service, times(2))
                .getEffectiveOwnerStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1);
    }

    @Test
    void assertKeyOwnerValid_shouldThrowIfNotActive() {
        givenStatus(NCA_ID_1, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.SUSPENDED);

        assertThrowsExceptionWithDetails(
                NotFoundException.class, () -> validator.assertKeyOwnerValid(KEY_1),
                "Key not found.");
    }

    @Test
    void assertKeyOwnerValid_shouldCheckTheKeysIssuerService() {
        givenStatus(NCA_ID_1, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.ACTIVE);
        givenStatus(NCA_ID_1, USER_KEY_OWNER_ID_1, OTHER_SERVICE_ID, KeyOwnerStatus.SUSPENDED);
        var otherServiceKey = new IntrospectionKeyVo(
                NCA_ID_1, KEY_VO_1.toBuilder().issuerServiceId(OTHER_SERVICE_ID).build());

        validator.assertKeyOwnerValid(KEY_1);
        assertThrowsExceptionWithDetails(
                NotFoundException.class, () -> validator.assertKeyOwnerValid(otherServiceKey),
                "Key not found.");
    }

    @Test
    void assertKeyOwnerValid_shouldCacheEachAccountSeparately() {
        givenStatus(NCA_ID_1, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.ACTIVE);
        givenStatus("nca-2", USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.SUSPENDED);

        validator.assertKeyOwnerValid(KEY_1);
        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> validator.assertKeyOwnerValid(new IntrospectionKeyVo("nca-2", KEY_VO_1)),
                "Key not found.");
    }

    @Test
    void assertKeyOwnerValid_shouldHandleCacheLoadFailure() {
        when(keysV2Service.getEffectiveOwnerStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1,
                                                   SERVICE_ID_1))
                .thenThrow(new RuntimeException("Database error"));

        IllegalStateException exception = assertThrows(
                IllegalStateException.class,
                () -> validator.assertKeyOwnerValid(KEY_1)
        );
        assertThat(exception.getMessage()).isEqualTo("Failed to retrieve key owner details");
        assertThat(exception.getCause()).isInstanceOf(RuntimeException.class);
        assertThat(exception.getCause().getMessage()).contains("Database error");
    }

    @Test
    void assertKeyOwnerValid_shouldHandleNullKeyOwnerType() {
        KeyVo invalidKey = KEY_VO_1.toBuilder()
                .ownerType(null)
                .build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertKeyOwnerValid(new IntrospectionKeyVo(NCA_ID_1, invalidKey)),
                "Invalid key owner"
        );
        verifyNoInteractions(keysV2Service);
    }

    @Test
    void assertKeyOwnerValid_shouldHandleNullKeyOwnerId() {
        KeyVo invalidKey = KEY_VO_1.toBuilder()
                .ownerId(null)
                .build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertKeyOwnerValid(new IntrospectionKeyVo(NCA_ID_1, invalidKey)),
                "Invalid key owner"
        );
        verifyNoInteractions(keysV2Service);
    }

    @Test
    void assertKeyOwnerValid_shouldHandleCacheSizeLimit() {
        when(keysV2Service.getEffectiveOwnerStatus(any(), any(), any(), any()))
                .thenReturn(KeyOwnerStatus.ACTIVE);

        for (int i = 0; i < 1000; i++) {
            KeyVo key = KEY_VO_1.toBuilder()
                    .ownerId("user-" + i)
                    .build();
            validator.assertKeyOwnerValid(new IntrospectionKeyVo(NCA_ID_1, key));
        }

        validator.assertKeyOwnerValid(KEY_1);
        verify(keysV2Service, times(1))
                .getEffectiveOwnerStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1);
    }
}
