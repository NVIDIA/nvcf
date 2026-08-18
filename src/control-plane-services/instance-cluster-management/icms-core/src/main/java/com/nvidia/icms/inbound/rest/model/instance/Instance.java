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
package com.nvidia.icms.inbound.rest.model.instance;


import com.fasterxml.jackson.annotation.JsonProperty;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.HealthInfo;
import com.nvidia.icms.inbound.rest.model.instance.InstanceLaunchSpecification.Placement;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
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
@Schema(description = "Instance status")
public class Instance {

    @JsonProperty("CreateTime")
    @Schema(description = "Instance creation time")
    private Instant createTime;

    @JsonProperty("ImageId")
    @Schema(description = "Instance image Id")
    private String imageId;

    @JsonProperty("ContainerImage")
    @Schema(description = "Container image to launch on the instance")
    private String containerImage;

    @JsonProperty("InstanceId")
    @Schema(description = "Instance Id")
    private String instanceId;

    @JsonProperty("CloudProvider")
    @Schema(description = "Cloud provider")
    private CloudProvider cloudProvider;

    @JsonProperty("InstanceType")
    @Schema(description = "Instance type")
    private String instanceType;

    @JsonProperty("Placement")
    @Schema(description = "Instance placement")
    private Placement placement;

    @JsonProperty("State")
    @Schema(description = "Instance state")
    private InstanceState state;

    @JsonProperty("HealthInfo")
    @Schema(description = "Instance health info")
    private HealthInfo healthInfo;

    @JsonProperty("LaunchRequestId")
    @Schema(description = "Launch request Id")
    private String launchRequestId;

    @JsonProperty("InstanceIps")
    @Schema(description = "Instance IP addresses")
    private Set<String> instanceIps;

    @JsonProperty("CapacityType")
    @Schema(description = "Capacity type of instance - RESERVED/SPOT/RESERVED_BACKUP")
    private CapacityType capacityType;

    @JsonProperty("DeploymentId")
    @Schema(description = "Deployment Id")
    private String deploymentId;

    @JsonProperty("GpuSpecificationId")
    @Schema(description = "Gpu specification Id")
    private String gpuSpecificationId;

    @JsonProperty("RequestId")
    @Schema(description = "The Id of the request related to this instance")
    private String requestId;

    @JsonProperty("Gpu")
    @Schema(description = "Gpu attached to this instance")
    private String gpu;

    @JsonProperty("UpdateTime")
    @Schema(description = "Last time instance status was updated")
    private Instant updateTime;
}
