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
package com.nvidia.nvcf.service.apikeys;

import com.fasterxml.jackson.annotation.JsonIgnore;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.annotation.JsonSetter;
import com.fasterxml.jackson.annotation.Nulls;
import com.google.common.annotations.VisibleForTesting;
import jakarta.annotation.Nullable;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.Getter;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.core.DefaultOAuth2AuthenticatedPrincipal;
import org.springframework.security.oauth2.core.OAuth2AuthenticatedPrincipal;
import org.springframework.util.StringUtils;

/**
 * Represents the result of ApiKey validation.
 *
 * @param allowed    indicates whether the current request should be allowed to proceed
 * @param ncaId      NVIDIA Cloud Account(NCA) id
 * @param ownerId    for Service Keys, this parameter will be NCA Id; for Personal Keys,
 *                   this parameter will be OIDC Id
 * @param policy     resource types and scopes
 */
public record ApiKeyValidationResult(@JsonProperty("allowed") boolean allowed,
                              @JsonProperty("ncaId") String ncaId,
                              @JsonProperty("ownerId") String ownerId,
                              @JsonProperty("policy") Policy policy) {

    public static final String FUNCTION_ACCESS_ATTRIBUTE = "function_access";
    public static final String POLICY_RESULT_ATTRIBUTE = "policy_result";

    public ApiKeyValidationResult(
            boolean allowed,
            String ncaId,
            String ownerId,
            Policy policy) {
        this.allowed = allowed;
        this.ncaId = ncaId;
        this.ownerId = ownerId;
        this.policy = policy;
    }

    public record Resource(@JsonProperty("type") String type, @JsonProperty("id") String id) {

    }

    public record Policy(
            @JsonProperty("resources") @JsonSetter(nulls = Nulls.AS_EMPTY) List<Resource> resources,
            @JsonProperty("scopes") @JsonSetter(nulls = Nulls.AS_EMPTY) List<String> scopes,
            @JsonProperty("product") String product) {

    }

    public boolean valid() {
        return allowed && StringUtils.hasText(ncaId) && StringUtils.hasText(ownerId) &&
                policy != null;
    }

    @JsonIgnore
    public OAuth2AuthenticatedPrincipal getOAuth2Principal() {
        Map<String, Object> resourcesAttribute = Map.of(
                FUNCTION_ACCESS_ATTRIBUTE, allAllowedFunctions(policy.resources),
                POLICY_RESULT_ATTRIBUTE, this);
        var scopes = policy.scopes.stream()
                .map(scope -> (GrantedAuthority) new SimpleGrantedAuthority("apikey:" + scope))
                .toList();
        return new DefaultOAuth2AuthenticatedPrincipal(ownerId, resourcesAttribute, scopes);
    }

    public static class ApiKeyFunctionVersionSpecifier {

        private final Set<UUID> allowedVersions = new HashSet<>();
        @Getter
        private boolean allAllowed;

        public void add(String versionId) {
            if (allAllowed) {
                return;
            }
            if ("*".equals(versionId)) {
                allowedVersions.clear();
                allAllowed = true;
                return;
            }
            allowedVersions.add(UUID.fromString(versionId));
        }

        public Set<UUID> getAllowedVersions() {
            if (allAllowed) {
                throw new IllegalStateException("all versions are allowed, check that first");
            }
            return Collections.unmodifiableSet(allowedVersions);
        }
    }


    public record ApiKeyFunctionAccess(Map<UUID, ApiKeyFunctionVersionSpecifier> allowedVersions,
                                    boolean privateFunctionsAllowed,
                                    boolean azpFunctionsAllowed) {

        public boolean hasResourcesScopedForFunction(
                UUID functionId,
                @Nullable UUID functionVersionId) {
            var versionSpecifier = allowedVersions.get(functionId);
            if (versionSpecifier == null) {
                return false;
            }
            if (versionSpecifier.isAllAllowed()) {
                return true;
            }
            // if not allowed for all and no version specified then we're not allowed to access
            // all versions of this function
            if (functionVersionId == null) {
                return false;
            }
            return versionSpecifier.getAllowedVersions().contains(functionVersionId);
        }
    }

    @VisibleForTesting
    static ApiKeyFunctionAccess allAllowedFunctions(List<Resource> resources) {
        Map<UUID, ApiKeyFunctionVersionSpecifier> allowedVersions = new HashMap<>(resources.size());
        boolean privateFunctionsAllowed = false;
        // azpFunctionsAllowed allows a user to continue using the same ApiKey to invoke and list
        // shared functions without having to generate a new one.
        boolean azpFunctionsAllowed = false;

        for (var resource : resources) {
            if ("account-functions".equals(resource.type()) && "*".equals(resource.id())) {
                privateFunctionsAllowed = true;
            }
            if ("authorized-functions".equals(resource.type()) && "*".equals(resource.id())) {
                azpFunctionsAllowed = true;
            }
            if (!"function".equals(resource.type())) {
                continue;
            }
            var resourceId = resource.id();
            if (resourceId == null) {
                continue;
            }
            var split = resourceId.indexOf('/');
            // no /, so it doesn't match the pattern
            if (split < 0) {
                continue;
            }
            UUID resourceFunctionId;
            try {
                resourceFunctionId = UUID.fromString(resourceId.substring(0, split));
            } catch (Exception e) {
                continue;
            }
            var versionSpecifier = allowedVersions.computeIfAbsent(
                    resourceFunctionId,
                    ignore -> new ApiKeyFunctionVersionSpecifier());
            var resourceFunctionVersion = resourceId.substring(split + 1);
            try {
                versionSpecifier.add(resourceFunctionVersion);
            } catch (Exception e) {
                // continue
            }
        }
        return new ApiKeyFunctionAccess(allowedVersions, privateFunctionsAllowed, azpFunctionsAllowed);
    }
}
