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
 * RFC 7662-style introspection result for a worker token.
 *
 * <p>When {@code active} is true every field except {@code error} is populated. The
 * workload binding ({@code request_id}, {@code function_id}/{@code function_version_id} or
 * {@code task_id}/{@code nca_id}) is resolved from ICMS state for the instance the token
 * was registered against; relying parties MUST compare it with the workload named in the
 * worker's request.</p>
 */
@Data
@Builder
@JsonInclude(JsonInclude.Include.NON_NULL)
public class WorkerTokenIntrospectResponse {

    private boolean active;

    private String sub;

    private String aud;

    private String iss;

    /** Token expiry, seconds since the epoch. */
    private Long exp;

    /** Resolved cluster ID (from the audience claim). */
    @JsonProperty("client_id")
    private String clientId;

    @JsonProperty("instance_id")
    private String instanceId;

    @JsonProperty("worker_id")
    private String workerId;

    @JsonProperty("request_id")
    private String requestId;

    @JsonProperty("function_id")
    private String functionId;

    @JsonProperty("function_version_id")
    private String functionVersionId;

    @JsonProperty("task_id")
    private String taskId;

    @JsonProperty("nca_id")
    private String ncaId;

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
