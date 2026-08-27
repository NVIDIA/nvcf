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

package com.nvidia.apikeys.converters;

import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.dto.keys.KeyOwnerDto;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.mockito.InjectMocks;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeyOwnerConverterTest {

    @InjectMocks
    private KeyOwnerConverter converter;

    @ParameterizedTest
    @EnumSource(KeyOwnerStatus.class)
    void toDto(KeyOwnerStatus status) {
        var vo = KEY_OWNER_VO_1.toBuilder()
                .ownerStatus(status)
                .ownerStatusUpdatedAt(TEST_TIME)
                .build();

        var expectedDto = KeyOwnerDto.builder()
                .status(status)
                .ownerType(USER)
                .ownerId(USER_KEY_OWNER_ID_1)
                .statusUpdatedAt(TEST_TIME)
                .build();

        assertThat(converter.toDto(vo)).isEqualTo(expectedDto);
    }
}
