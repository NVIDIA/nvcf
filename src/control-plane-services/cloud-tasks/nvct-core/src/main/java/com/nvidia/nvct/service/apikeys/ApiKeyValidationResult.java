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
package com.nvidia.nvct.service.apikeys;

import com.fasterxml.jackson.annotation.JsonIgnore;
import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.annotation.JsonSetter;
import com.fasterxml.jackson.annotation.Nulls;
import com.google.common.annotations.VisibleForTesting;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.core.DefaultOAuth2AuthenticatedPrincipal;
import org.springframework.security.oauth2.core.OAuth2AuthenticatedPrincipal;
import org.springframework.util.StringUtils;

/**
 * Represents the result of API Key validation.
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

    public static final String TASK_ACCESS_ATTRIBUTE = "task_access";
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
                TASK_ACCESS_ATTRIBUTE, allAllowedTasks(policy.resources),
                POLICY_RESULT_ATTRIBUTE, this);
        var scopes = policy.scopes.stream()
                .map(scope -> (GrantedAuthority) new SimpleGrantedAuthority("apikey:" + scope))
                .toList();
        return new DefaultOAuth2AuthenticatedPrincipal(ownerId, resourcesAttribute, scopes);
    }

    public record ApiKeyTaskAccess(Set<UUID> allowedTaskIds,
                                   boolean privateTasksAllowed) {

        public boolean hasResourcesScopedForTask(UUID taskId) {
            return allowedTaskIds.contains(taskId);
        }
    }

    @VisibleForTesting
    static ApiKeyTaskAccess allAllowedTasks(List<Resource> resources) {
        Set<UUID> allowedTaskIds = new HashSet<>();
        boolean privateTasksAllowed = false;

        for (var resource : resources) {
            if ("account-tasks".equals(resource.type()) && "*".equals(resource.id())) {
                privateTasksAllowed = true;
            }
            if (!"task".equals(resource.type())) {
                continue;
            }
            var resourceId = resource.id();
            if (resourceId == null) {
                continue;
            }

            try {
                var resourceTaskId = UUID.fromString(resourceId);
                allowedTaskIds.add(resourceTaskId);
            } catch (Exception e) {
                // continue
            }
        }
        return new ApiKeyTaskAccess(allowedTaskIds, privateTasksAllowed);
    }
}
