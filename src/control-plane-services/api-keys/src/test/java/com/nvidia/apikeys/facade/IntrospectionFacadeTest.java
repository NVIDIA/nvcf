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
import static com.nvidia.apikeys.TestData.CACHED_INTROSPECTION_RESPONSE_1;
import static com.nvidia.apikeys.TestData.INTROSPECTION_REQUEST_1;
import static com.nvidia.apikeys.TestData.INTROSPECTION_RESPONSE_1;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.services.ActionRecorder.Action.INTROSPECT;
import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.API_KEY;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.caching.IntrospectionLocalCache;
import com.nvidia.apikeys.converters.KeyResponseBuilder;
import com.nvidia.apikeys.services.ActionRecorder;
import com.nvidia.apikeys.utils.TracingUtils;
import com.nvidia.apikeys.validators.ApiKeyParser;
import com.nvidia.apikeys.validators.IntrospectionValidator;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Optional;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class IntrospectionFacadeTest {

    @Mock
    private ApiKeyParser apiKeyParserMock;
    @Mock
    private IntrospectionValidator validatorMock;
    @Mock
    private KeyResponseBuilder responseBuilderMock;
    @Mock
    private IntrospectionLocalCache introspectionLocalCacheMock;
    @Mock
    private ActionRecorder actionRecorderMock;
    @Mock
    private TracingUtils tracingUtilsMock;

    @InjectMocks
    public IntrospectionFacade facade;

    @Test
    void introspect_shouldCacheValidResponse() {
        when(apiKeyParserMock.rawApiKeyToHash(API_KEY_1)).thenReturn(API_KEY_HASH_1);
        when(validatorMock.validate(INTROSPECTION_REQUEST_1, API_KEY_HASH_1)).thenReturn(KEY_VO_1);
        when(responseBuilderMock.toIntrospectionResponse(KEY_VO_1))
                .thenReturn(INTROSPECTION_RESPONSE_1);

        assertThat(facade.introspect(INTROSPECTION_REQUEST_1))
                .isEqualTo(INTROSPECTION_RESPONSE_1);

        verify(introspectionLocalCacheMock)
                .cacheValidResponse(INTROSPECTION_REQUEST_1, CACHED_INTROSPECTION_RESPONSE_1);
        verify(actionRecorderMock)
                .record(API_KEY, KEY_ID_1, "USER", USER_KEY_OWNER_ID_1,
                        INTROSPECT, SERVICE_ID_1, Set.of(SERVICE_ID_1));
    }

    @Test
    void introspect_shouldUseCachedResponse() {
        when(introspectionLocalCacheMock.getCachedResponse(INTROSPECTION_REQUEST_1))
                .thenReturn(Optional.of(CACHED_INTROSPECTION_RESPONSE_1));

        assertThat(facade.introspect(INTROSPECTION_REQUEST_1))
                .isEqualTo(INTROSPECTION_RESPONSE_1);

        verifyNoInteractions(validatorMock, responseBuilderMock);
        verify(actionRecorderMock)
                .record(API_KEY, KEY_ID_1, "USER", USER_KEY_OWNER_ID_1,
                        INTROSPECT, SERVICE_ID_1, Set.of(SERVICE_ID_1));
    }

    @Test
    void introspect_shouldCacheErrorResponse() {
        BadRequestException exception = new BadRequestException("error");
        when(apiKeyParserMock.rawApiKeyToHash(API_KEY_1)).thenThrow(exception);

        assertThat(assertThrows(
                BadRequestException.class, () -> facade.introspect(INTROSPECTION_REQUEST_1)))
                .isEqualTo(exception);

        verifyNoInteractions(validatorMock, responseBuilderMock);
    }

    @Test
    void introspect_shouldUseCachedErrorResponse() {
        BadRequestException exception = new BadRequestException("error");

        when(introspectionLocalCacheMock.getKnownInvalidResponse(INTROSPECTION_REQUEST_1))
                .thenReturn(Optional.of(exception));

        assertThat(assertThrows(
                BadRequestException.class, () -> facade.introspect(INTROSPECTION_REQUEST_1)))
                .isEqualTo(exception);

        verifyNoInteractions(validatorMock, responseBuilderMock);
    }

}
