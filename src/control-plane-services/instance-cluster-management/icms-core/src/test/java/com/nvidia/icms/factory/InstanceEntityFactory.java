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
package com.nvidia.icms.factory;

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.util.TimeUtils;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.UUID;
import jakarta.annotation.Nullable;

/**
 * Factory class for creating instance entities used in testing.
 * Provides methods to generate both SpotInstanceEntity and InstanceV2Entity with default values.
 */
public class InstanceEntityFactory {

        /**
     * Creates a InstanceV2Entity with default test values and UUID-based identifiers.
     *
     * @param instanceId Optional instance ID, random UUID used if null
     * @param requestId Optional request ID, random UUID used if null
     * @param createdTime Optional creation timestamp, current time used if null
     * @param customer Optional customer ID, random UUID used if null
     * @param action Optional update action to apply to the created entity
     * @return InstanceV2Entity configured with provided or default values
     */

    public static @NotNull InstanceV2Entity createDefaultInstanceV2(
            @Nullable String instanceId,
            @Nullable String requestId,
            @Nullable Instant createdTime,
            @Nullable String customer,
            @Nullable UpdateEntity<InstanceV2Entity> action) {
        UUID timeUuid = createdTime != null ? TimeUtils.getUuidFromTimeStamp(createdTime)
                : TimeUtils.getTimeUuidNow();

        InstanceV2Entity instanceEntity = InstanceV2Entity.builder()
                .instanceId(instanceId != null ? instanceId : UUID.randomUUID().toString())
                .requestId(requestId != null ? requestId : UUID.randomUUID().toString())
                .createTimeuuid(timeUuid)
                .customer(customer != null ? customer : UUID.randomUUID().toString().replace("_", ""))
                .requestState(SpotInstanceRequestState.ACTIVE)
                .requestStatusCode(SpotInstanceStatus.FULFILLED)
                .instanceStateName(SpotInstanceInternalState.RUNNING)
                .resourceProvider(ResourceProvider.BYOC)
                .instanceStateCode(SpotInstanceInternalState.getStateCode(SpotInstanceInternalState.RUNNING))
                .imageId(RandomFactory.getRandomStringWithPrefix("image", 5))
                .zone(RandomFactory.getRandomStringWithPrefix("zone", 5))
                .cloudProvider(CloudProvider.AWS.toString())
                .gpuCountPerInstance(2)
                .build();

        if (action != null) {
            action.update(instanceEntity);
        }
        return instanceEntity;



   }

}
