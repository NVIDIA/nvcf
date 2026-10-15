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

import static com.nvidia.apikeys.TestData.API_KEY_1;
import static com.nvidia.apikeys.TestData.API_KEY_HASH_1;
import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_DTO_1_LOOKUP;
import static com.nvidia.apikeys.TestData.KEY_LOOKUP_REQUEST_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_DTO_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.LIST_KEYS_RESPONSE_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.UPDATE_KEY_OWNER_STATUS_REQUEST_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.converters.KeyOwnerConverter;
import com.nvidia.apikeys.converters.KeyResponseBuilder;
import com.nvidia.apikeys.dto.keys.UpdateKeyOwnerStatusRequest;
import com.nvidia.apikeys.services.KeyOwnerService;
import com.nvidia.apikeys.services.KeysService;
import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.apikeys.validators.ApiKeyParser;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.Clock;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class AdminFacadeTest {

    @Mock
    private ApiKeyParser apiKeyParserMock;

    @Mock
    private KeyResponseBuilder responseBuilderMock;

    @Mock
    private KeyOwnerService keyOwnerServiceMock;

    @Mock
    private KeysService keysServiceMock;

    @Mock
    private ValidatingKeyLoader keyLoaderMock;

    @Mock
    private KeyOwnerConverter keyOwnerConverterMock;

    @Mock
    private Clock clockMock;

    @InjectMocks
    private AdminFacade facade;

    @Test
    void lookup() {
        when(apiKeyParserMock.rawApiKeyToHash(API_KEY_1)).thenReturn(API_KEY_HASH_1);
        when(keyLoaderMock.loadKeyByHash(API_KEY_HASH_1)).thenReturn(KEY_VO_1);
        when(responseBuilderMock.toLookupDto(KEY_VO_1)).thenReturn(KEY_DTO_1_LOOKUP);

        assertThat(facade.lookup(KEY_LOOKUP_REQUEST_1)).isEqualTo(KEY_DTO_1_LOOKUP);
    }

    @Test
    void listKeysInAllServices() {
        when(keyOwnerServiceMock.loadExistingKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(keysServiceMock.listKeys(KEY_OWNER_VO_1))
                .thenReturn(List.of(KEY_BY_OWNER_AND_SERVICE_VO_1));
        when(responseBuilderMock.toListResponse(List.of(KEY_BY_OWNER_AND_SERVICE_VO_1)))
                .thenReturn(LIST_KEYS_RESPONSE_1);

        assertThat(facade.listKeysInAllServices(USER, USER_KEY_OWNER_ID_1))
                .isEqualTo(LIST_KEYS_RESPONSE_1);
    }

    @Test
    void deleteUserKeys() {
        when(keyOwnerServiceMock.loadExistingKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);

        facade.deleteUserKeys(USER, USER_KEY_OWNER_ID_1);

        verify(keysServiceMock).deleteKeys(KEY_OWNER_VO_1);
    }

    @Test
    void getKeyOwner() {
        when(keyOwnerServiceMock.loadExistingKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(keyOwnerConverterMock.toDto(KEY_OWNER_VO_1)).thenReturn(KEY_OWNER_DTO_1);

        assertThat(facade.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).isEqualTo(KEY_OWNER_DTO_1);
    }

    @Test
    void updateKeyOwnerStatus() {
        when(keyOwnerServiceMock.loadExistingKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(clockMock.instant()).thenReturn(TEST_TIME);
        when(keyOwnerServiceMock.updateKeyOwner(KEY_OWNER_VO_1,
                                                UPDATE_KEY_OWNER_STATUS_REQUEST_1.getStatus(),
                                                TEST_TIME))
                .thenReturn(KEY_OWNER_VO_1);
        when(keyOwnerConverterMock.toDto(KEY_OWNER_VO_1)).thenReturn(KEY_OWNER_DTO_1);

        assertThat(facade.updateKeyOwnerStatus(USER, USER_KEY_OWNER_ID_1,
                                               UPDATE_KEY_OWNER_STATUS_REQUEST_1))
                .isEqualTo(KEY_OWNER_DTO_1);
    }

    @Test
    void updateKeyOwnerStatus_throwsIfRequestNull() {
        assertThat(assertThrows(
                BadRequestException.class,
                () -> facade.updateKeyOwnerStatus(USER, USER_KEY_OWNER_ID_1, null)))
                .hasMessageContaining("status is required parameter");
    }

    @Test
    void updateKeyOwnerStatus_throwsIfStatusNull() {
        UpdateKeyOwnerStatusRequest request = new UpdateKeyOwnerStatusRequest(null);
        assertThat(assertThrows(
                BadRequestException.class,
                () -> facade.updateKeyOwnerStatus(USER, USER_KEY_OWNER_ID_1, request)))
                .hasMessageContaining("status is required parameter");
    }
}
