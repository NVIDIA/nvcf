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
import java.time.Instant;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Builder
// TODO(sparve): We have to add imageId in response as per SDD, discuss with team for source
public class SpotInstanceRequest {

    @JsonProperty("CreateTime")
    @Schema(description = "Request creation time")
    private Instant createTime;

    @JsonProperty("InstanceId")
    @Schema(description = "Instance Id")
    private String instanceId;

    @JsonProperty("LaunchSpecification")
    @Schema(description = "Instance launch specifications")
    SpotInstanceLaunchSpecification spotInstanceLaunchSpecification;

    @JsonProperty("LaunchedAvailabilityZone")
    @Schema(description = "Launched availability zone of the instance request")
    private String launchedAvailabilityZone;

    @JsonProperty("SpotInstanceRequestId")
    @Schema(description = "Request Id")
    private String spotInstanceRequestId;

    @JsonProperty("SpotCloudProvider")
    @Schema(description = "Cloud provider")
    private CloudProvider spotCloudProvider;

    @JsonProperty("State")
    @Schema(description = "Instance request state")
    private SpotInstanceRequestState state;

    @JsonProperty("Status")
    @Schema(description = "Instance request status")
    private SpotInstanceRequestStatus status;

    @JsonProperty("InstanceState")
    @Schema(description = "Instance state")
    private SpotInstanceState instanceState;

    @JsonProperty("HealthInfo")
    @Schema(description = "Instance health info")
    private HealthInfo healthInfo;

    @JsonProperty("InstanceInterruptionBehavior")
    @Schema(description = "Instance interruption behavior")
    private String instanceInterruptionBehavior;

    @JsonProperty("InstanceIps")
    @Schema(description = "Instance IP addresses")
    private Set<String> instanceIps;

    @JsonProperty("DeploymentId")
    @Schema(description = "Deployment Id")
    private String deploymentId;

    @JsonProperty("GpuSpecificationId")
    @Schema(description = "Gpu specification Id")
    private String gpuSpecificationId;

}
