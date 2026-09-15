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
package com.nvidia.icms.service.scheduled.cleanup;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.SliceResult;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.ExpiredInstanceTerminateService;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.jetbrains.annotations.Nullable;
import org.springframework.data.domain.Limit;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;

@Slf4j
@AllArgsConstructor
public class InstancesByDayCleanupExecutor implements
        DatabaseCleanupExecutor<InstanceByDayEntity> {

    private final InstanceV2Repository instanceV2Repository;

    private final InstanceRequestV2Repository instanceRequestV2Repository;

    private final ExpiredInstanceTerminateService expiredInstanceTerminateService;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    @Nullable
    @Override
    public InstanceByDayEntity getFirstRecord(Instant day) {
        List<InstanceByDayEntity> entities = instanceV2Repository.getInstanceByDayRepo()
                .findAllByKeyTruncatedTsByDay(day, Limit.of(1));
        return entities != null && !entities.isEmpty() ? entities.getFirst() : null;
    }

    @Override
    public Slice<InstanceByDayEntity> findRecordsPerDay(Instant day, Pageable pageable) {
        return instanceV2Repository.getInstanceByDayRepo()
                .findAllByKeyTruncatedTsByDay(day, pageable);
    }

    @Override
    public boolean isRecordActive(InstanceByDayEntity dbRecord) {
        return dbRecord != null && !dbRecord.isMarkedAsDeleted();
    }

    @Override
    public void deleteRecordsByDay(Instant day) {
        instanceV2Repository.getInstanceByDayRepo().deleteByKeyTruncatedTsByDay(day);
    }

    @Override
    public void cleanupRecordsByDay(Instant day) {
        SliceResult<InstanceByDayEntity> entities = new SliceResult<>(null,
                                                                                  icmsConfigurationProperties.getDatabaseCleanupDbPageSize());

        do {
            entities = new SliceResult<>(
                    instanceV2Repository.getInstanceByDayRepo()
                            .findAllByKeyTruncatedTsByDay(day,
                                                          entities.generateCassandraPageRequest()),
                    entities.getLimit());

            entities.getResult().forEach(r -> {
                Optional<InstanceV2Entity> instance = instanceV2Repository.getInstanceV2Repo()
                        .findById(r.getKey().getInstanceId());

                if (instance.isPresent()) {
                    Optional<InstanceRequestV2Entity> instanceRequestV2Entity = instanceRequestV2Repository.findRequestById(
                            instance.get().getRequestId());
                    if (instanceRequestV2Entity.isEmpty()) { // if instance exists, but request does not
                        log.info(
                                "InstancesByDayCleanupExecutor : RequestId {} InstanceId {}: terminating instance because request does not exist.",
                                instance.get().getRequestId(),
                                instance.get().getInstanceId());
                        instanceV2Repository.delete(instance.get(), true);
                    } else {
                        log.info(
                                "InstancesByDayCleanupExecutor : RequestId {} InstanceId {}: terminating instance due to age.",
                                instance.get().getRequestId(),
                                instance.get().getInstanceId());

                        expiredInstanceTerminateService.terminateExpiredInstances(
                                List.of(instance.get()));
                    }
                } else {
                    log.warn(
                            "InstancesByDayCleanupExecutor : RequestId {} InstanceId {}: remove instance-by-day record without record in main instance table",
                            r.getRequestId(),
                            r.getKey().getInstanceId());
                    instanceV2Repository.delete(r);
                }
            });

        } while (entities.hasNextData());
    }
}
