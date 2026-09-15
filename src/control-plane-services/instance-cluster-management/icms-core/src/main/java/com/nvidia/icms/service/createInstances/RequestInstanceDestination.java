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
package com.nvidia.icms.service.createInstances;

import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import jakarta.annotation.Nullable;
import java.util.HashSet;
import java.util.Set;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;

@Builder(toBuilder = true)
@Data
@AllArgsConstructor
public class RequestInstanceDestination {

    private InstanceTypeV5Udt instanceType;
    private String clusterGroupName;
    private String clusterGroupId;
    /**
    Queue selection based on capacity type will be as follows:<br>
        SPOT:
            - each destination per unique queue (global/detailedTargeting)<br>
        RESERVED:
            - reserved zone's destination (zone specific queue)<br>
        RESERVED_BACKUP:
            - each destination per reservation per unique queue (global/detailedTargeting)
     */
    private String creationQueueUrl;
    private String ncaId;
    private Set<String> authorizedNcaIds;
    private CloudProvider cloudProvider;

    @Nullable
    private String clusterName;

    @Nullable
    private String clusterId;

    @Nullable
    private String region;

    private String gpuName;

    @Nullable
    private UUID reservationId;

    /**
     * We will have following different values for batchCount based on flow and capacityType
     * Non-BYOC:
     *  1. SPOT
     *  2. RESERVED
     *  3. RESERVED_BACKUP
     *  NVCA:
     *  1. SPOT
     */
    private Integer instanceBatchCount;

    private CapacityType capacityType;

    /**
     * Maximum number of instances that can be fulfilled for this destination.
     * For non-BYOC destinations: initialized to the request's instance count,
     * then adjusted to partial fulfillment count if capacity is insufficient.
     * Used by SQS messages to request the correct number of instances.
     */
    private Integer maxFulfillableInstances;

    /**
     * Copy constructor that creates a new RequestInstanceDestination from an existing one.
     *
     * @param source The RequestInstanceDestination object to copy from
     */
    public RequestInstanceDestination(RequestInstanceDestination source) {
        this.instanceType = source.instanceType;
        this.clusterGroupName = source.clusterGroupName;
        this.clusterGroupId = source.clusterGroupId;
        this.creationQueueUrl = source.creationQueueUrl;
        this.ncaId = source.ncaId;
        this.authorizedNcaIds = new HashSet<>(source.authorizedNcaIds);
        this.cloudProvider = source.cloudProvider;
        this.clusterName = source.clusterName;
        this.clusterId = source.clusterId;
        this.region = source.region;
        this.gpuName = source.gpuName;
        this.reservationId = source.reservationId;
        this.instanceBatchCount = source.instanceBatchCount;
        this.capacityType = source.capacityType;
        this.maxFulfillableInstances = source.maxFulfillableInstances;
    }

    public boolean isReserved() {
        return capacityType != null && capacityType == CapacityType.RESERVED;
    }

    public boolean isReservedBackup() {
        return capacityType != null && capacityType == CapacityType.RESERVED_BACKUP;
    }

    public boolean isSpot() {
        return capacityType != null && capacityType == CapacityType.SPOT;
    }

    public static boolean isReservedOrReservedBackupDestinations(Set<RequestInstanceDestination> destinations) {
        if (isSetEmptyOrNull(destinations)) {
            return false;
        }
        return destinations.stream().allMatch(RequestInstanceDestination::isReservedOrReservedBackupDestination);
    }

    public static boolean isReservedOrReservedBackupDestination(RequestInstanceDestination destination) {
        return destination != null && (destination.isReserved() || destination.isReservedBackup());
    }
}
