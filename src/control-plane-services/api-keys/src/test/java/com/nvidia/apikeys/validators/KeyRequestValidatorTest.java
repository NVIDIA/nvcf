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

import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.Instant;
import java.time.ZoneId;
import java.time.temporal.ChronoUnit;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.EnumSource.Mode;
import org.junit.jupiter.params.provider.NullSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeyRequestValidatorTest {

    private final KeyRequestValidator validator = new KeyRequestValidator(
            TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

    @ParameterizedTest
    @NullSource
    @ValueSource(strings = {
            "!invalid char",
            "", // empty
            "0123456789012345678901234567890123456789012345678901234567890123456789", // too long
    })
    void getValidatedRequest_parseThrowsIfDescriptionInvalid(String description) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertDescriptionValid(description), "description is invalid");
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "2023-10-02T08:25:24.00Z", // day before test time
            "2023-10-03T08:25:23.00Z", // second before test time
            "2023-10-04T08:25:25.00Z", // day and one second after test time
            "2023-11-03T08:25:24.00Z"  // month after test time
    })
    void getValidatedRequest_parseThrowsIfExpirationInvalid(String expirationDateString) {
        Instant expiresAt = Instant.parse(expirationDateString);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertExpirationDateValid(SERVICE_VO_1, expiresAt),
                "expires_at must be in the future and not more than 1 days from now");
    }

    @ParameterizedTest
    @ValueSource(ints = {1, 10, 100, 399, 3999})
    void getValidatedRequest_passesForExtremeValues(int expiresInDaysFromNow) {
        Instant expiresAt = TEST_TIME.plus(expiresInDaysFromNow, ChronoUnit.DAYS);

        ServiceVo service = SERVICE_VO_1.toBuilder()
                .maxApiKeyTtlDays(4000)
                .build();

        assertDoesNotThrow(() -> validator.assertExpirationDateValid(service, expiresAt));
    }

    @Test
    void assertKeyActive_passWhenStatusActive() {
        assertDoesNotThrow(() -> validator.assertKeyActive(KEY_VO_1));
    }

    @ParameterizedTest
    @EnumSource(value = KeyStatus.class, names = "ACTIVE", mode = Mode.EXCLUDE)
    void assertKeyActive_throwsIfNotActive(KeyStatus status) {
        KeyVo keyInStatus = KEY_VO_1.toBuilder()
                .keyStatus(status)
                .build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> validator.assertKeyActive(keyInStatus),
                "Key is not active");
    }

    @Test
    void getValidAudienceServiceIds_defaultsToServiceId() {
        assertThat(validator.getValidAudienceServiceIds(SERVICE_VO_1, null))
                .isEqualTo(Set.of(SERVICE_ID_1));
    }

    @Test
    void getValidAudienceServiceIds_throwsIfRequestedAudServiceIdsEmpty() {
        Set<String> audienceServiceIds = Set.of();
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.getValidAudienceServiceIds(SERVICE_VO_1, audienceServiceIds),
                "audience_service_ids can't be empty");
    }

    @Test
    void getValidAudienceServiceIds_throwsIfRequestedAudServiceIdsIsNotSubsetOfAllowed() {
        Set<String> audienceServiceIds = Set.of(SERVICE_ID_1, "ID_2", "ID_3");
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.getValidAudienceServiceIds(SERVICE_VO_1, audienceServiceIds),
                "service 'nvidia-cloud-functions-ncp-service-id-aketm'"
                        + " cannot use audiences: ID_2,ID_3");
    }

    @Test
    void getValidAudienceServiceIds_returnsValidatedSet() {
        Set<String> audienceServiceIds = Set.of(SERVICE_ID_1, "ID_2", "ID_3");
        ServiceVo serviceVo = SERVICE_VO_1.toBuilder()
                .audienceServiceIds(audienceServiceIds)
                .build();

        Set<String> requestedAudienceServiceIds = Set.of("ID_2", "ID_3");
        assertThat(validator.getValidAudienceServiceIds(serviceVo, requestedAudienceServiceIds))
                .isEqualTo(requestedAudienceServiceIds);
    }
}
