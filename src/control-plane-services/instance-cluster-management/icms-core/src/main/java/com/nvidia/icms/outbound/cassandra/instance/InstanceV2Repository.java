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

import static com.nvidia.icms.service.telemetry.model.Events.DB_INSERT_FAILED;
import static com.nvidia.icms.service.telemetry.model.Events.DELETED_INCONSISTENT_DATA;
import static com.nvidia.icms.service.telemetry.model.Events.UPDATE_CONFLICT_RESOLUTION;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.DbQuery;
import com.nvidia.icms.util.DbQueryExecutor;
import com.nvidia.icms.util.DbQueryExecutorService;
import com.nvidia.icms.util.PeriodOfTime;
import com.nvidia.icms.util.TimeUtils;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.*;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.function.Consumer;
import lombok.AllArgsConstructor;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class InstanceV2Repository {

    @Getter
    private final InstanceV2Repo instanceV2Repo;
    @Getter
    private final InstanceByDayRepo instanceByDayRepo;

    private final InstancePerZoneRepository instancePerZoneRepository;

    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final TelemetryEventClient telemetryEventClient;

    private final DbQueryExecutorService dbQueryExecutorService;


    @Observed
    public void insert(@NotNull InstanceV2Entity entity) {
        if (entity.getCreateTimeuuid() == null) {
            entity.setCreateTimeuuid(TimeUtils.getTimeUuidNow());
        }

        try {
            if (!instanceV2Repo.isInserted(entity) ||
                    !instanceByDayRepo.isInserted(InstanceConverter.toInstanceByDayEntity(entity))) {

                // Handling conflict error
                handleConflictWhileInsert(entity);
                return;
            }

        } catch (IcmsConflictException icmsConflictException) {

            // Rethrowing custom conflict exception thrown by handleConflictWhileInsert
            throw icmsConflictException;

        } catch (Exception exception) {
            log.error(
                    "InstanceV2Repository: insert in DB failed, deleting entries to avoid inconsistency." +
                            "instance-id: {}, request-id: {}, instanceState: {} requestState: {}, error {} exception -",
                    entity.getInstanceId(),
                    entity.getRequestId(),
                    entity.getInstanceStateName().getStateName(),
                    entity.getRequestState().toString(),
                    exception.getMessage(),
                    exception);

            // Deleting entry from DB
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withInstanceId(entity.getInstanceId())
                                                               .withRequestId(entity.getRequestId())
                                                               .withResourceProvider(entity.getResourceProvider())
                                                               .withZoneName(entity.getZone())
                                                               .withError(exception.getMessage())
                                                               .withEventName(DB_INSERT_FAILED.toString())));

            delete(entity, true);

            // Cluster Agent should retry on 500 error
            String errMsg = String.format("Failed to write data in DB for instance-id %s",
                                          entity.getInstanceId());
            log.error(errMsg);
            throw new IcmsInternalServerException(errMsg, exception);
        }

        // Adding entity in the per-zone instance repository
        updateInstancePerZoneRepository(entity);
    }

    @Observed
    public void delete(@NotNull InstanceByDayEntity instanceByDayEntity) {
        instanceByDayEntity.setMarkedAsDeleted(true);
        instanceByDayRepo.update(instanceByDayEntity);
    }

    @Observed
    public void delete(@NotNull InstanceV2Entity entity, boolean loggingEnabled) {
        try {
            Instant entityDay = TimeUtils.getDateFromInstant(TimeUtils.getInstantFromUuid(entity.getCreateTimeuuid()));

            Optional<InstanceByDayEntity> instanceByDayEntity = instanceByDayRepo.findByKeyTruncatedTsByDayAndKeyInstanceId(entityDay,
                                                     entity.getInstanceId());
            instanceByDayEntity.ifPresent(this::delete);

            instanceV2Repo.deleteById(entity.getInstanceId());

            deleteFromInstancePerZoneRepository(entity);

            logDataInconsistency(loggingEnabled, entity);

        } catch (Exception exception) {
            log.error(
                    "InstanceV2Repository: Exception occurred while deleting entries from instance repository, " +
                            "instance-id: {} request-id: {} error: {} Exception: ",
                    entity.getInstanceId(),
                    entity.getRequestId(),
                    exception.getMessage(),
                    exception);
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withEventName(
                                                                       Events.INSTANCE_DELETION_FAILED.toString())
                                                               .withError(String.format("InstanceV2Repository: %s",
                                                                                        exception.getMessage()))));

            // Rollback logic
            // Update: Update existing entry if present else insert
            restore(entity);
            updateInstancePerZoneRepository(entity);

            // Rethrowing exception
            throw exception;
        }
    }


    @Observed
    public void update(@NotNull InstanceV2Entity entity) {
        try {
            updateInstancePerZoneRepository(entity);
            instanceV2Repo.update(entity);
        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: update, failed update entity, instanceId - {}, error - {}",
                    entity.getInstanceId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to update instanceEntity, error: %s",
                                  exception.getMessage()), exception);
        }
    }

    @Observed
    public void restore(@NotNull InstanceV2Entity entity) {
        instanceByDayRepo.update(InstanceConverter.toInstanceByDayEntity(entity));
        instanceV2Repo.update(entity);
    }


    // Find instances by instance ID
    @Observed
    public Optional<InstanceV2Entity> findInstanceById(@Nullable String instanceId) {
        try {
            if (StringUtils.isBlank(instanceId)) {
                return Optional.empty();
            }

            return instanceV2Repo.findById(instanceId);
        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: findByInstanceId, failed fetch entity, instanceId - {}, error - {}",
                    instanceId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceEntities, error: %s",
                                  exception.getMessage()), exception);
        }
    }


    @Observed
    public Optional<InstanceV2Entity> findInstanceByCustomerAndId(
            @Nullable String customer,
            @Nullable String instanceId) {

        try {
            Optional<InstanceV2Entity> optionalEntity = findInstanceById(instanceId);
            if (optionalEntity.isEmpty()) {
                log.info(
                        "InstanceV2Repository: Instance not found in database for instance id {}",
                        instanceId);
                return Optional.empty();
            }

        /* Todo Yury: this level of code should not throw any  exceptions, callers should
        if (StringUtils.isBlank(customer)) {
            return optionalEntity;
        }*/

            if (!optionalEntity.get().getCustomer().equals(customer)) {
                log.warn(
                        "InstanceV2Repository: Instance with id {} does not belong to the customer {}",
                        instanceId, customer);
                throw new IcmsBadRequestException(
                        String.format("Could not find instance with %s id",
                                      instanceId));
            }

            return optionalEntity;
        } catch (IcmsBadRequestException icmsBadRequestException) {
            // rethrowing custom exception
            throw icmsBadRequestException;

        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: findInstanceByCustomerAndId, failed fetch entity, customer - {}, instanceId - {}, error - {}",
                    customer, instanceId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceEntity, error: %s",
                                  exception.getMessage()), exception);
        }
    }

    @Observed
    public List<InstanceV2Entity> findInstancesByIds(@Nullable Set<String> instanceIds) {

        List<InstanceV2Entity> result = new ArrayList<>();
        if (instanceIds == null || instanceIds.isEmpty()) {
            return new ArrayList<>();
        }

        instanceIds.forEach(r -> {
            Optional<InstanceV2Entity> requestEntity = findInstanceById(r);
            requestEntity.ifPresent(result::add);
        });

        return result;
    }



    @Observed
    public List<InstanceV2Entity> findInstancesByCustomerAndIds(
            @Nullable String customer,
            @NotNull Set<String> instanceIds) {
        List<InstanceV2Entity> instances = findInstancesByIds(instanceIds);
        return filterByCustomer(instances, customer);
    }


    // Find instances by request ID

    @Observed
    public List<InstanceV2Entity> findInstancesByRequestId(@Nullable String requestId) {
        try {
            if (StringUtils.isBlank(requestId)) {
                return new ArrayList<>();
            }

            return instanceV2Repo.findAllByRequestId(requestId).toList();
        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: findInstancesByRequestId, failed fetch entities, requestId - {}, error - {}",
                    requestId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceEntities, error: %s",
                                  exception.getMessage()), exception);
        }
    }

    @Observed
    public List<InstanceV2Entity> findInstancesByDeploymentId(@Nullable UUID deploymentId) {
        try {
            if (deploymentId == null) {
                return new ArrayList<>();
            }

            return instanceV2Repo.findAllByDeploymentId(deploymentId).toList();
        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: findInstancesByDeploymentId, "
                            + "failed fetch entities, deploymentId - {}, error - {}",
                    deploymentId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceEntities, error: %s",
                                  exception.getMessage()), exception);
        }
    }

    @Observed
    public List<InstanceV2Entity> findInstancesByGpuSpecificationId(
            @Nullable UUID deploymentId,
            @Nullable UUID gpuSpecificationId) {
        try {
            if (deploymentId == null || gpuSpecificationId == null) {
                return new ArrayList<>();
            }

            return instanceV2Repo
                    .findAllByDeploymentIdAndGpuSpecificationId(deploymentId, gpuSpecificationId)
                    .toList();
        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: "
                            + "findInstancesByGpuSpecificationId, "
                            + "failed fetch entities, deploymentId - {}, gpuSpecificationId - {}, "
                            + "error - {}",
                    deploymentId, gpuSpecificationId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceEntities, error: %s",
                                  exception.getMessage()), exception);
        }
    }

    @Observed
    public List<InstanceV2Entity> findInstancesByRequestIds(@Nullable Set<String> requestIds) {
        List<InstanceV2Entity> result = new ArrayList<>();

        if (null == requestIds || requestIds.isEmpty()) {
            return result;
        }

        requestIds.forEach(r -> {
            List<InstanceV2Entity> instances = findInstancesByRequestId(r);
            result.addAll(instances);
        });

        return result;
    }

    @Observed
    public List<InstanceV2Entity> findInstancesByCustomerAndRequestIds(
            @Nullable String customer,
            @NotNull Set<String> requestIds) {

        return filterByCustomer(findInstancesByRequestIds(requestIds), customer);
    }

    @Observed
    public Map<String, List<InstanceV2Entity>> findAllInstancesByCustomerAndRequestIds(
            @Nullable String customer,
            @Nullable Set<String> requestIds,
            boolean executeInParallel) {
        try {
            Map<String, List<InstanceV2Entity>> instanceEntityMap = new HashMap<>();

            if (null == requestIds || requestIds.isEmpty()) {
                return instanceEntityMap;
            }

            requestIds.forEach(r -> instanceEntityMap.put(r, new ArrayList<>()));

            List<InstanceV2Entity> instances;

            if (executeInParallel) {
                DbQueryExecutor<String, InstanceV2Entity> dbQueryExecutor = new DbQueryExecutor<>(
                        dbQueryExecutorService);
                instances = dbQueryExecutor.executeQueries(requestIds.stream().toList(),
                                                           icmsConfigurationProperties.getFindInstanceByRequestIdCallsPerThread(),
                                                           icmsConfigurationProperties.getFindInstanceByRequestIdThreadsInParallel(),
                                                           generateDbQueryByRequestId(),
                                                           "findByRequestIds");
                instances = filterByCustomer(instances, customer);
            }
            else {
                instances = findInstancesByCustomerAndRequestIds(customer, requestIds);
            }

            instances.forEach(r -> {
                if (instanceEntityMap.get(r.getRequestId()) == null) {
                    instanceEntityMap.put(r.getRequestId(), new ArrayList<>());
                }
                instanceEntityMap.get(r.getRequestId()).add(r);
            });

            return instanceEntityMap;
        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: findAllInstancesByCustomerAndRequestIds, failed to fetch entities, customer - {}, requestIds - {}, error - {}",
                    customer, requestIds, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceEntities, error: %s",
                                  exception.getMessage()), exception);
        }
    }


    /**
     * Get a list of instanceID in the period of time periodDateStart < periodDateEnd
     * @param periodDateStart
     * @param periodDateEnd
     * @return
     */
    public List<String> findInstanceIdsPerPeriod(
            @Nullable Instant periodDateStart,
            @Nullable Instant periodDateEnd) {

        if (periodDateStart == null || periodDateEnd == null ||
        periodDateStart.isAfter(periodDateEnd)) {
            return new ArrayList<>();
        }

        Instant currentDate = periodDateStart.truncatedTo(ChronoUnit.DAYS);
        periodDateEnd = periodDateEnd.plus(1, ChronoUnit.DAYS).truncatedTo(ChronoUnit.DAYS);

        List<String> allInstanceIds = new ArrayList<>();

        while (currentDate.isBefore(periodDateEnd)) {
            List<InstanceByDayEntity> instancesByDay = instanceByDayRepo.findAllByKeyTruncatedTsByDay(currentDate);
            instancesByDay.forEach(r -> {
                if (!r.isMarkedAsDeleted()) {
                    allInstanceIds.add(r.getKey().getInstanceId());
                }
            });
            currentDate = currentDate.plus(1, ChronoUnit.DAYS);
        }

        return allInstanceIds;
    }


    /**
     * Get a list of instances in the period of time periodDateStart < periodDateEnd
     * @param periodDateStart
     * @param periodDateEnd
     * @return
     */
    public List<InstanceV2Entity> findInstancesPerPeriod(
            @Nullable Instant periodDateStart,
            @Nullable Instant periodDateEnd) {
        List<String> allInstanceIds = findInstanceIdsPerPeriod(periodDateStart, periodDateEnd);
        return findInstancesByIds(new HashSet<>(allInstanceIds));
    }


    /**
     * Retrieves all instances from the database in paginated batches and applies a specified action to each instance.
     *
     * @param action The EntityAction to execute on each InstanceV2Entity. The action should return true if processing was successful.
     * @param pauseBetweenPagesInMs Time in milliseconds to pause between processing each page to avoid database overload
     *
     * Key features:
     * - Uses pagination to handle large datasets efficiently
     * - Implements configurable pause between page requests to prevent DB overload
     * - Tracks processed record count using AtomicInteger for thread safety
     * - Continues processing until no more pages are available
     *
     * Example usage:
     * EntityAction<InstanceV2Entity> action = entity -> {
     *     // Process entity
     *     return true;
     * };
     * int processedCount = findAllInstancesAndApplyAction(action, 1000);
     */
    @Observed
    public void findAllInstancesAndApplyAction(
            @Nullable Consumer<InstanceV2Entity> action,
            int pauseBetweenPagesInMs) {

        if (action == null) {
            log.warn("findAllInstancesAndApplyAction: Action is not provided, execution skipped");
            return;
        }

        instanceV2Repo.applyActions(instanceV2Repo::findAll,
                                        action,
                                        icmsConfigurationProperties.getDatabaseReadPageSize(),
                                        pauseBetweenPagesInMs,
                                        0); //TODO Yury do we need a pause here
    }


    @Observed
    public List<InstanceV2Entity> findAllForPreviousMonths(int monthOffset) {
        Instant currentDate = TimeUtils.getCurrentDate();
        Instant startDate = TimeUtils.getFirstDateOfPreviousMonth(monthOffset);
        List<PeriodOfTime> startOfPeriod = PeriodOfTime.buildQueryOptimizedPeriodsForDates(startDate, currentDate);

        List<InstanceV2Entity> result = new ArrayList<>();

        startOfPeriod.forEach(period -> result.addAll(findInstancesPerPeriod(period.start(), period.end())));

        return result;
    }


    @Observed
    public List<InstanceV2Entity> findAllByCustomer(@Nullable String customer) {
        try {
            List<InstanceV2Entity> instances = findAllForPreviousMonths(3);

            return filterByCustomer(instances, customer);
        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: findAllByCustomer, failed fetch entities, customer - {}, error - {}",
                    customer, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceEntities, error: %s",
                                  exception.getMessage()), exception);
        }
    }

    @Observed
    public List<InstanceV2Entity> findByReservationId(@Nullable UUID reservationId) {
        try {
            if (reservationId == null) {
                return List.of();
            }
            return instanceV2Repo.findByReservationId(reservationId).toList();
        } catch (Exception exception) {
            log.error(
                    "class:InstanceV2Repository function: findByReservationId, failed fetch entities, reservationId - {}, error - {}",
                    reservationId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch instanceEntities, error: %s",
                            exception.getMessage()), exception);
        }
    }

    /**
     * Task to find a InstanceV2Entity record in C* based on requestID. @parameters is a @requestIds
     */
    private DbQuery<String, InstanceV2Entity> generateDbQueryByRequestId() {
        return (result, parameters, queryIndex, indexStart, indexEnd) -> {
            List<InstanceV2Entity> r = new ArrayList<>();
            log.debug(
                    "findByCustomerAndRequestIds: queryIndex {} indexStart {} indexEnd {} parameters.size {}",
                    queryIndex,
                    indexStart,
                    indexEnd,
                    parameters.size());

            List<InstanceV2Entity> instances = findInstancesByRequestIds(new HashSet<>(parameters.subList(indexStart, indexEnd)));
            result.put(queryIndex, instances);
        };
    }

    /**
     * This method will update instance if already present else add instance in InstancePerZoneRepository
     **/
    private void updateInstancePerZoneRepository(@NotNull InstanceV2Entity entity) {
        instancePerZoneRepository.update(InstanceConverter.toInstanceByZoneEntity(entity));
    }

    private void deleteFromInstancePerZoneRepository(@NotNull InstanceV2Entity entity) {
        InstanceByZoneEntity instanceByZoneEntity = InstanceConverter.toInstanceByZoneEntity(entity);
        instancePerZoneRepository.delete(instanceByZoneEntity.getKey().getTruncatedTs(),
                                             instanceByZoneEntity.getKey().getZone(),
                                             instanceByZoneEntity.getKey().getInstanceId());
    }

    private void handleConflictWhileInsert(@NotNull InstanceV2Entity entity) {

        validateConflictException(entity);

        log.info("InstanceV2Repository: Resolving conflict by updating entity; occurred due to CAS error," +
                         " instanceId: {} requestId: {} instanceState: {} requestState: {}",
                 entity.getInstanceId(), entity.getRequestId(),
                 entity.getInstanceStateName().getStateName(), entity.getRequestState().toString());

        // else due to CAS error there is inconsistency in DB, update entry to resolve inconsistency
        restore(entity);
        updateInstancePerZoneRepository(entity);

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withInstanceId(entity.getInstanceId())
                                                           .withRequestId(entity.getRequestId())
                                                           .withResourceProvider(entity.getResourceProvider())
                                                           .withInstanceState(entity.getInstanceStateName().getStateName())
                                                           .withRequestState(entity.getRequestState().toString())
                                                           .withEventName(UPDATE_CONFLICT_RESOLUTION.toString())));
    }

    private boolean isEntryPresentInAllTables(
            @Nullable InstanceV2Entity instanceV2Entity) {

        // checking for instanceByIdDao
        if (instanceV2Entity != null) {

            Instant entityDay = TimeUtils.getDateFromInstant(TimeUtils.getInstantFromUuid(instanceV2Entity.getCreateTimeuuid()));

            if (instanceV2Repo.findById(instanceV2Entity.getInstanceId()).isPresent() &&
                    instanceByDayRepo.findByKeyTruncatedTsByDayAndKeyInstanceId(entityDay, instanceV2Entity.getInstanceId()).isPresent())
            {
                return true;
            }
        }

        return false;
    }

    private void logDataInconsistency(boolean loggingEnabled, @NotNull InstanceV2Entity entity) {

        if (loggingEnabled) {
            log.info("InstanceV2Repository: deleted entry for instance-id {} request-id {}",
                     entity.getInstanceId(), entity.getRequestId());
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withInstanceId(entity.getInstanceId())
                                                               .withRequestId(entity.getRequestId())
                                                               .withResourceProvider(entity.getResourceProvider())
                                                               .withZoneName(entity.getZone())
                                                               .withEventName(DELETED_INCONSISTENT_DATA.toString())));
        }
    }


    /*
   Conflict error will be due to reason:
   1. Entry is present in all tables (valid scenario)
    */
    private void validateConflictException(@NotNull InstanceV2Entity entity) {
        // 1. Entry is present in all tables (valid scenario)
        if (isEntryPresentInAllTables(entity)) {
            log.error("validateConflictException: {} customer with instanceId {} already exists",
                      entity.getCustomer(),
                      entity.getInstanceId());
            throw new IcmsConflictException(String.format("Instance with %s id already exists",
                                                         entity.getInstanceId()));
        }
    }



    private List<InstanceV2Entity> filterByCustomer(@Nullable List<InstanceV2Entity> instances, @Nullable String customer) {

        if (instances == null) {
            return new ArrayList<>();
        }

        if (!StringUtils.isEmpty(customer)) {
            instances.removeIf(r -> ! customer.equals(r.getCustomer()));
        }

        return instances;
    }
}
