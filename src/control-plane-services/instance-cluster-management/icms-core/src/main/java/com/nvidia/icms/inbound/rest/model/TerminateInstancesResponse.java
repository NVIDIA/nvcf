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
package com.nvidia.icms.inbound.rest.model;

import com.fasterxml.jackson.annotation.JsonProperty;
import io.swagger.v3.oas.annotations.media.Schema;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Terminate instances response")
public class TerminateInstancesResponse {

    @JsonProperty("TerminatingInstances")
    @Schema(description = "Instance Ids")
    private List<TerminatingInstance> terminatingInstances;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class TerminatingInstance {

        @JsonProperty("InstanceId")
        @Schema(description = "Instance Id")
        private String instanceId;

        @JsonProperty("RequestId")
        @Schema(description = "Instance request id")
        private String requestId;

        @JsonProperty("CurrentState")
        @Schema(description = "Current state of the instance")
        private SpotInstanceState currentState;

        @JsonProperty("PreviousState")
        @Schema(description = "Previous state of the instance")
        private SpotInstanceState previousState;
    }
}
