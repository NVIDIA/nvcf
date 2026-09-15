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

package com.nvidia.apikeys.facade;

import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_DTO_1_NO_SECRET;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.LIST_KEYS_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.LIST_KEYS_RESPONSE_1;
import static com.nvidia.apikeys.TestData.UPDATE_KEY_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.UPDATE_KEY_STATUS_REQUEST_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.AssertionsForClassTypes.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.converters.KeyResponseBuilder;
import com.nvidia.apikeys.services.KeysService;
import com.nvidia.apikeys.validators.ServiceAdminRequestValidator;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ServiceAdminFacadeTest {

    @Mock
    private ServiceAdminRequestValidator serviceAdminRequestValidatorMock;
    @Mock
    private KeysService keysServiceMock;
    @Mock
    private KeyResponseBuilder keyResponseBuilderMock;

    @InjectMocks
    private ServiceAdminFacade serviceAdminFacade;

    @Test
    void getUserKey() {
        when(serviceAdminRequestValidatorMock.validateGetKey(USER, USER_KEY_OWNER_ID_1, KEY_ID_1))
                .thenReturn(KEY_VO_1);
        when(keyResponseBuilderMock.toDto(KEY_VO_1))
                .thenReturn(KEY_DTO_1_NO_SECRET);

        assertThat(serviceAdminFacade.getKeyById(USER, USER_KEY_OWNER_ID_1, KEY_ID_1))
                .isEqualTo(KEY_DTO_1_NO_SECRET);
    }

    @Test
    void updateKeyStatus() {
        when(serviceAdminRequestValidatorMock.validateUpdateKeyStatus(
                USER, USER_KEY_OWNER_ID_1, KEY_ID_1, UPDATE_KEY_STATUS_REQUEST_1))
                .thenReturn(UPDATE_KEY_REQUEST_VO_1);
        when(keysServiceMock.updateKey(UPDATE_KEY_REQUEST_VO_1)).thenReturn(KEY_VO_1);
        when(keyResponseBuilderMock.toDto(KEY_VO_1)).thenReturn(KEY_DTO_1_NO_SECRET);

        assertThat(serviceAdminFacade.updateKeyStatus(
                USER, USER_KEY_OWNER_ID_1, KEY_ID_1, UPDATE_KEY_STATUS_REQUEST_1))
                .isEqualTo(KEY_DTO_1_NO_SECRET);
    }

    @Test
    void listApiKeys() {
        when(serviceAdminRequestValidatorMock.validateListKeysRequest(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(LIST_KEYS_REQUEST_VO_1);

        when(keysServiceMock.listKeys(LIST_KEYS_REQUEST_VO_1)).thenReturn(
                List.of(KEY_BY_OWNER_AND_SERVICE_VO_1));

        when(keyResponseBuilderMock.toListResponse(List.of(KEY_BY_OWNER_AND_SERVICE_VO_1)))
                .thenReturn(LIST_KEYS_RESPONSE_1);

        assertThat(serviceAdminFacade.listApiKeys(USER, USER_KEY_OWNER_ID_1))
                .isEqualTo(LIST_KEYS_RESPONSE_1);
    }
}
