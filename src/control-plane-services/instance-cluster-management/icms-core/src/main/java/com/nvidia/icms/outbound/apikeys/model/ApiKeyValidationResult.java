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
package com.nvidia.icms.outbound.apikeys.model;

import com.fasterxml.jackson.annotation.JsonIgnore;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.annotation.JsonSetter;
import com.fasterxml.jackson.annotation.Nulls;
import java.util.List;
import java.util.Map;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.core.DefaultOAuth2AuthenticatedPrincipal;
import org.springframework.security.oauth2.core.OAuth2AuthenticatedPrincipal;
import org.springframework.util.StringUtils;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Builder
public class ApiKeyValidationResult {

    public static final String POLICY_RESULT_ATTRIBUTE = "policy_result";
    public static final String API_KEY_SCOPE_PREFIX = "apikey:";

    @JsonProperty("allowed")
    private boolean allowed;

    @JsonProperty("ncaId")
    private String ncaId;

    @JsonProperty("ownerId")
    private String ownerId;

    @JsonProperty("policy")
    private Policy policy;

    public boolean isValid() {
        return allowed && StringUtils.hasText(ncaId) && StringUtils.hasText(ownerId) &&
                policy != null;
    }

    @JsonIgnore
    public OAuth2AuthenticatedPrincipal getOAuth2Principal() {
        Map<String, Object> resourcesAttribute = Map.of(POLICY_RESULT_ATTRIBUTE, this);
        var scopes = policy.scopes.stream()
                .map(scope -> (GrantedAuthority) new SimpleGrantedAuthority(
                        API_KEY_SCOPE_PREFIX + scope))
                .toList();
        return new DefaultOAuth2AuthenticatedPrincipal(ownerId, resourcesAttribute, scopes);
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class Policy {

        @JsonProperty("aud")
        private String aud;

        @JsonProperty("resources")
        @JsonSetter(nulls = Nulls.AS_EMPTY)
        private List<Resource> resources;

        @JsonProperty("scopes")
        @JsonSetter(nulls = Nulls.AS_EMPTY)
        private List<String> scopes;

        @JsonProperty("product")
        private String product;
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class Resource {

        @JsonProperty("type")
        private String type;

        @JsonProperty("id")
        private String id;
    }
}
