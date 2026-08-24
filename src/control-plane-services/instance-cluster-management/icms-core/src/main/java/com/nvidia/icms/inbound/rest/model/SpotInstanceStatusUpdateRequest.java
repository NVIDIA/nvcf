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

import com.nvidia.icms.outbound.sqs.model.CapacityType;
import io.swagger.v3.oas.annotations.media.Schema;

import jakarta.validation.constraints.NotNull;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Update instance status")
@Builder
public class SpotInstanceStatusUpdateRequest {

    @Schema(description = "Original action of the request")
    SpotInstanceRequestAction action;

    @Schema(description = "State of the instance for the specified instance id")
    @NotNull(message = "instanceState must not be null")
    SpotInstanceInternalState instanceState;

    @Schema(description = "State of the request with which this instance is created")
    @NotNull(message = "requestState must not be null")
    SpotInstanceRequestState requestState;

    @Schema(description = "Status of the instance for the specified instance id",
            allowableValues = {"fulfilled", "instance-terminated-no-capacity", "instance-terminated-by-user",
                    "instance-terminated-by-service"},
            type = "String")
    @NotNull(message = "status must not be null")
    SpotInstanceStatus status;

    @Schema(description = "Placement related details for instance")
    InstancePlacement placement;

    @Schema(description = "Image id used for launching the instance")
    String imageId;

    @Schema(description = "Verbose information about instance termination, "
            + "cluster can send any verbose termination reason which could be helpful in debugging")
    @Nullable
    String terminationCause;

    @Schema(description = "Health info of instance")
    @Nullable
    SpotInstanceHeathInfo healthInfo;

    @Schema(description = "System Failure is downstream instance setup error")
    @Nullable
    String systemFailure;

    @Schema(description = "IP addresses of various interfaces of the instance")
    @Nullable
    Set<String> instanceIps;

    @Schema(description = "ReservationId for which instance is created")
    @Nullable
    UUID reservationId;

    @Schema(description = "Capacity type of the instance", allowableValues = {"SPOT", "RESERVED", "RESERVED_BACKUP"}, defaultValue = "SPOT")
    @Nullable
    CapacityType capacityType;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    public static class InstancePlacement {

        @Schema(description = "Zone where the instance is placed")
        String availabilityZone;

    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class SpotInstanceHeathInfo {

        @Schema(description = "Logs of failed container")
        String errorLog;

        @Schema(description = "Error source of error log")
        String errorSource;
    }

    public CapacityType getCapacityType() {
        // If capacityType not provided then retuning SPOT as DEFAULT capacityType
        return Objects.requireNonNullElse(this.capacityType, CapacityType.SPOT);
    }
}
