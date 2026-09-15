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

import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.util.TimeUtils;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.UUID;

public class InstanceRequestEntityFactory {

    /**
     * Creates a default InstanceRequestV2Entity with specified or default values.
     * This factory method is used to create a new instance request entity with basic configuration.
     * The method allows for customization through optional parameters and an update action.
     *
     * @param requestId The unique identifier for the instance request. If null, a random UUID will be generated.
     * @param createdTime The timestamp when the request was created. If null, current time will be used.
     * @param customer The customer identifier. If null, a random string with "customer" prefix will be generated.
     * @param action An optional update action to modify the entity after creation.
     * @return A new InstanceRequestV2Entity with default or specified values.
     */

    public static @NotNull InstanceRequestV2Entity createDefaultInstanceRequestV2(
            @Nullable String requestId, @Nullable Instant createdTime, @Nullable String customer,
            @Nullable UpdateEntity<InstanceRequestV2Entity> action) {

        UUID timeUuid = createdTime != null ? TimeUtils.getUuidFromTimeStamp(createdTime)
                : TimeUtils.getTimeUuidNow();

        InstanceRequestV2Entity instanceRequestEntity = InstanceRequestV2Entity.builder()
                .requestId(requestId != null ? requestId : UUID.randomUUID().toString())
                .createTimeuuid(timeUuid)
                .customer(customer != null ? customer
                                  : RandomFactory.getRandomStringWithPrefix("customer", 5))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(SpotInstanceRequestState.OPEN)
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .ncaId(UUID.randomUUID().toString())
                .build();

        if (action != null) {
            action.update(instanceRequestEntity);
        }
        return instanceRequestEntity;
    }

}
