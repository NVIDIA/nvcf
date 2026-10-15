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

import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.Clock;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Set;
import java.util.regex.Pattern;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.collections4.SetUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class KeyRequestValidator {

    private static final String INVALID_EXPIRATION_ERROR_FORMAT = "expires_at must be in the future and not more than %d days from now";
    private final Clock clock;

    private static final Pattern DESCRIPTION_PATTERN = Pattern.compile("^[a-zA-Z0-9\\s_-]{1,64}$");

    public void assertKeyActive(KeyVo keyVo) {
        if (keyVo.getKeyStatus() != KeyStatus.ACTIVE) {
            throw new BadRequestException("Key is not active");
        }
    }

    public void assertExpirationDateValid(ServiceVo service, Instant expiresAt) {
        Instant now = clock.instant();
        Instant maxExpirationFromNow = now.plus(service.getMaxApiKeyTtlDays(), ChronoUnit.DAYS);

        if (expiresAt == null
                || expiresAt.isBefore(now) || expiresAt.isAfter(maxExpirationFromNow)) {
            throw new BadRequestException(
                    String.format(INVALID_EXPIRATION_ERROR_FORMAT, service.getMaxApiKeyTtlDays()));
        }
    }

    public void assertDescriptionValid(String description) {
        if (description == null || !DESCRIPTION_PATTERN.matcher(description).matches()) {
            throw new BadRequestException("description is invalid");
        }
    }

    public Set<String> getValidAudienceServiceIds(
            ServiceVo service, Set<String> requestedAudienceServiceIds) {
        if (requestedAudienceServiceIds == null) {
            return Set.of(service.getServiceId());
        }
        if (requestedAudienceServiceIds.isEmpty()) {
            throw new BadRequestException("audience_service_ids can't be empty");
        }
        Set<String> validServiceIds = SetUtils.emptyIfNull(service.getAudienceServiceIds());
        Set<String> invalidIds = SetUtils.difference(requestedAudienceServiceIds, validServiceIds);
        if (!invalidIds.isEmpty()) {
            String invalidIdsList = invalidIds.stream().sorted()
                    .collect(Collectors.joining(","));
            throw new BadRequestException(String.format(
                    "service '%s' cannot use audiences: %s",
                    service.getServiceId(), invalidIdsList));
        }
        return requestedAudienceServiceIds;
    }
}
