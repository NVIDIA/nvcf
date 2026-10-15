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
package com.nvidia.icms.outbound.sqs.model.byoc;

import java.util.Map;
import java.util.Set;

import com.fasterxml.jackson.annotation.JsonInclude;
import io.swagger.v3.oas.annotations.media.Schema;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@Builder
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Model for BYOC pod termination message")
public class ByocTerminatePodMessageModel {

    @Schema(description = "Unique identifier for the request", example = "req-123456")
    private String requestId;

    @Schema(description = "NCA (NVIDIA Cloud Agent) identifier", example = "nca-789012")
    private String ncaId;

    @Schema(description = "Action to be performed", example = "terminate")
    private String action;

    @Schema(description = "Set of instance IDs to be terminated", example = "[\"i-1234567890abcdef0\"]")
    private Set<String> instanceIds;

    @Schema(description = "Availability zone where the instances are located", example = "us-west-2a")
    private String availabilityZone;

    @Schema(description = "Trace parent for distributed tracing", example = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
    @JsonInclude(JsonInclude.Include.NON_NULL)
    private String traceParent;

    @Schema(description = "Trace state for distributed tracing", example = "{\"key\": \"value\"}")
    @JsonInclude(JsonInclude.Include.NON_NULL)
    private Map<String, String> traceState;
}
