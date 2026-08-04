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
package com.nvidia.icms.outbound.cassandra.request;

import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayEntity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayKey;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.util.TimeUtils;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import lombok.experimental.UtilityClass;

@UtilityClass
public class InstanceRequestConverter {


    public static InstanceRequestV2ByDayEntity toInstanceRequest2ByDayEntity(
            @NotNull InstanceRequestV2Entity entity) {

        Instant entityDay = TimeUtils.getDateFromInstant(
                TimeUtils.getInstantFromUuid(entity.getCreateTimeuuid()));
        return InstanceRequestV2ByDayEntity.builder()
                .key(InstanceRequestV2ByDayKey.builder()
                             .truncatedTsByDay(entityDay)
                             .requestId(entity.getRequestId())
                             .build())
                .createTimeuuid(entity.getCreateTimeuuid())
                .build();
    }
}
