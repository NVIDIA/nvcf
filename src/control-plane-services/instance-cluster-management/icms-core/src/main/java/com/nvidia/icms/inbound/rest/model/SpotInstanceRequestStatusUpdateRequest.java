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
import java.util.Objects;
import java.util.UUID;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Update instance requests status")
public class SpotInstanceRequestStatusUpdateRequest {

    @Schema(description = "Status of the instance request",
            allowableValues = {"pending-fulfillment", "schedule-expired", "cannot-fulfill"},
            type = "String")
    SpotRequestStatusCode status;

    @Nullable
    @Schema(description = "Message batch id of SQS object", type = "String")
    String messageBatchId;

    @Nullable
    @Schema(description = "Instance count requested in SQS object", type = "Integer")
    Integer instanceCount;

    @Nullable
    @Schema(description = "Placement related details for instance")
    SpotInstanceStatusUpdateRequest.InstancePlacement placement;

    @Nullable
    @Schema(description = "ReservationId for RESERVED_BACKUP acknowledgments")
    UUID reservationId;

    @Nullable
    @Schema(description = "Capacity type of the acknowledgment", allowableValues = {"SPOT", "RESERVED", "RESERVED_BACKUP"}, defaultValue = "SPOT")
    CapacityType capacityType;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    public static class InstancePlacement {

        @Schema(description = "Zone where the instance is placed")
        String availabilityZone;

    }

    public CapacityType getCapacityType() {
        // If capacityType not provided then returning SPOT as DEFAULT capacityType
        return Objects.requireNonNullElse(this.capacityType, CapacityType.SPOT);
    }
}
