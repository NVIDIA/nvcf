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

package com.nvidia.apikeys.services;

import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.OWNER_INFO_ONLY_MODEL;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.TestData;
import com.nvidia.apikeys.converters.KeyOwnerVoBuilder;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import java.time.Instant;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeyOwnerVoBuilderTest {

    @Mock
    private MillisecondPrecisionClock clock;

    @InjectMocks
    private KeyOwnerVoBuilder keyOwnerVoBuilder;

    @Test
    void getNewKeyOwnerVo() {
        when(clock.instant()).thenReturn(TEST_TIME);
        KeyOwnerVo keyOwnerVo = KEY_OWNER_VO_1.toBuilder()
                .ownerStatusUpdatedAt(TEST_TIME)
                .build();

        assertThat(keyOwnerVoBuilder.getNewKeyOwnerVo(KeyOwnerType.USER, TestData.USER_KEY_OWNER_ID_1))
                .isEqualTo(keyOwnerVo);
    }

    @Test
    void getKeyOwnerVoFromExistingKey() {
        assertThat(keyOwnerVoBuilder.getKeyOwnerVoFromExistingKey(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .isEqualTo(KEY_OWNER_VO_1);
    }

    @Test
    void getKeyOwnerVoFromModel() {
        assertThat(keyOwnerVoBuilder.getKeyOwnerVoFromModel(OWNER_INFO_ONLY_MODEL))
                .isEqualTo(KEY_OWNER_VO_1);
    }
}
