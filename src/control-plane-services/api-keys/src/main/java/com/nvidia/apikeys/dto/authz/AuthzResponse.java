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

package com.nvidia.apikeys.dto.authz;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
import jakarta.annotation.Nullable;
import tools.jackson.databind.JsonNode;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@Builder
@NoArgsConstructor
@AllArgsConstructor
public class AuthzResponse {

    private String namespace;

    @JsonProperty("rule_name")
    private String ruleName;

    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    public static class Result {

        @JsonProperty("allowed")
        boolean allowed;
        @JsonProperty("ncaId")
        String ncaId;
        @JsonProperty("ownerId")
        String ownerId;
        @JsonProperty("policy")
        JsonNode policy;
        // Only set for the *.llm_allow rule; absent otherwise. No rate-limit data source is
        // wired up yet (mirrors the same gap on the managed side), so always null for now.
        @JsonProperty("accountTokenRateLimit")
        @Nullable
        AccountTokenRateLimit accountTokenRateLimit;
    }

    // ALWAYS: the mapper's default is NON_NULL, but both fields being null is itself
    // meaningful (no rate configured) and callers expect the two keys present, not a bare {}.
    @Data
    @Builder
    @NoArgsConstructor
    @AllArgsConstructor
    @JsonInclude(JsonInclude.Include.ALWAYS)
    public static class AccountTokenRateLimit {

        @JsonProperty("inputTokenRateLimit")
        @Nullable
        String inputTokenRateLimit;
        @JsonProperty("outputTokenRateLimit")
        @Nullable
        String outputTokenRateLimit;
    }

    // Result for apikey.allow/apikey.llm_allow; AccountTokenRateLimit for the
    // tiered-rate-limit rule, which returns only the rate fields.
    private Object result;

}
