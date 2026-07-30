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

package com.nvidia.apikeys.services;

import tools.jackson.databind.JsonNode;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.dto.authz.AuthzResponse;
import com.nvidia.apikeys.dto.introspection.IntrospectionResponse;
import java.util.Objects;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class AuthzService {

    private static final String POLICIES_FIELD = "policies";
    private static final String AUD_FIELD = "aud";

    private final NakProperties nakProperties;


    public AuthzResponse.Result evaluatePolicy(
            String audienceServiceId, IntrospectionResponse introspectionResponse) {

        return AuthzResponse.Result.builder()
                .ncaId(nakProperties.getNcaId())
                .ownerId(introspectionResponse.getOwnerId())
                .allowed(true)
                .policy(getPolicyByAudience(audienceServiceId, introspectionResponse))
                .build();
    }

    private JsonNode getPolicyByAudience(
            String audienceServiceId, IntrospectionResponse introspectionResponse) {
        JsonNode policies = Optional.ofNullable(introspectionResponse)
                .map(IntrospectionResponse::getAuthorizations)
                .map(authz -> authz.get(POLICIES_FIELD))
                .orElseThrow(() -> new RuntimeException(
                        "Found no policies in api keys authorizations"));

        if (!policies.isArray()) {
            throw new RuntimeException("Policies field is not an array");
        }

        for (JsonNode policy : policies) {
            if (policy.isObject() && policy.has(AUD_FIELD)) {
                JsonNode audNode = policy.get(AUD_FIELD);
                if (audNode.isString() && Objects.equals(audNode.asString(), audienceServiceId)) {
                    return policy;
                }
            }
        }

        throw new RuntimeException(
                "Found no matching policy in api keys authorizations for audience: "
                        + audienceServiceId);
    }
}
