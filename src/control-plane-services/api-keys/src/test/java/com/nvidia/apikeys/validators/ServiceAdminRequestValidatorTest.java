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
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.LIST_KEYS_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.UPDATE_KEY_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static com.nvidia.apikeys.vo.KeyStatus.SUSPENDED;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.caching.CallerServiceResolver;
import com.nvidia.apikeys.dto.keys.UpdateKeyStatusRequest;
import com.nvidia.apikeys.services.KeyOwnerService;
import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.apikeys.vo.DeleteKeyRequestVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.SuspendKeysRequestVo;
import com.nvidia.apikeys.vo.UpdateKeyRequestVo;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ServiceAdminRequestValidatorTest {

    @Mock
    private CallerServiceResolver callerServiceResolverMock;

    @Mock
    private KeyStatusTransitionValidator keyStatusTransitionValidatorMock;

    @Mock
    private ValidatingKeyLoader validatingKeyLoaderMock;

    @Mock
    private KeyOwnerService keyOwnerServiceMock;

    @InjectMocks
    private ServiceAdminRequestValidator validator;

    @Test
    void validateGetKey() {
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(keyOwnerServiceMock.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(validatingKeyLoaderMock.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(KEY_VO_1);

        assertThat(validator.validateGetKey(USER, USER_KEY_OWNER_ID_1, KEY_ID_1))
                .isEqualTo(KEY_VO_1);
    }

    @Test
    void validateUpdateKeyStatus() {
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(keyOwnerServiceMock.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(validatingKeyLoaderMock.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(KEY_VO_1);

        UpdateKeyStatusRequest request = new UpdateKeyStatusRequest(SUSPENDED);

        KeyVo keyVo = KEY_VO_1.toBuilder()
                .keyStatus(SUSPENDED)
                .build();

        UpdateKeyRequestVo expectedUpdateRequest = UPDATE_KEY_REQUEST_VO_1.toBuilder()
                .key(keyVo)
                .build();

        assertThat(validator.validateUpdateKeyStatus(USER, USER_KEY_OWNER_ID_1, KEY_ID_1, request))
                .isEqualTo(expectedUpdateRequest);

        verify(keyStatusTransitionValidatorMock)
                .assertStatusTransitionValid(KeyStatus.ACTIVE, SUSPENDED);
    }

    @Test
    void validateListKeysRequest() {
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(keyOwnerServiceMock.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);

        assertThat(validator.validateListKeysRequest(USER, USER_KEY_OWNER_ID_1))
                .isEqualTo(LIST_KEYS_REQUEST_VO_1);
    }

    @Test
    void validateSuspendKeysRequest() {
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(keyOwnerServiceMock.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);

        SuspendKeysRequestVo expectedRequest = SuspendKeysRequestVo.builder()
                .keyOwner(KEY_OWNER_VO_1)
                .service(SERVICE_VO_1)
                .build();

        assertThat(validator.validateSuspendKeysRequest(USER, USER_KEY_OWNER_ID_1))
                .isEqualTo(expectedRequest);
    }

    @Test
    void validateDeleteKeyRequest() {
        when(callerServiceResolverMock.getCallerService()).thenReturn(SERVICE_VO_1);
        when(keyOwnerServiceMock.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(validatingKeyLoaderMock.loadKeyByOwnerAndServiceVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_VO_1);

        DeleteKeyRequestVo expectedRequest = DeleteKeyRequestVo.builder()
                .keyOwner(KEY_OWNER_VO_1)
                .service(SERVICE_VO_1)
                .key(KEY_BY_OWNER_AND_SERVICE_VO_1)
                .build();

        assertThat(validator.validateDeleteKeyRequest(USER, USER_KEY_OWNER_ID_1, KEY_ID_1))
                .isEqualTo(expectedRequest);
    }
}
