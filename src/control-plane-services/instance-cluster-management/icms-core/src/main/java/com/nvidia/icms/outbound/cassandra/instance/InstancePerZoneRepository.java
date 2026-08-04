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

import static com.nvidia.icms.util.TimeUtils.getFirstDateOfPreviousMonth;

import com.nvidia.icms.configuration.bean.DbConfigurationProperties;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.outbound.cassandra.SliceResult;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneKey;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.util.TransitionPhase;
import io.micrometer.observation.annotation.Observed;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class InstancePerZoneRepository {

    private final InstanceByZoneRepo instanceByZoneRepo;

    private final InstanceV2Repo instanceV2Repo;

    private final DbConfigurationProperties dbConfigurationProperties;

    private final IcmsConfigurationProperties icmsConfigurationProperties;


    @Observed
    public void insert(InstanceByZoneEntity entity) {

        try {
            if (!instanceByZoneRepo.isInsertedIfNotExists(entity)) {
                log.error("{} zone with instanceId {} already exists", entity.getKey().getZone(),
                        entity.getKey().getInstanceId());
                throw new IcmsConflictException(String.format("Instance with %s id already exists",
                        entity.getKey().getInstanceId()));
            }
        } catch (IcmsConflictException icmsConflictException) {
            // rethrowing custom exception
            throw icmsConflictException;

        } catch (Exception exception) {
            log.error(
                    "class:InstancePerZoneRepository function: insert, failed insert entity, instanceId - {}, zone - {}, error - {}",
                    entity.getKey().getInstanceId(), entity.getKey().getZone(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to insert instancePerZoneRepository, error: %s",
                            exception.getMessage()), exception);
        }
    }

    @Observed
    public void update(InstanceByZoneEntity entity) {
        try {
            instanceByZoneRepo.update(entity);
        } catch (Exception exception) {
            log.error("class:InstancePerZoneRepository function: update, failed to update entity, instanceId - {}, zone - {}, error - {}",
                    entity.getKey().getInstanceId(), entity.getKey().getZone(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to update instanceByZoneEntity, error: %s",
                            exception.getMessage()), exception);
        }
    }

    @Observed
    public void delete(Instant truncatedTs, String zone, String instanceId) {
        try {
            instanceByZoneRepo.deleteById(InstanceByZoneKey.builder()
                                                      .truncatedTs(truncatedTs)
                                                      .zone(zone)
                                                      .instanceId(instanceId)
                                                      .build());
        } catch (Exception exception) {
            log.error("class:InstancePerZoneRepository function: delete, failed to delete entity, instanceId - {}, zone - {}, error - {}",
                    instanceId, zone, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to delete instanceByZoneEntity, error: %s",
                            exception.getMessage()), exception);
        }
    }

    @Observed
    public List<InstanceByZoneEntity> findAllActiveInstancesByZone(String zone) {

        try {
            return findAllByZoneWithPreviousMonths(zone, dbConfigurationProperties.getQueryDurationMonths(), true);
        } catch (Exception exception) {
            log.error(
                    "class:InstancePerZoneRepository function: findAllActiveInstancesByZone, failed fetch entities, zone - {}, error - {}",
                    zone, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceByZoneEntity, error: %s",
                            exception.getMessage()), exception);
        }
    }


    @Observed
    public Optional<InstanceByZoneEntity> findByZoneAndInstanceId(
            String zoneName,
            String instanceId) {

        try {
            Optional<InstanceV2Entity> optionalEntity = instanceV2Repo.findById(
                    instanceId);
            if (optionalEntity.isEmpty()) {
                log.error("Instance not found in database for instance id {}",
                          instanceId);
                return Optional.empty();
            }
            Instant createdTime = TimeUtils.getInstantFromUuid(
                    optionalEntity.get().getCreateTimeuuid());
            Instant firstDayOfMonth = TimeUtils.getFirstDateOfMonthFromInstant(createdTime);
            return instanceByZoneRepo.findByKeyTruncatedTsAndKeyZoneAndKeyInstanceId(
                    firstDayOfMonth,
                    zoneName,
                    instanceId);

        } catch (Exception exception) {
            log.error(
                    "class:InstancePerZoneRepository function: findByZoneAndInstanceId, failed fetch entities, instanceId - {}, zone - {}, error - {}",
                    instanceId, zoneName, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceByZoneEntity, error: %s",
                                  exception.getMessage()), exception);
        }
    }


    private SliceResult<InstanceByZoneEntity> findAllByZoneAndDate(
            String zone,
            Instant truncatedTs,
            SliceResult<InstanceByZoneEntity> previousResult) {

        if (previousResult != null && previousResult.canBeRequested()) {
            return new SliceResult<>(
                    instanceByZoneRepo.findAllByKeyTruncatedTsAndKeyZone(truncatedTs,
                                                                             zone,
                                                                             previousResult.generateCassandraPageRequest()),
                    previousResult.getLimit());
        } else {
            return new SliceResult<>(null, null, 0);
        }
    }


    private List<InstanceByZoneEntity> findAllByZoneWithPreviousMonths(
            String zone, int previousMonths, boolean activeOnly) {

        List<InstanceByZoneEntity> instanceEntityList = new ArrayList<>();
        // go over all month to back
        for(int monthOffset = 0; monthOffset <=previousMonths; monthOffset++) {
            Instant firstDay = getFirstDateOfPreviousMonth(monthOffset);

            SliceResult<InstanceByZoneEntity> entities = new SliceResult<>(null, icmsConfigurationProperties.getDatabaseReadPageSize());
            do {
                entities = findAllByZoneAndDate(zone, firstDay, entities);
                if (entities.getResult() != null) {
                    entities.getResult().forEach(r -> {
                        if (!activeOnly || isInstanceActive(r.getInstanceStateName())) {
                            instanceEntityList.add(r);
                        }
                    });
                }
            } while (entities.hasNextData());
        }

        return instanceEntityList;
    }

    private boolean isInstanceActive(SpotInstanceInternalState instanceInternalState) {
        return Set.of(SpotInstanceInternalState.RUNNING,
                      SpotInstanceInternalState.STARTING,
                      SpotInstanceInternalState.SHUTTING_DOWN).contains(instanceInternalState);
    }

}
