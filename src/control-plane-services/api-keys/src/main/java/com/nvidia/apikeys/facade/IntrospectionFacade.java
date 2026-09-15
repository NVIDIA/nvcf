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

import static com.nvidia.apikeys.services.ActionRecorder.Action.INTROSPECT;
import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.API_KEY;

import com.nvidia.apikeys.caching.IntrospectionLocalCache;
import com.nvidia.apikeys.converters.KeyResponseBuilder;
import com.nvidia.apikeys.dto.introspection.IntrospectionRequest;
import com.nvidia.apikeys.dto.introspection.IntrospectionResponse;
import com.nvidia.apikeys.services.ActionRecorder;
import com.nvidia.apikeys.utils.TracingUtils;
import com.nvidia.apikeys.validators.ApiKeyParser;
import com.nvidia.apikeys.validators.IntrospectionValidator;
import com.nvidia.apikeys.vo.CachedIntrospectionResponse;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.BootResponseException;
import java.util.Set;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class IntrospectionFacade {

    private final ApiKeyParser apiKeyParser;
    private final IntrospectionValidator validator;
    private final KeyResponseBuilder responseBuilder;
    private final IntrospectionLocalCache introspectionLocalCache;
    private final ActionRecorder actionRecorder;
    private final TracingUtils tracingUtils;

    public IntrospectionResponse introspect(IntrospectionRequest request) {
        tracingUtils.addIntrospectionRequestTags(request);

        // See if this request led to error recently
        throwIfRequestLedToExceptionRecently(request);

        // See if we served such response recently and it was a success
        var cachedSuccessfulResponse = introspectionLocalCache.getCachedResponse(request);

        CachedIntrospectionResponse cachedResponse = cachedSuccessfulResponse
                .orElseGet(() -> introspectAndCacheResult(request));

        logKeyIntrospectionAction(request, cachedResponse);

        // no cache hit, go the full circle
        return cachedResponse.response();
    }

    private CachedIntrospectionResponse introspectAndCacheResult(IntrospectionRequest request) {
        try {
            String apiKeyHash = apiKeyParser.rawApiKeyToHash(request.getKey());
            KeyVo key = validator.validate(request, apiKeyHash);
            IntrospectionResponse response = responseBuilder.toIntrospectionResponse(key);
            var cachedIntrospectionResponse = new CachedIntrospectionResponse(response, key);
            introspectionLocalCache.cacheValidResponse(request, cachedIntrospectionResponse);
            return cachedIntrospectionResponse;
        } catch (BootResponseException e) {
            introspectionLocalCache.cacheError(request, e);
            throw e;
        }
    }

    private void throwIfRequestLedToExceptionRecently(IntrospectionRequest request) {
        var exception = introspectionLocalCache.getKnownInvalidResponse(request);
        if (exception.isPresent()) {
            throw exception.get();
        }
    }

    private void logKeyIntrospectionAction(
            IntrospectionRequest request, CachedIntrospectionResponse cachedResponse) {
        KeyVo keyVo = cachedResponse.keyVo();
        actionRecorder.record(API_KEY, keyVo.getKeyId(), String.valueOf(keyVo.getOwnerType()),
                              keyVo.getOwnerId(), INTROSPECT, keyVo.getIssuerServiceId(),
                              Set.of(request.getAudienceServiceId()));

        tracingUtils.addKeyTags(keyVo);
    }
}
