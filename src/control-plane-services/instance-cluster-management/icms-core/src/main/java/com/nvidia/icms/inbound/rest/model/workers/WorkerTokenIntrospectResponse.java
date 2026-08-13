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
package com.nvidia.icms.inbound.rest.model.workers;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
import lombok.Builder;
import lombok.Data;

/**
 * Response body for the worker token introspection endpoint (RFC 7662).
 *
 * <p>When {@code active} is true, all resolved identity fields are populated.
 * When {@code active} is false, only {@code active} and {@code error} are set
 * (RFC 7662 section 2.2).</p>
 */
@Data
@Builder
@JsonInclude(JsonInclude.Include.NON_NULL)
public class WorkerTokenIntrospectResponse {

    private boolean active;

    private String sub;
    private String aud;
    private String iss;

    /** Resolved cluster ID (canonical name from the audience claim). */
    @JsonProperty("client_id")
    private String clientId;

    @JsonProperty("instance_id")
    private String instanceId;

    @JsonProperty("worker_id")
    private String workerId;

    /** Token type: "psat" or "spiffe". */
    @JsonProperty("token_type")
    private String tokenType;

    private String error;

    public static WorkerTokenIntrospectResponse inactive(String error) {
        return WorkerTokenIntrospectResponse.builder()
                .active(false)
                .error(error)
                .build();
    }
}
