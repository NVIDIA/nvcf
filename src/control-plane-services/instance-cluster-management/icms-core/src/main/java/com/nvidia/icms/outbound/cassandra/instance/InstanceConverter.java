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
package com.nvidia.icms.outbound.cassandra.instance;

import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayKey;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneKey;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.util.TimeUtils;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import lombok.experimental.UtilityClass;

@UtilityClass
public class InstanceConverter {


    public static InstanceByZoneEntity toInstanceByZoneEntity(
            @NotNull InstanceV2Entity entity) {
        Instant createdTime = TimeUtils.getInstantFromUuid(entity.getCreateTimeuuid());
        return InstanceByZoneEntity.builder()
                .key(InstanceByZoneKey.builder()
                             .truncatedTs(TimeUtils.getFirstDateOfMonthFromInstant(createdTime))
                             .instanceId(entity.getInstanceId())
                             .zone(entity.getZone())
                             .build())

                // Fields from InstanceV2Entity
                .customer(entity.getCustomer())
                .requestId(entity.getRequestId())
                .imageId(entity.getImageId())
                .requestState(entity.getRequestState())
                .updateTimestamp(entity.getInstanceUpdateTime())
                .instanceStateCode(entity.getInstanceStateCode())
                .instanceStateName(entity.getInstanceStateName())
                .resourceProvider(entity.getResourceProvider())
                .errorLog(entity.getErrorLog())
                .errorSource(entity.getErrorSource())
                .requestStatusCode(entity.getRequestStatusCode())
                .requestStatusMessage(entity.getRequestStatusMessage())
                .requestStatusUpdateTime(entity.getRequestStatusUpdateTime())

                // New fields
                .ncaId(entity.getNcaId())
                .gpu(entity.getGpu())
                .backend(entity.getBackend())
                .instanceType(entity.getInstanceType())
                .instanceIps(entity.getInstanceIps())
                .request(entity.getRequestRawData())
                .build();
    }

    public static InstanceByDayEntity toInstanceByDayEntity(
            @NotNull InstanceV2Entity entity) {
        Instant entityDay = TimeUtils.getDateFromInstant(
                TimeUtils.getInstantFromUuid(entity.getCreateTimeuuid()));
        return InstanceByDayEntity.builder()
                .key(InstanceByDayKey.builder()
                             .truncatedTsByDay(entityDay)
                             .instanceId(entity.getInstanceId())
                             .build())
                .requestId(entity.getRequestId())
                .createTimeuuid(entity.getCreateTimeuuid())
                .build();
    }

}
