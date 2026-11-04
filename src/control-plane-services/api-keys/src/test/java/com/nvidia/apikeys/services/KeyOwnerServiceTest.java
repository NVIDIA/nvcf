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

import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.KEY_OWNER;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.converters.KeyOwnerVoBuilder;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.services.ActionRecorder.Action;
import com.nvidia.apikeys.utils.TracingUtils;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.boot.exceptions.NotFoundException;
import java.util.Optional;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeyOwnerServiceTest {

    @Mock
    private KeyOwnerVoBuilder keyOwnerVoBuilderMock;

    @Mock
    private KeysDao daoMock;

    @Mock
    private ActionRecorder actionRecorderMock;

    @Mock
    private TracingUtils tracingUtilsMock;

    @InjectMocks
    private KeyOwnerService keyOwnerService;

    @Test
    void loadKeyOwnerByTypeAndId_returnsOwnerIfPresent() {
        when(daoMock.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(
                Optional.of(KEY_OWNER_VO_1));

        assertThat(keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .isEqualTo(KEY_OWNER_VO_1);
    }

    @Test
    void loadKeyOwnerByTypeAndId_makesNewEntityIfNotFound() {
        when(daoMock.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.empty());
        when(keyOwnerVoBuilderMock.getNewKeyOwnerVo(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);

        assertThat(keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(
                USER, USER_KEY_OWNER_ID_1)).isEqualTo(KEY_OWNER_VO_1);
    }

    @Test
    void loadExistingKeyOwner_returnsIfOwnerExists() {
        when(daoMock.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(
                Optional.of(KEY_OWNER_VO_1));

        assertThat(keyOwnerService.loadExistingKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .isEqualTo(KEY_OWNER_VO_1);
    }

    @Test
    void loadExistingKeyOwner_throwsIfUserDoesNotExist() {
        when(daoMock.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.empty());

        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> keyOwnerService.loadExistingKeyOwner(USER, USER_KEY_OWNER_ID_1),
                "Key owner not found");
    }

    @ParameterizedTest
    @ValueSource(strings = {"SUSPENDED", "ACTIVE"})
    void updateKeyOwner(String status) {
        KeyOwnerVo updated = KEY_OWNER_VO_1.toBuilder()
                .ownerStatus(KeyOwnerStatus.valueOf(status))
                .ownerStatusUpdatedAt(TEST_TIME)
                .build();

        when(daoMock.save(updated)).thenReturn(updated);

        assertThat(keyOwnerService.updateKeyOwner(
                KEY_OWNER_VO_1, KeyOwnerStatus.valueOf(status), TEST_TIME))
                .isEqualTo(updated);

        verify(actionRecorderMock).record(KEY_OWNER, "N/A", "USER", USER_KEY_OWNER_ID_1,
                                          Action.valueOf(status), "N/A", Set.of());
    }
}
