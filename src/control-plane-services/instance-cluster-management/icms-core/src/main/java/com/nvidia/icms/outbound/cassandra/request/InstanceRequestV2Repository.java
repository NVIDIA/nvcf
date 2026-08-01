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

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.SliceResult;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayEntity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TimeUtils;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Consumer;
import java.util.stream.Stream;
import lombok.AllArgsConstructor;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.data.domain.Limit;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class InstanceRequestV2Repository {

    private static final String REQUEST_INFO_FETCHING_FAILED = "Failed to fetch request information from instanceRequestV2";
    private static final String REQUEST_UPDATE_FAILED = "Failed to update request information in instanceRequestV2";
    private static final String REQUEST_RESTORE_FAILED = "Failed to restore request in instanceRequestV2";
    private static final String REQUEST_DELETE_FAILED = "Failed to delete request from instanceRequestV2";
    private static final String REQUEST_INSERT_FAILED = "Failed to insert request from instanceRequestV2";

    @VisibleForTesting
    @Getter
    final InstanceRequestV2Repo instanceRequestV2Repo;

    @VisibleForTesting
    @Getter
    final InstanceRequestV2ByDayRepo instanceRequestV2ByDayRepo;

    private final TelemetryEventClient telemetryEventClient;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    @Observed
    public void insert(@NotNull InstanceRequestV2Entity entity) {

        try {
            if (entity.getCreateTimeuuid() == null) {
                entity.setCreateTimeuuid(TimeUtils.getTimeUuidNow());
            }

            if (!instanceRequestV2Repo.isInserted(entity) ||
                    !instanceRequestV2ByDayRepo.isInserted(InstanceRequestConverter.toInstanceRequest2ByDayEntity(entity)))
            {
                    log.error("{} customer with requestId {} already exists",
                              entity.getCustomer(),
                              entity.getRequestId());
                    throw new IcmsConflictException(String.format("Request with %s id already exists",
                                                                 entity.getRequestId()));
            }

            // rethrowing custom exception thrown from logic
        } catch (IcmsConflictException icmsConflictException) {
            throw icmsConflictException;

        } catch (Exception exception) {
            log.error(
                    "class:InstanceRequestV2Repository function: insert, failed to insert entry, requestId - {}, error - {}",
                    entity.getRequestId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", REQUEST_INSERT_FAILED, exception.getMessage()), exception);
        }
    }


    @Observed
    public void delete(@NotNull InstanceRequestV2ByDayEntity instanceRequestV2ByDayEntity)
    {
        instanceRequestV2ByDayEntity.setMarkedAsDeleted(true);
        instanceRequestV2ByDayRepo.update(instanceRequestV2ByDayEntity);
    }


    @Observed
    public void delete(@NotNull InstanceRequestV2Entity entity) {
        Instant entityDay = null;
        try {
            entityDay = TimeUtils.getDateFromInstant(TimeUtils.getInstantFromUuid(entity.getCreateTimeuuid()));

            // Instead of deleting records from DB we have to mark it as deleted.
            Optional<InstanceRequestV2ByDayEntity> instanceRequestV2ByDayEntity = instanceRequestV2ByDayRepo.findByKeyTruncatedTsByDayAndKeyRequestId(entityDay, entity.getRequestId());
            instanceRequestV2ByDayEntity.ifPresent(this::delete);

            instanceRequestV2Repo.deleteById(entity.getRequestId());
        } catch (Exception exception) {
            log.error(
                    "Exception occurred while deleting entries from request repository, requestId - {}, error: {} Exception:",
                    entity.getRequestId(), exception.getMessage(), exception);
            sendInstanceRequestTelemetry(exception, Events.REQUEST_DELETION_FAILED, entity.getCustomer(),
                                     entity.getRequestId());

            // Rollback logic
            restore(entity);

            log.error("InstanceRequestV2Repository delete failed for requestId - {}, error - {}",
                    entity.getRequestId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", REQUEST_DELETE_FAILED, exception.getMessage()), exception);
        }
    }


    @Observed
    public void restore(InstanceRequestV2Entity entity) {
        try {
            // Update: Update existing entry if present else insert
            instanceRequestV2Repo.update(entity);
            instanceRequestV2ByDayRepo.update(InstanceRequestConverter.toInstanceRequest2ByDayEntity(entity));
        } catch (Exception exception) {
            log.error("class:InstanceRequestV2Repository function: restore, failed to restore entries, requestId - {}, error - {}",
                    entity.getRequestId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", REQUEST_RESTORE_FAILED, exception.getMessage()), exception);
        }
    }


    @Observed
    public void update(InstanceRequestV2Entity entity) {

        try {
            instanceRequestV2Repo.update(entity);
        } catch (Exception exception) {
            log.error(
                    "class:InstanceRequestV2Repository function: update, failed to update information, requestId - {}, error - {}",
                    entity.getRequestId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", REQUEST_UPDATE_FAILED, exception.getMessage()), exception);
        }
    }

    @Observed
    public void updateRequests(List<InstanceRequestV2Entity> entities) {
        entities.forEach(this::update);
    }

    @Observed
    public Optional<InstanceRequestV2Entity> findRequestById(@Nullable String requestId) {

        try {
            if (StringUtils.isBlank(requestId)) {
                log.warn("Instance request not found, requestId is null");
                return Optional.empty();
            }

            Optional<InstanceRequestV2Entity> instanceRequestEntity =
                    instanceRequestV2Repo.findById(requestId);

            if (instanceRequestEntity.isEmpty()) {
                log.warn("Instance request not found, requestId {}", requestId);
                return Optional.empty();
            }
            return instanceRequestEntity;
        } catch (Exception exception) {
            log.error(
                    "class:InstanceRequestV2Repository function: findByRequestId, failed to fetch request info, requestId - {}, error - {}",
                    requestId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", REQUEST_INFO_FETCHING_FAILED, exception.getMessage()), exception);
        }
    }

    @Observed
    public Optional<InstanceRequestV2Entity> findRequestByIdAndCustomer(
            @NotNull String requestId,
            @Nullable String customer)  { // if it is empty or noll, then ignored)

        Optional<InstanceRequestV2Entity> instanceRequestEntity = findRequestById(requestId);

        if ( !(customer == null || customer.isEmpty()) &&  instanceRequestEntity.isPresent() && !instanceRequestEntity.get().getCustomer().equals(customer)) {
            log.info("Instance request {} does not belong to customer {}", requestId, customer);
            return Optional.empty();
        }

        return instanceRequestEntity;
    }

    @Observed
    public List<InstanceRequestV2Entity> findRequestsByDeploymentId(
            @Nullable String ncaId,
            @NotNull UUID deploymentId,
            @Nullable String customer)  {

        List<InstanceRequestV2Entity> requests = instanceRequestV2Repo.findAllByDeploymentId(deploymentId).toList();
        if (!requests.isEmpty()) {
            if (StringUtils.isNotBlank(customer)) {
                requests = requests.stream().filter(r -> customer.equals(r.getCustomer())).toList();
                if (requests.isEmpty()) {
                    log.info("NVCF Deployment {} NCA Id {} does not belong to customer {}",
                             deploymentId,
                             ncaId == null ? "null" : ncaId,
                             customer);
                    return requests;
                }
            }

            if (StringUtils.isNotBlank(ncaId)) {
                requests = requests.stream().filter(r -> ncaId.equals(r.getNcaId())).toList();
                if (requests.isEmpty()) {
                    log.info("NVCF Deployment {} does not belong to ncaid {}", deploymentId, ncaId);
                    return requests;
                }
            }
        }

        return requests;
    }



    /**
     * Fetches instance request records from C* based on list of request IDs.
     */
    @Observed
    public List<InstanceRequestV2Entity> findRequestsByIds(Set<String> requestIds) {
        List<InstanceRequestV2Entity> result = new ArrayList<>();

        if (requestIds == null || requestIds.isEmpty()) {
            return result;
        }

        requestIds.forEach(r -> {
            Optional<InstanceRequestV2Entity> requestEntity = findRequestById(r);
            requestEntity.ifPresent(result::add);
        });
        return result;
    }


    @Observed
    public List<InstanceRequestV2Entity> findRequestsByIdsAndCustomer(
            @Nullable Set<String> requestIds,
            @Nullable String customer) {

        List<InstanceRequestV2Entity> requests =  findRequestsByIds(requestIds);
        if (customer == null || customer.isEmpty()) {
            return requests;
        }

        requests.removeIf(r -> !r.getCustomer().equals(customer));

        return requests;
    }


     /**
     * Reads instance request records from C* for period of time
     * @param periodDateStart - Start of the period of time, will be truncated to days
     * @param periodDateEnd - End of the period of time, will be truncated to days
     * @return List of records for this period of time
     */
    public Set<String> findRequestIdsPerPeriod(
            Instant periodDateStart,
            Instant periodDateEnd) {

        try {
            if (periodDateStart == null || periodDateEnd == null ||
                    periodDateStart.isAfter(periodDateEnd)) {
                return new HashSet<>();
            }

            Instant currentDate = periodDateStart.truncatedTo(ChronoUnit.DAYS);
            periodDateEnd = periodDateEnd.plus(1, ChronoUnit.DAYS).truncatedTo(ChronoUnit.DAYS);

            Set<String> allRequestIds = new HashSet<>();


            while (currentDate.isBefore(periodDateEnd)) {
                List<InstanceRequestV2ByDayEntity> requestsByDay = instanceRequestV2ByDayRepo.findAllByKeyTruncatedTsByDay(currentDate, Limit.unlimited());
                requestsByDay.forEach(r -> {
                    if (!r.isMarkedAsDeleted()) {
                        allRequestIds.add(r.getKey().getRequestId());
                    }
                });
                currentDate = currentDate.plus(1, ChronoUnit.DAYS);
            }

            return allRequestIds;
        } catch (Exception exception) {
            log.error(
                    "class:InstanceRequestV2Repository function: findRequestIdsPerPeriod, failed to read entities from DB for period of time, periodDateStart - {}, periodDateEnd - {}, error - {}",
                    periodDateStart, periodDateEnd, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("%s, error: %s", REQUEST_INFO_FETCHING_FAILED,
                            exception.getMessage()), exception);
        }
    }

    @Observed
    public List<InstanceRequestV2Entity> findRequestsPerPeriod(
            Instant periodDateStart,
            Instant periodDateEnd) {
        Set<String> allRequestIds = findRequestIdsPerPeriod(periodDateStart, periodDateEnd);
        return findRequestsByIds(allRequestIds);
    }


    @Observed
    // Return requests in day range of  firstDay(now- monthOffset) <= requests <= now
    public List<InstanceRequestV2Entity> findRequestsInLastMonths(Integer monthOffset) {
        return findRequestsPerPeriod(TimeUtils.getFirstDateOfPreviousMonth(monthOffset), TimeUtils.getCurrentDate());
    }


    /**
     * Finds all request records associated with the specified `ncaId`
     *
     * @param ncaId The NCA ID to filter the request IDs. Must not be null or empty.
     * @return A list of request records  hat match the filtering criteria. Returns an empty list if
     *         the `ncaId` is null, empty, or if no matching requests are found.
     *
     * @throws IcmsInternalServerException If there is an exception while querying the database.
     *                                     Includes details about the error for telemetry purposes.
     *
     * Logging:
     * - Logs errors if there is a failure during the query execution.
     */
    public @NotNull List<InstanceRequestV2Entity> findRequestsPerNcaId(@NotNull String ncaId)
    {
        try {
            if (ncaId.isEmpty()) {
                return new ArrayList<>();
            }

            return instanceRequestV2Repo.findAllByNcaId(ncaId).toList();

        } catch (Exception exception) {
            log.error(
                    "class:InstanceRequestV2Repository function: findRequestIdsPerNcaId, failed to read entities from DB for ncaId {}, error - {}",
                    ncaId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("%s, error: %s", REQUEST_INFO_FETCHING_FAILED,
                                  exception.getMessage()), exception);
        }
    }

    /**
     * Finds all request IDs associated with the specified `ncaId` and `deploymentId` within a configured
     * time range. The range is defined as starting from the first day of the previous month (based on
     * the configuration property `cancelRequestUpToPastMonths`) up to the current date (inclusive).
     * Only requests that are not marked as deleted are included in the results.
     *
     * @param ncaId       The NCA ID to filter the request IDs. Must not be null or empty.
     * @param deploymentId The Deployment ID to filter the request IDs. Must not be null.
     * @return A set of request IDs that match the filtering criteria. Returns an empty set if
     *         the `ncaId` is empty, or if no requests are found matching the criteria.
     *
     * @throws IcmsInternalServerException If there is an exception during the query execution.
     *                                    This exception includes detailed error information for telemetry purposes.
     *
     * Logging:
     * - Errors during the query are logged with the `ncaId`, `deploymentId`, and exception details.
     */
    public List<InstanceRequestV2Entity> findRequestsPerNcaIdAndDeploymentId(@NotNull String ncaId, @NotNull UUID deploymentId)
    {
        try {
            if (ncaId.isEmpty()) {
                return new ArrayList<>();
            }

            Stream<InstanceRequestV2Entity> requests = instanceRequestV2Repo.findAllByDeploymentId(deploymentId);
            return requests.filter(r -> ncaId.equals(r.getNcaId())).toList();
        } catch (Exception exception) {
            log.error(
                    "class:InstanceRequestV2Repository function: findRequestIdsPerNcaId, failed to read entities from DB for ncaId {}, deploymentId {}, error - {}",
                    ncaId,  deploymentId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("%s, error: %s", REQUEST_INFO_FETCHING_FAILED,
                                  exception.getMessage()), exception);
        }
    }

    @Observed
    public void findAllRequestsAndApplyAction(
            @Nullable Consumer<InstanceRequestV2Entity> action,
            int pauseBetweenPagesInMs,
            int pauseBetweenRecordsInMs,
            int recordsInSlice) {

        if (action == null) {
            log.warn("findAllRequestsAndApplyAction: Action is not provided, execution skipped");
            return;
        }

        SliceResult<InstanceRequestV2Entity> entitySlice = new SliceResult<>(null, recordsInSlice > 0 ? recordsInSlice : icmsConfigurationProperties.getDatabaseReadPageSize());
        do {
            if (entitySlice.canBeRequested()) {
                entitySlice = new SliceResult<>(
                        instanceRequestV2Repo.findAll(entitySlice.generateCassandraPageRequest()),
                        entitySlice.getLimit());

                if (entitySlice.getResult() != null) {
                    for(InstanceRequestV2Entity request: entitySlice.getResult()) {
                        action.accept(request);
                        sleep(pauseBetweenRecordsInMs);
                    }
                }
            } else {
                break;
            }

            sleep(pauseBetweenPagesInMs);
        } while (entitySlice.hasNextData());
    }


    private void sleep(int timeMs) {
        if (timeMs > 0) {
            try {
                Thread.sleep(timeMs); // sleep to avoid DDoS on DB
            } catch (InterruptedException e) {
                log.error("Error of pausing the thread error: {}", e.getMessage(), e);
            }
        }

    }


    private void sendInstanceRequestTelemetry(Exception e, Events event, String customer, String requestId) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withEventName(event.toString())
                                                           .withRequestId(requestId)
                                                           .withCustomer(customer)
                                                           .withError(e.getMessage())));
    }
}
