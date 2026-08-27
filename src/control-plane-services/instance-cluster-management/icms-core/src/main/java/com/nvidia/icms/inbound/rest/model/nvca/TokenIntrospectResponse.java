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
package com.nvidia.icms.inbound.rest.model.nvca;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
import lombok.Builder;
import lombok.Data;

/**
 * Response body for the token introspection endpoint (RFC 7662).
 *
 * <p>Field names follow RFC 7662 conventions where applicable:
 * {@code active}, {@code sub}, {@code aud}, {@code iss}, {@code token_type}.
 * The {@code cluster_id} field carries the NVCF-specific resolved cluster ID
 * (was {@code client_id} in the initial draft — renamed because the token is
 * not an OAuth 2.0 client credential and naming it {@code client_id} confused
 * every downstream consumer).</p>
 */
@Data
@Builder
@JsonInclude(JsonInclude.Include.NON_NULL)
public class TokenIntrospectResponse {

    /** Whether the token is active (valid). */
    private boolean active;

    /** Subject claim from the JWT. */
    private String sub;

    /** Audience claim from the JWT. */
    private String aud;

    /** Issuer claim from the JWT. */
    private String iss;

    /** Resolved cluster ID (NVCF-specific — not a standard RFC 7662 field). */
    @JsonProperty("cluster_id")
    private String clusterId;

    /** Token type: "psat" or "spiffe". */
    @JsonProperty("token_type")
    private String tokenType;

    /** Error description (only present when active=false). */
    private String error;

    public static TokenIntrospectResponse inactive(String error) {
        return TokenIntrospectResponse.builder()
                .active(false)
                .error(error)
                .build();
    }
}
