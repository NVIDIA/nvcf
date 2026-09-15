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


import static com.nvidia.apikeys.TestData.CREATE_KEY_REQUEST_1;
import static com.nvidia.apikeys.TestData.CREATE_KEY_REQUEST_VO;
import static com.nvidia.apikeys.TestData.DELETE_KEY_BY_ID_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.GENERATED_KEY_VO_1;
import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_DTO_1;
import static com.nvidia.apikeys.TestData.KEY_DTO_1_SECRET;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1_AUTHZ_2;
import static com.nvidia.apikeys.TestData.LIST_KEYS_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.LIST_KEYS_RESPONSE_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.UPDATE_AUTHORIZATIONS_REQUEST_1;
import static com.nvidia.apikeys.TestData.UPDATE_KEY_REQUEST_VO_1_AUTHZ_2;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.caching.CallerServiceResolver;
import com.nvidia.apikeys.converters.KeyResponseBuilder;
import com.nvidia.apikeys.services.ActorResolver;
import com.nvidia.apikeys.services.ActorToValidatedKeyOwnerResolver;
import com.nvidia.apikeys.services.KeysService;
import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.apikeys.validators.KeysOperationByOwnerValidator;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class UserOwnedKeysFacadeTest {

    @Mock
    private ActorResolver actorJwtResolverMock;
    @Mock
    private KeysOperationByOwnerValidator validatorMock;
    @Mock
    private KeysService keysServiceMock;
    @Mock
    private KeyResponseBuilder keyResponseBuilderMock;
    @Mock
    private CallerServiceResolver callerServiceResolverMock;
    @Mock
    private ActorToValidatedKeyOwnerResolver actorToValidatedKeyOwnerResolverMock;
    @Mock
    private ValidatingKeyLoader validatingKeyLoaderMock;

    @InjectMocks
    private UserOwnedKeysFacade facade;

    @Test
    void createApiKey() {
        when(actorJwtResolverMock.getValidatedActorId()).thenReturn(USER_KEY_OWNER_ID_1);
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(actorToValidatedKeyOwnerResolverMock.getUserKeyOwnerVo(SERVICE_VO_1,
                                                                    USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(validatorMock.validateCreateKeyRequest(
                SERVICE_VO_1, KEY_OWNER_VO_1, CREATE_KEY_REQUEST_1))
                .thenReturn(CREATE_KEY_REQUEST_VO);
        when(keysServiceMock.createKey(CREATE_KEY_REQUEST_VO)).thenReturn(CREATE_KEY_REQUEST_VO);
        when(keyResponseBuilderMock.toDto(KEY_VO_1, GENERATED_KEY_VO_1)).thenReturn(
                KEY_DTO_1_SECRET);

        assertThat(facade.createApiKey(CREATE_KEY_REQUEST_1)).isEqualTo(KEY_DTO_1_SECRET);
    }

    @Test
    void listApiKeys() {
        when(actorJwtResolverMock.getValidatedActorId()).thenReturn(USER_KEY_OWNER_ID_1);
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(actorToValidatedKeyOwnerResolverMock.getUserKeyOwnerVo(SERVICE_VO_1,
                                                                    USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(validatorMock.validateListKeysRequest(SERVICE_VO_1, KEY_OWNER_VO_1))
                .thenReturn(LIST_KEYS_REQUEST_VO_1);
        var records = List.of(KEY_BY_OWNER_AND_SERVICE_VO_1);
        when(keysServiceMock.listKeys(LIST_KEYS_REQUEST_VO_1)).thenReturn(records);
        when(keyResponseBuilderMock.toListResponse(records))
                .thenReturn(LIST_KEYS_RESPONSE_1);

        assertThat(facade.listApiKeys()).isEqualTo(LIST_KEYS_RESPONSE_1);
    }

    @Test
    void getKeyById() {
        when(actorJwtResolverMock.getValidatedActorId()).thenReturn(USER_KEY_OWNER_ID_1);
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(actorToValidatedKeyOwnerResolverMock.getUserKeyOwnerVo(SERVICE_VO_1,
                                                                    USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(validatingKeyLoaderMock.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(KEY_VO_1);
        when(keyResponseBuilderMock.toDto(KEY_VO_1)).thenReturn(KEY_DTO_1);

        assertThat(facade.getKeyById(KEY_ID_1)).isEqualTo(KEY_DTO_1);
    }

    @Test
    void deleteKeyById() {
        when(actorJwtResolverMock.getValidatedActorId()).thenReturn(USER_KEY_OWNER_ID_1);
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(actorToValidatedKeyOwnerResolverMock.getUserKeyOwnerVo(SERVICE_VO_1,
                                                                    USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(validatorMock.validateDeleteKeyRequest(SERVICE_VO_1, KEY_OWNER_VO_1, KEY_ID_1))
                .thenReturn(DELETE_KEY_BY_ID_REQUEST_VO_1);

        facade.deleteKeyById(KEY_ID_1);

        verify(keysServiceMock).deleteKeyById(DELETE_KEY_BY_ID_REQUEST_VO_1);
    }

    @Test
    void updateKeyAuthorizations() {
        when(actorJwtResolverMock.getValidatedActorId()).thenReturn(USER_KEY_OWNER_ID_1);
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(actorToValidatedKeyOwnerResolverMock.getUserKeyOwnerVo(
                SERVICE_VO_1, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(validatorMock.validateUpdateKeyRequest(
                SERVICE_VO_1, KEY_OWNER_VO_1, KEY_ID_1, UPDATE_AUTHORIZATIONS_REQUEST_1))
                .thenReturn(UPDATE_KEY_REQUEST_VO_1_AUTHZ_2);
        when(keysServiceMock.updateKey(UPDATE_KEY_REQUEST_VO_1_AUTHZ_2))
                .thenReturn(KEY_VO_1_AUTHZ_2);
        when(keyResponseBuilderMock.toDto(KEY_VO_1_AUTHZ_2))
                .thenReturn(KEY_DTO_1);

        assertThat(facade.updateKeyAuthorizations(KEY_ID_1, UPDATE_AUTHORIZATIONS_REQUEST_1))
                .isEqualTo(KEY_DTO_1);
    }
}
