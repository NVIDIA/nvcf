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

import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.boot.exceptions.NotFoundException;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ValidatingKeyLoaderTest {

    @Mock
    private KeysDao keysDaoMock;

    @Mock
    private KeyByOwnerAndServiceVo keyByOwnerAndServiceVoMock;

    @InjectMocks
    private ValidatingKeyLoader keyLoader;

    @Test
    void loadKeyByOwnerAndServiceVo_shouldReturnValidUserKey() {
        when(keysDaoMock.getKeyByOwnerAndServiceAndId(
                USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(keyByOwnerAndServiceVoMock));

        assertThat(keyLoader.loadKeyByOwnerAndServiceVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1))
                .isEqualTo(keyByOwnerAndServiceVoMock);
    }

    @Test
    void loadKeyByOwnerAndServiceVo_throwsIfKeyNotFound() {
        when(keysDaoMock.getKeyByOwnerAndServiceAndId(
                USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.empty());

        assertThat(assertThrows(
                NotFoundException.class,
                () -> keyLoader.loadKeyByOwnerAndServiceVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1)))
                .hasMessageContaining("Key not found");
    }
}
