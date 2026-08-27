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

import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_EXPIRES_AT_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import java.time.Clock;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeyExpirationValidatorTest {

    @Mock
    private Clock clockMock;

    @InjectMocks
    private KeyExpirationValidator validator;

    @Test
    void validateStatus_noChangesIfKeyNotExpired() {
        when(clockMock.instant()).thenReturn(KEY_EXPIRES_AT_1.plusSeconds(-1));

        assertThat(validator.validateStatus(KEY_VO_1)).isEqualTo(KEY_VO_1);
    }

    @Test
    void validateStatus_noChangesIfKeySuspended() {
        KeyVo key = KEY_VO_1.toBuilder()
                .keyStatus(KeyStatus.SUSPENDED)
                .build();

        assertThat(validator.validateStatus(key)).isEqualTo(key);
    }

    @Test
    void validateStatus_setStatusToExpired() {
        when(clockMock.instant()).thenReturn(KEY_EXPIRES_AT_1.plusSeconds(1));

        KeyVo expected = KEY_VO_1.toBuilder()
                .keyStatus(KeyStatus.EXPIRED)
                .build();

        assertThat(validator.validateStatus(KEY_VO_1)).isEqualTo(expected);
    }

    @Test
    void validateStatus_KeyByOwner_noChangesIfKeyNotExpired() {
        when(clockMock.instant()).thenReturn(KEY_EXPIRES_AT_1.plusSeconds(-1));

        assertThat(validator.validateStatus(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .isEqualTo(KEY_BY_OWNER_AND_SERVICE_VO_1);
    }

    @Test
    void validateStatus_KeyByOwner_noChangesIfKeySuspended() {
        var key = KEY_BY_OWNER_AND_SERVICE_VO_1.toBuilder()
                .keyStatus(KeyStatus.SUSPENDED)
                .build();

        assertThat(validator.validateStatus(key)).isEqualTo(key);
    }

    @Test
    void validateStatus_KeyByOwner_setStatusToExpired() {
        when(clockMock.instant()).thenReturn(KEY_EXPIRES_AT_1.plusSeconds(1));

        var expected = KEY_BY_OWNER_AND_SERVICE_VO_1.toBuilder()
                .keyStatus(KeyStatus.EXPIRED)
                .build();

        assertThat(validator.validateStatus(KEY_BY_OWNER_AND_SERVICE_VO_1)).isEqualTo(expected);
    }

}
