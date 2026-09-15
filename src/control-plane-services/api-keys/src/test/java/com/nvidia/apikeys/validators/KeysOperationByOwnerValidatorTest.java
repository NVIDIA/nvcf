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

import static com.nvidia.apikeys.TestData.AUTHORIZATION_JSON_NODES_1;
import static com.nvidia.apikeys.TestData.AUTHORIZATION_JSON_NODES_2;
import static com.nvidia.apikeys.TestData.CREATE_KEY_REQUEST_1;
import static com.nvidia.apikeys.TestData.CREATE_KEY_REQUEST_VO;
import static com.nvidia.apikeys.TestData.DELETE_KEY_BY_ID_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.GENERATED_KEY_VO_1;
import static com.nvidia.apikeys.TestData.KEY_AUTHZ_1;
import static com.nvidia.apikeys.TestData.KEY_AUTHZ_2;
import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_DESCRIPTION_1;
import static com.nvidia.apikeys.TestData.KEY_EXPIRES_AT_1;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.UPDATE_AUTHORIZATIONS_REQUEST_1;
import static com.nvidia.apikeys.TestData.UPDATE_KEY_REQUEST_VO_1_AUTHZ_2;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.converters.ValidatingAuthorizationsConverter;
import com.nvidia.apikeys.dto.keys.CreateKeyRequest;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.services.CredentialService;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.apikeys.vo.CreateKeyRequestVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ListKeysRequestVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeysOperationByOwnerValidatorTest {

    @Mock
    private CredentialService credentialServiceMock;
    @Mock
    private KeysDao keysDaoMock;
    @Mock
    private KeyRequestValidator keyRequestValidatorMock;
    @Mock
    private ValidatingAuthorizationsConverter authzConverterMock;
    @Mock
    private MillisecondPrecisionClock clockMock;
    @Mock
    private NakProperties nakPropertiesMock;
    @Mock
    private ValidatingKeyLoader validatingKeyLoaderMock;

    @InjectMocks
    private KeysOperationByOwnerValidator keysOperationByOwnerValidator;

    @Test
    void validateCreateKeyRequest_buildsValidRequest() {
        when(clockMock.instant()).thenReturn(TEST_TIME);
        when(authzConverterMock.readValidAuthorizations(SERVICE_VO_1, AUTHORIZATION_JSON_NODES_1))
                .thenReturn(KEY_AUTHZ_1);
        when(keysDaoMock.list(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(List.of());
        when(credentialServiceMock.generateApiKey()).thenReturn(GENERATED_KEY_VO_1);
        when(nakPropertiesMock.getKeepAfterExpiredDuration()).thenReturn(Duration.ofDays(30));
        when(keyRequestValidatorMock.getValidAudienceServiceIds(SERVICE_VO_1, Set.of(SERVICE_ID_1)))
                .thenReturn(Set.of(SERVICE_ID_1));

        assertThat(keysOperationByOwnerValidator.validateCreateKeyRequest(
                SERVICE_VO_1, KEY_OWNER_VO_1, CREATE_KEY_REQUEST_1))
                .isEqualTo(CREATE_KEY_REQUEST_VO);

        verify(keyRequestValidatorMock).assertDescriptionValid(KEY_DESCRIPTION_1);
        verify(keyRequestValidatorMock).assertExpirationDateValid(SERVICE_VO_1, KEY_EXPIRES_AT_1);
    }

    @Test
    void validateCreateKeyRequest_buildsValidRequestWithNoCustomAudiences() {
        when(clockMock.instant()).thenReturn(TEST_TIME);
        when(authzConverterMock.readValidAuthorizations(SERVICE_VO_1, AUTHORIZATION_JSON_NODES_1))
                .thenReturn(KEY_AUTHZ_1);
        when(keysDaoMock.list(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(List.of());
        when(credentialServiceMock.generateApiKey()).thenReturn(GENERATED_KEY_VO_1);
        when(nakPropertiesMock.getKeepAfterExpiredDuration()).thenReturn(Duration.ofDays(30));
        when(keyRequestValidatorMock.getValidAudienceServiceIds(SERVICE_VO_1, null))
                .thenReturn(Set.of(SERVICE_ID_1));

        CreateKeyRequest createKeyRequest = CREATE_KEY_REQUEST_1.toBuilder()
                .audienceServiceIds(null)
                .build();
        assertThat(keysOperationByOwnerValidator.validateCreateKeyRequest(
                SERVICE_VO_1, KEY_OWNER_VO_1, createKeyRequest))
                .isEqualTo(CREATE_KEY_REQUEST_VO);

        verify(keyRequestValidatorMock).assertDescriptionValid(KEY_DESCRIPTION_1);
        verify(keyRequestValidatorMock).assertExpirationDateValid(SERVICE_VO_1, KEY_EXPIRES_AT_1);
    }

    @Test
    void validateCreateKeyRequest_buildsValidRequest_keep5days() {
        when(clockMock.instant()).thenReturn(TEST_TIME);
        when(keysDaoMock.list(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(List.of());
        when(credentialServiceMock.generateApiKey()).thenReturn(GENERATED_KEY_VO_1);
        when(nakPropertiesMock.getKeepAfterExpiredDuration()).thenReturn(Duration.ofDays(5));
        when(authzConverterMock.readValidAuthorizations(SERVICE_VO_1, AUTHORIZATION_JSON_NODES_1))
                .thenReturn(KEY_AUTHZ_1);
        when(keyRequestValidatorMock.getValidAudienceServiceIds(SERVICE_VO_1, Set.of(SERVICE_ID_1)))
                .thenReturn(Set.of(SERVICE_ID_1));

        Instant deleteKeyAt = KEY_EXPIRES_AT_1.plus(5, ChronoUnit.DAYS);
        KeyVo keyVo = KEY_VO_1.toBuilder()
                .deletesAt(deleteKeyAt)
                .build();

        CreateKeyRequestVo createKeyRequestVo = CREATE_KEY_REQUEST_VO.toBuilder()
                .key(keyVo)
                .build();

        assertThat(keysOperationByOwnerValidator.validateCreateKeyRequest(
                SERVICE_VO_1, KEY_OWNER_VO_1, CREATE_KEY_REQUEST_1))
                .isEqualTo(createKeyRequestVo);

        verify(keyRequestValidatorMock).assertExpirationDateValid(SERVICE_VO_1, KEY_EXPIRES_AT_1);
        verify(keyRequestValidatorMock).assertDescriptionValid(KEY_DESCRIPTION_1);
    }

    @Test
    void validateCreateKeyRequest_throwsIfReachedMaxKeys() {
        ServiceVo serviceVo = SERVICE_VO_1.toBuilder()
                .maxApiKeysPerUser(0)
                .build();
        when(keysDaoMock.list(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(List.of());

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> keysOperationByOwnerValidator.validateCreateKeyRequest(
                        serviceVo, KEY_OWNER_VO_1, CREATE_KEY_REQUEST_1),
                "Actor reached maximum allowed number of keys in service");

        verify(keyRequestValidatorMock).assertDescriptionValid(KEY_DESCRIPTION_1);
        verify(keyRequestValidatorMock).assertExpirationDateValid(serviceVo, KEY_EXPIRES_AT_1);
    }

    @Test
    void validateListKeysRequest() {
        assertThat(keysOperationByOwnerValidator
                           .validateListKeysRequest(SERVICE_VO_1, KEY_OWNER_VO_1))
                .isEqualTo(ListKeysRequestVo.builder()
                                   .service(SERVICE_VO_1)
                                   .keyOwner(KEY_OWNER_VO_1)
                                   .build());
    }

    @Test
    void validateDeleteKeyRequest() {
        when(validatingKeyLoaderMock.loadKeyByOwnerAndServiceVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(KEY_BY_OWNER_AND_SERVICE_VO_1);

        assertThat(keysOperationByOwnerValidator.validateDeleteKeyRequest(
                SERVICE_VO_1, KEY_OWNER_VO_1, KEY_ID_1))
                .isEqualTo(DELETE_KEY_BY_ID_REQUEST_VO_1);
    }

    @Test
    void validateUpdateKeyRequest() {
        when(authzConverterMock.readValidAuthorizations(SERVICE_VO_1, AUTHORIZATION_JSON_NODES_2))
                .thenReturn(KEY_AUTHZ_2);
        when(validatingKeyLoaderMock.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(KEY_VO_1);

        assertThat(keysOperationByOwnerValidator.validateUpdateKeyRequest(
                SERVICE_VO_1, KEY_OWNER_VO_1, KEY_ID_1, UPDATE_AUTHORIZATIONS_REQUEST_1))
                .isEqualTo(UPDATE_KEY_REQUEST_VO_1_AUTHZ_2);

        verify(keyRequestValidatorMock).assertKeyActive(KEY_VO_1);
        verifyNoMoreInteractions(keyRequestValidatorMock);
    }
}
