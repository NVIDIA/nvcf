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

import static com.nvidia.apikeys.TestData.API_KEY_SUFFIX_1;
import static com.nvidia.apikeys.TestData.GENERATED_KEY_VO_1;
import static com.nvidia.apikeys.TestData.KEY_AUTHZ_1;
import static com.nvidia.apikeys.TestData.KEY_DESCRIPTION_1;
import static com.nvidia.apikeys.TestData.KEY_EXPIRES_AT_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.converters.ValidatingAuthorizationsConverter;
import com.nvidia.apikeys.dto.keysV2.CreateKeyRequest;
import com.nvidia.apikeys.services.CredentialService;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.services.ServicesService;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.Duration;
import java.time.ZoneId;
import java.util.Optional;
import java.util.Set;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import tools.jackson.databind.json.JsonMapper;

@ExtendWith(MockitoExtension.class)
class KeyManagementRequestValidatorTest {

    private static final Duration KEEP_AFTER_EXPIRED = Duration.ofDays(5);

    @Mock
    private ServicesService servicesService;
    @Mock
    private CredentialService credentialService;

    private KeyManagementRequestValidator validator;

    @BeforeEach
    void setUp() {
        var clock = TestClock.fixed(TEST_TIME, ZoneId.systemDefault());
        validator = new KeyManagementRequestValidator(
                NakProperties.builder().keepAfterExpiredDuration(KEEP_AFTER_EXPIRED).build(),
                servicesService,
                credentialService,
                new KeyRequestValidator(clock),
                new ValidatingAuthorizationsConverter(),
                new MillisecondPrecisionClock(clock));
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {" ", "*"})
    void assertRouteIdRejectsMissingAndReservedValues(String value) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> validator.assertRouteId("userId", value),
                "userId is invalid");
    }

    @Test
    void assertRouteIdAcceptsOrdinaryValue() {
        validator.assertRouteId("userId", USER_KEY_OWNER_ID_1);
    }

    @Test
    void requireServiceReturnsRegisteredService() {
        when(servicesService.getServiceById(SERVICE_ID_1)).thenReturn(Optional.of(SERVICE_VO_1));

        assertThat(validator.requireService(SERVICE_ID_1)).isEqualTo(SERVICE_VO_1);
    }

    @Test
    void requireServiceRejectsUnregisteredService() {
        when(servicesService.getServiceById("unknown")).thenReturn(Optional.empty());

        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> validator.requireService("unknown"),
                "Unknown issuer service: 'unknown'");
    }

    @Test
    void requireServiceRejectsReservedServiceWithoutLookup() {
        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> validator.requireService("*"),
                "serviceId is invalid");
        verifyNoInteractions(servicesService);
    }

    @Test
    void validateCreateKeyRequestBuildsActiveUserKeyFromRoute() throws Exception {
        when(credentialService.generateApiKey()).thenReturn(GENERATED_KEY_VO_1);

        var create = validator.validateCreateKeyRequest(
                SERVICE_VO_1, NCA_ID_1, USER_KEY_OWNER_ID_1, createRequest());

        assertThat(create.generatedKey()).isEqualTo(GENERATED_KEY_VO_1);
        assertThat(create.service()).isEqualTo(SERVICE_VO_1);
        assertThat(create.key()).satisfies(key -> {
            assertThat(key.getKeyStatus()).isEqualTo(KeyStatus.ACTIVE);
            assertThat(key.getNcaId()).isEqualTo(NCA_ID_1);
            assertThat(key.getOwnerType()).isEqualTo(KeyOwnerType.USER);
            assertThat(key.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
            assertThat(key.getIssuerServiceId()).isEqualTo(SERVICE_ID_1);
            assertThat(key.getAudienceServiceIds()).containsExactly(SERVICE_ID_1);
            assertThat(key.getKeyId()).isEqualTo(GENERATED_KEY_VO_1.getKeyId());
            assertThat(key.getKeyHash()).isEqualTo(GENERATED_KEY_VO_1.getKeyHash());
            assertThat(key.getApiKeySuffix()).isEqualTo(API_KEY_SUFFIX_1);
            assertThat(key.getCreatedAt()).isEqualTo(TEST_TIME);
            assertThat(key.getExpiresAt()).isEqualTo(KEY_EXPIRES_AT_1);
            assertThat(key.getDeletesAt()).isEqualTo(KEY_EXPIRES_AT_1.plus(KEEP_AFTER_EXPIRED));
            assertThat(key.getAuthorizations()).isEqualTo(KEY_AUTHZ_1);
            assertThat(key.getDescription()).isEqualTo(KEY_DESCRIPTION_1);
        });
    }

    @Test
    void validateCreateKeyRequestRejectsInvalidDescriptionBeforeGeneratingKey() throws Exception {
        var request = createRequest().toBuilder().description("!invalid").build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validateCreateKeyRequest(
                        SERVICE_VO_1, NCA_ID_1, USER_KEY_OWNER_ID_1, request),
                "description is invalid");
        verifyNoInteractions(credentialService);
    }

    @Test
    void validateCreateKeyRequestRejectsExpirationBeyondServiceLimit() throws Exception {
        var request = createRequest().toBuilder()
                .expiresAt(TEST_TIME.plus(Duration.ofDays(2)))
                .build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validateCreateKeyRequest(
                        SERVICE_VO_1, NCA_ID_1, USER_KEY_OWNER_ID_1, request),
                "expires_at must be in the future and not more than 1 days from now");
    }

    @Test
    void validateCreateKeyRequestRejectsUnregisteredAudience() throws Exception {
        var request = createRequest().toBuilder()
                .audienceServiceIds(Set.of("other-service"))
                .build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validateCreateKeyRequest(
                        SERVICE_VO_1, NCA_ID_1, USER_KEY_OWNER_ID_1, request),
                "service '%s' cannot use audiences: other-service".formatted(SERVICE_ID_1));
    }

    @Test
    void validateCreateKeyRequestRejectsMissingAuthorizations() throws Exception {
        var request = createRequest().toBuilder().authorizations(null).build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validateCreateKeyRequest(
                        SERVICE_VO_1, NCA_ID_1, USER_KEY_OWNER_ID_1, request),
                "authorizations can not be null");
    }

    @Test
    void validateAuthorizationsDelegatesToConverter() throws Exception {
        var node = new JsonMapper().readTree(KEY_AUTHZ_1);

        assertThat(validator.validateAuthorizations(SERVICE_VO_1, node)).isEqualTo(KEY_AUTHZ_1);
    }

    @Test
    void resolveLimitUsesDefaultWhenAbsent() {
        assertThat(validator.resolveLimit(null, false))
                .isEqualTo(KeyManagementRequestValidator.DEFAULT_LIMIT);
    }

    @Test
    void resolveLimitAcceptsMaximum() {
        assertThat(validator.resolveLimit(KeyManagementRequestValidator.MAX_LIMIT, false))
                .isEqualTo(KeyManagementRequestValidator.MAX_LIMIT);
    }

    @ParameterizedTest
    @ValueSource(ints = {Integer.MIN_VALUE, -1, 0, KeyManagementRequestValidator.MAX_LIMIT + 1})
    void resolveLimitRejectsOutOfRange(int limit) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> validator.resolveLimit(limit, false),
                "limit must be between 1 and %d".formatted(KeyManagementRequestValidator.MAX_LIMIT));
    }

    @Test
    void resolveLimitAppliesLowerCapToUnfilteredList() {
        var tooLarge = KeyManagementRequestValidator.MAX_UNFILTERED_LIMIT + 1;

        assertThat(validator.resolveLimit(KeyManagementRequestValidator.MAX_UNFILTERED_LIMIT, true))
                .isEqualTo(KeyManagementRequestValidator.MAX_UNFILTERED_LIMIT);
        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> validator.resolveLimit(tooLarge, true),
                "limit must be between 1 and %d"
                        .formatted(KeyManagementRequestValidator.MAX_UNFILTERED_LIMIT));
    }

    private static CreateKeyRequest createRequest() throws Exception {
        return CreateKeyRequest.builder()
                .description(KEY_DESCRIPTION_1)
                .expiresAt(KEY_EXPIRES_AT_1)
                .authorizations(new JsonMapper().readTree(KEY_AUTHZ_1))
                .build();
    }
}
