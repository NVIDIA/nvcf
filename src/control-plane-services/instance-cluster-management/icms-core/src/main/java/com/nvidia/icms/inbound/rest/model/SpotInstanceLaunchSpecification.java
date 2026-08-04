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
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import io.swagger.v3.oas.annotations.media.Schema;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.NoArgsConstructor;


@Data
@AllArgsConstructor
@NoArgsConstructor
public class SpotInstanceLaunchSpecification {

    @JsonProperty("InstanceType")
    @Schema(description = "Instance type")
    private String instanceType;

    @JsonProperty("ContainerImage")
    @Schema(description = "Container image to launch on the instance")
    private String containerImage;

    @JsonProperty("Placement")
    @Schema(description = "Instance placement")
    private Placement placement;


    @JsonProperty("Gpu")
    @Schema(description = "Gpu attached to instance")
    private String gpu;

    @JsonProperty("Backend")
    @Schema(description = "Backend used to create instance")
    private String backend;

    @JsonProperty("NcaId")
    @Schema(description = "Id of the cloud account for which instances need to be created")
    private String ncaId;

    @JsonProperty("CapacityType")
    @Schema(description = "Capacity type of instance - RESERVED/SPOT/RESERVED_BACKUP")
    private CapacityType capacityType;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    public static class Placement {

        @JsonProperty("AvailabilityZone")
        @Schema(description = "Requested availability zone for the instances")
        private String availabilityZone;

    }
}
