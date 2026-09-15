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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.params.provider.Arguments.arguments;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.util.TransitionPhase;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Optional;
import java.util.stream.Stream;
import org.jetbrains.annotations.NotNull;
import org.junit.jupiter.params.provider.Arguments;

public class InstanceTestBase extends IntegrationTest {

    void assertInstanceV2Entity(Optional<InstanceV2Entity> instanceV2Entity,
                                    String instanceId,
                                    String requestId,
                                    String customer,
                                    Instant createTime,
                                    @NotNull TransitionPhase transitionPhase ) {
        switch (transitionPhase)  {
            case WRITE_OLD_READ_OLD -> {
                assertTrue(instanceV2Entity.isEmpty());
                return;
            }
        }

        assertTrue(instanceV2Entity.isPresent());

        if (instanceId != null) {
            assertEquals(instanceId, instanceV2Entity.get().getInstanceId());
        }

        if (customer != null) {
            assertEquals(customer, instanceV2Entity.get().getCustomer());
        }

        if (requestId != null) {
            assertEquals(requestId, instanceV2Entity.get().getRequestId());
        }

        if (createTime != null) {
            assertEquals(createTime.truncatedTo(ChronoUnit.MILLIS),
                         TimeUtils.getInstantFromUuid(instanceV2Entity.get().getCreateTimeuuid()));
        }
    }


    void assertInstanceV2Entity(@NotNull Optional<InstanceV2Entity> instanceV2Entity, String instanceId, String requestId, String customer, Instant createTime ) {
        assertTrue(instanceV2Entity.isPresent());

        if (instanceId != null) {
            assertEquals(instanceId, instanceV2Entity.get().getInstanceId());
        }

        if (customer != null) {
            assertEquals(customer, instanceV2Entity.get().getCustomer());
        }

        if (requestId != null) {
            assertEquals(requestId, instanceV2Entity.get().getRequestId());
        }

        if (createTime != null) {
            assertEquals(createTime.truncatedTo(ChronoUnit.MILLIS),
                         TimeUtils.getInstantFromUuid(instanceV2Entity.get().getCreateTimeuuid()));
        }
    }

    void assertInstanceByDayEntity(@NotNull Optional<InstanceByDayEntity> instanceByDayEntity, String instanceId, String requestId, Instant createTime) {
        assertTrue(instanceByDayEntity.isPresent());

        if (instanceId != null) {
            assertEquals(instanceId, instanceByDayEntity.get().getKey().getInstanceId());
        }

        if (createTime != null) {
            assertEquals(createTime.truncatedTo(ChronoUnit.DAYS), instanceByDayEntity.get().getKey().getTruncatedTsByDay());
        }

        if (requestId != null) {
            assertEquals(requestId, instanceByDayEntity.get().getRequestId());
        }
    }

    void assertInstanceByZoneEntity(@NotNull Optional<InstanceByZoneEntity> instanceByZoneEntity, String instanceId, String requestId, String zone) {
        assertTrue(instanceByZoneEntity.isPresent());

        if (instanceId != null) {
            assertEquals(instanceId, instanceByZoneEntity.get().getKey().getInstanceId());
        }

        if (requestId != null) {
            assertEquals(requestId, instanceByZoneEntity.get().getRequestId());
        }

        if (zone != null) {
            assertEquals(zone, instanceByZoneEntity.get().getKey().getZone());
        }

    }

    void assertInstancePresents(@NotNull List<InstanceV2Entity> entities, String instanceId) {
        assertEquals(1, entities.stream().filter(r -> r.getInstanceId().equals(instanceId)).count());
    }


    private static @NotNull Stream<Arguments> getAllComboFor2Booleans() {
        return Stream.of(
                arguments(true, true),
                arguments(true, false),
                arguments(false, false),
                arguments(false, true)
        );
    }


}
