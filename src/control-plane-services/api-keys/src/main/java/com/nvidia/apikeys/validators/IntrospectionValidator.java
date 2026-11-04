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

import com.nvidia.apikeys.caching.CachingKeysLoader;
import com.nvidia.apikeys.caching.IntrospectionKeyOwnerValidator;
import com.nvidia.apikeys.dto.introspection.IntrospectionRequest;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Set;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.collections4.SetUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class IntrospectionValidator {

    private final IntrospectionKeyOwnerValidator introspectionKeyOwnerValidator;
    private final CachingKeysLoader cachingKeysLoader;

    public KeyVo validate(IntrospectionRequest request, String apiKeyHash) {
        KeyVo key = cachingKeysLoader.loadKeyByHash(apiKeyHash);

        assertKeyIsActive(key);
        assertAudienceServiceMatches(request, key);

        performKeyOwnerValidations(key);

        return key;
    }

    private void performKeyOwnerValidations(KeyVo key) {
        try {
            introspectionKeyOwnerValidator.assertKeyOwnerValid(key);
        } catch (Exception e) {
            if (e instanceof BootResponseException knownException) {
                // throw exception if we can make sense out of it. normally when something not found
                // or failed to connect to external service or DB failed
                throw knownException;
            } else {
                // if this is something where we don't know the origin and error code we just log it
                // and return generic error to not expose details
                log.error("Unexpected error while performing key owner validation:", e);
                throw new IllegalStateException("Unexpected error during key introspection");
            }
        }
    }

    private static void assertKeyIsActive(KeyVo key) {
        if (key.getKeyStatus() != KeyStatus.ACTIVE) {
            throw new NotFoundException("Key not found");
        }
    }

    private static void assertAudienceServiceMatches(IntrospectionRequest request, KeyVo key) {
        Set<String> validAudiences = SetUtils.emptyIfNull(key.getAudienceServiceIds());
        if (!validAudiences.contains(request.getAudienceServiceId())) {
            throw new BadRequestException("audience_service_id invalid");
        }
    }

}
