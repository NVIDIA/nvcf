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

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.converters.ValidatingAuthorizationsConverter;
import com.nvidia.apikeys.dto.keysV2.CreateKeyRequest;
import com.nvidia.apikeys.services.CredentialService;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.services.ServicesService;
import com.nvidia.apikeys.vo.CreateKeyV2RequestVo;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.temporal.ChronoUnit;
import lombok.RequiredArgsConstructor;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import tools.jackson.databind.JsonNode;

/**
 * Validation shared by the key-owner, account-admin and platform-admin endpoints. The NCA,
 * issuer service and user come from the route and the body holds only key attributes.
 */
@Service
@RequiredArgsConstructor
public class KeyManagementRequestValidator {

    public static final int DEFAULT_LIMIT = 50;
    public static final int MAX_LIMIT = 100;
    // a list with no NCA, service or user restriction scans the whole table
    public static final int MAX_UNFILTERED_LIMIT = 50;

    static final String RESERVED_ID = "*";
    static final String MESG_ID_INVALID = "%s is invalid";
    static final String MESG_UNKNOWN_SERVICE = "Unknown issuer service: '%s'";
    static final String MESG_LIMIT_INVALID = "limit must be between 1 and %d";

    private final NakProperties nakProperties;
    private final ServicesService servicesService;
    private final CredentialService credentialService;
    private final KeyRequestValidator keyRequestValidator;
    private final ValidatingAuthorizationsConverter authorizationsConverter;
    private final MillisecondPrecisionClock clock;

    /**
     * A route ID must be present and must not be the reserved wildcard.
     */
    public void assertRouteId(String name, String value) {
        if (StringUtils.isBlank(value) || RESERVED_ID.equals(value)) {
            throw new BadRequestException(MESG_ID_INVALID.formatted(name));
        }
    }

    /**
     * Resolves a registered issuer service. An unregistered ID, including a legacy service ID
     * that is not registered, is rejected.
     */
    public ServiceVo requireService(String serviceId) {
        assertRouteId("serviceId", serviceId);
        return servicesService.getServiceById(serviceId)
                .orElseThrow(() -> new BadRequestException(MESG_UNKNOWN_SERVICE.formatted(serviceId)));
    }

    public CreateKeyV2RequestVo validateCreateKeyRequest(
            ServiceVo service, String ncaId, String userId, CreateKeyRequest request) {
        keyRequestValidator.assertDescriptionValid(request.getDescription());
        keyRequestValidator.assertExpirationDateValid(service, request.getExpiresAt());
        var audienceServiceIds = keyRequestValidator.getValidAudienceServiceIds(
                service, request.getAudienceServiceIds());
        var authorizations = validateAuthorizations(service, request.getAuthorizations());

        var generatedKey = credentialService.generateApiKey();
        var key = KeyV2Vo.builder()
                .keyStatus(KeyStatus.ACTIVE)
                .ncaId(ncaId)
                .ownerType(KeyOwnerType.USER)
                .ownerId(userId)
                .issuerServiceId(service.getServiceId())
                .audienceServiceIds(audienceServiceIds)
                .keyId(generatedKey.getKeyId())
                .keyHash(generatedKey.getKeyHash())
                .createdAt(clock.instant().truncatedTo(ChronoUnit.MILLIS))
                .expiresAt(request.getExpiresAt())
                .deletesAt(request.getExpiresAt().plus(nakProperties.getKeepAfterExpiredDuration()))
                .apiKeySuffix(generatedKey.getKeySuffix())
                .authorizations(authorizations)
                .description(request.getDescription())
                .build();
        return CreateKeyV2RequestVo.builder()
                .service(service)
                .key(key)
                .generatedKey(generatedKey)
                .build();
    }

    public String validateAuthorizations(ServiceVo service, JsonNode authorizations) {
        return authorizationsConverter.readValidAuthorizations(service, authorizations);
    }

    /**
     * An absent limit uses the default. A list without any restriction has a lower cap.
     */
    public int resolveLimit(Integer requested, boolean unfiltered) {
        var max = unfiltered ? MAX_UNFILTERED_LIMIT : MAX_LIMIT;
        if (requested == null) {
            return Math.min(DEFAULT_LIMIT, max);
        }
        if (requested < 1 || requested > max) {
            throw new BadRequestException(MESG_LIMIT_INVALID.formatted(max));
        }
        return requested;
    }
}
