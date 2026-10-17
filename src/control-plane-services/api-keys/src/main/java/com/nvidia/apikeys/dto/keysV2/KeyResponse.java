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

package com.nvidia.apikeys.dto.keysV2;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonInclude.Include;
import java.time.Instant;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.ToString;
import tools.jackson.databind.JsonNode;

/**
 * Shared key contract for create, get, update, and list. Only the create response carries the
 * complete key in value. Other responses carry the masked value. List items omit authorizations.
 */
@Data
@Builder(toBuilder = true)
@NoArgsConstructor
@AllArgsConstructor
@JsonInclude(Include.NON_NULL)
public class KeyResponse {

    private String id;

    // @Data's generated toString honors this exclusion, so the full key is never printed.
    @ToString.Exclude
    private String value;

    private KeyStatusV2 status;

    private String userId;

    private String ncaId;

    private String issuerServiceId;

    private Set<String> audienceServiceIds;

    private String description;

    private Instant createdAt;

    private Instant expiresAt;

    private Instant lastUsedAt;

    private JsonNode authorizations;

    // Lombok ignores @ToString.Exclude on the builder, so its toString would print the full key.
    public static class KeyResponseBuilder {

        @Override
        public String toString() {
            return "KeyResponse.KeyResponseBuilder()";
        }
    }
}
