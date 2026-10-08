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
package com.nvidia.icms.service.scheduled.request;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.DbQueryExecutorService;
import com.nvidia.icms.util.audit.AuditOperation;
import com.nvidia.icms.util.audit.AuditState;
import com.nvidia.icms.util.audit.AuditUtils;
import jakarta.validation.constraints.NotNull;
import com.nvidia.icms.service.workers.WorkerIdentifierService;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState.ACTIVE;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState.CANCELED;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState.CLOSED;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState.OPEN;
import static com.nvidia.icms.scheduled.StaleRequestDeletionTaskController.STALE_REQUEST_DELETION_JOB_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.ACTION;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLOSED_REQUESTS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLOSED_REQUEST_COUNT;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.DELETED_INSTANCES;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.DELETED_INSTANCE_COUNT;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.DELETED_REQUESTS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.DELETED_REQUEST_COUNT;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_LOCATION_ASYNC_OPERATION;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_ACTOR_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OPERATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_INSTANCE_REQUEST_TYPE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_STATE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUMMARY_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_TYPE_KEY;

@Slf4j
@Service
@AllArgsConstructor
public class StaleRequestDeletionTask {
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final InstanceRequestV2Repository instanceRequestV2Repository;

    private final InstanceV2Repository instanceV2Repository;

    private final AppAuditService auditService;

    private final TelemetryEventClient telemetryEventClient;

    private final InstanceServiceHelper instanceServiceHelper;

    private final DbQueryExecutorService dbQueryExecutorService;

    private final WorkerIdentifierService workerIdentifierService;

    public void execute() {

        if (!icmsConfigurationProperties.isStateRequestDeletionTaskEnabled()) {
            log.info("Job: {} is not enabled existing the scheduled task",
                     STALE_REQUEST_DELETION_JOB_NAME);
            return;
        }
        log.info("job: {}, Starting execution", STALE_REQUEST_DELETION_JOB_NAME);

        sendStartedTelemetryEvent();

        Set<String> deletedInstances = new HashSet<>();
        Set<String> deletedRequest = new HashSet<>();
        Set<String> closedRequests = new HashSet<>();

        try {
            instanceRequestV2Repository.findAllRequestsAndApplyAction(
                    request -> performOperationPerRequest(request,
                                                          deletedInstances,
                                                          deletedRequest,
                                                          closedRequests),
                    icmsConfigurationProperties.getStaleRequestPauseBetweenPagesInMs(),
                    icmsConfigurationProperties.getStaleRequestPauseBetweenRecordsInMs(),
                    icmsConfigurationProperties.getStaleRequestRecordsInDbPage());

            sendCompletionEvent(deletedInstances, deletedRequest, closedRequests, "COMPLETED");

            log.info("job: {}, completed", STALE_REQUEST_DELETION_JOB_NAME);

        } catch (Exception e) {
            log.error("Job: {} failed with error - {}", STALE_REQUEST_DELETION_JOB_NAME,
                      e.getMessage());

            // Sending all the data processed before failure
            sendCompletionEvent(deletedInstances, deletedRequest, closedRequests, "FAILED");

            log.error("job: {}, Error during job execution: {}", STALE_REQUEST_DELETION_JOB_NAME,
                      e.getMessage(), e);
        }
    }

    private void performOperationPerRequest(
            @NotNull InstanceRequestV2Entity request,
            @NotNull Set<String> deletedInstances,
            @NotNull Set<String> deletedRequest,
            @NotNull Set<String> closedRequests) {

        // Handle for CLOSED request
        if (isTerminalState(request.getState())) {
            handleTerminalStateRequests(request, deletedInstances, deletedRequest);
        } else if (request.getState().equals(OPEN) || request.getState().equals(ACTIVE)) {
            handleOpenOrActiveRequests(request, closedRequests);
        }
    }


    /**
     * If request is CLOSED/CANCELED before 2 days and all its created instances(until request was open) are terminated then we should delete request and instances
     * No new instance can be reported once request is marked as closed/canceled
     *
     * @param instanceRequestEntity        {@link InstanceRequestV2Entity}
     * @param deletedInstances Set to store deleted instances
     * @param deletedRequest Set to store deleted requests
     */
    // 7. If request is closed and all instances terminated; delete entries
    // 7.1 If request is canceled ideally it should not have any instances but due to race condition (SIS-1090),
    //     request can be canceled even after having instances; delete entries
    private void handleTerminalStateRequests(
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull Set<String> deletedInstances,
            @NotNull Set<String> deletedRequest) {

        // Validating if request is closed 2 days ago
        if (canClosedRequestDeleted(instanceRequestEntity.getStatusUpdateTime())) {

            List<InstanceV2Entity> instanceEntityList =
                instanceV2Repository.findInstancesByRequestId(instanceRequestEntity.getRequestId());

            // Validating if all CREATED instances are terminated
            if (areAllCreatedInstancesTerminated(instanceEntityList)) {

                // Delete Instances
                instanceEntityList.forEach(entity -> {
                    instanceV2Repository.delete(entity, false);
                    deletedInstances.add(entity.getInstanceId());
                });

                // Delete Request
                workerIdentifierService.deleteForRequestQuietly(instanceRequestEntity.getRequestId());
                instanceRequestV2Repository.delete(instanceRequestEntity);
                deletedRequest.add(instanceRequestEntity.getRequestId());

            } else {
                // This will happen only when cluster didn't report terminated state cloud be because cluster if offline
                log.info(
                        "job: {} requestId {} is closed 2 days ago but ALL instances are not marked as terminated",
                        STALE_REQUEST_DELETION_JOB_NAME, instanceRequestEntity.getRequestId());
            }
        }
    }

    /**
     * If a request is OPEN or ACTIVE and all REQUESTED instances are terminated by Service then
     * we should close such request. These requests will be deleted after 2 days
     *
     * @param instanceRequestEntity        {@link InstanceRequestV2Entity}
     * @param closedRequests Set to store closed requests
     */

    // 8. If request is open/active and all requested instances are terminated; mark request closed
    private void handleOpenOrActiveRequests(
            InstanceRequestV2Entity instanceRequestEntity, Set<String> closedRequests) {

        if (areAllRequestedInstancesTerminated(instanceRequestEntity)) {
            // Updating request to CLOSED
            InstanceRequestV2Entity entityBefore =
                    AuditUtils.deepCopyInstanceRequestEntity(instanceRequestEntity);
            updateRequestStateToClosed(instanceRequestEntity);

            instanceRequestV2Repository.update(instanceRequestEntity);
            workerIdentifierService.deleteForRequestQuietly(instanceRequestEntity.getRequestId());

            sendAuditForTerminatedRequest(entityBefore, instanceRequestEntity);
            closedRequests.add(instanceRequestEntity.getRequestId());
        }
    }

    private void updateRequestStateToClosed(InstanceRequestV2Entity instanceRequestEntity) {
        instanceRequestEntity.setAction(SpotInstanceRequestAction.TERMINATE_SPOT_INSTANCE_REQUEST);
        instanceRequestEntity.setState(CLOSED);
        instanceRequestEntity.setStatusCode(
                SpotRequestStatusCode.REQUEST_TERMINATED_BY_SERVICE.toString());
        instanceRequestEntity.setStatusMessage(
                "All instances are terminated hence closing the request");
        instanceRequestEntity.setStatusUpdateTime(Instant.now());
    }

    private void sendAuditForTerminatedRequest(
            InstanceRequestV2Entity entityBefore, InstanceRequestV2Entity entityAfter) {
        Map<String, Object> auditProps = new HashMap<>();
        auditProps.put(AUDIT_ACTOR_ID_KEY, STALE_REQUEST_DELETION_JOB_NAME);
        auditProps.put(AUDIT_SUBJECT_ID_KEY, STALE_REQUEST_DELETION_JOB_NAME);
        auditProps.put(AUDIT_ACTOR_LOCATION_KEY, AUDIT_ACTOR_LOCATION_ASYNC_OPERATION);
        auditProps.put(AUDIT_SUBJECT_LOCATION_KEY, AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION);

        auditProps.put(AUDIT_OPERATION_KEY,
                       AuditOperation.TERMINATE_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, entityAfter.getRequestId());
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
        auditProps.put(AUDIT_STATE_KEY, AuditState.TERMINATED_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_SUMMARY_KEY,
                       "All instances are terminated hence closing the request " +
                               entityAfter.getRequestId());
        auditService.sendAuditEventForInstanceRequest(auditProps, entityBefore, entityAfter);
    }

    private boolean areAllCreatedInstancesTerminated(
            List<InstanceV2Entity> instanceEntityList) {
        for (InstanceV2Entity entity : instanceEntityList) {
            if (!entity.getInstanceStateName().equals(SpotInstanceInternalState.TERMINATED)) {
                return false;
            }
        }
        return true;
    }

    private boolean areAllRequestedInstancesTerminated(@NotNull InstanceRequestV2Entity instanceRequestEntity) {

        List<InstanceV2Entity> instancesInRequest =  instanceV2Repository.findInstancesByRequestId(instanceRequestEntity.getRequestId());
        int terminatedInstanceCount = instancesInRequest.stream().filter(entity -> entity.getInstanceStateName()
                        .equals(SpotInstanceInternalState.TERMINATED))
                .toList().size();

        if (canRequestBeTerminatedDueToAge(instanceRequestEntity.getStatusUpdateTime())) {
            boolean canBeTerminated = instancesInRequest.size() == terminatedInstanceCount;
            log.info(
                    "RequestId {}: Validating if request can be terminated due to age. Can be terminated {}. Instances created {}. Instances terminated {}",
                    instanceRequestEntity.getRequestId(),
                    canBeTerminated,
                    instancesInRequest.size(),
                    terminatedInstanceCount);
            // If request was update last time at least 5 days (default) ago check if all created instances for this requests are terminated
            return canBeTerminated;
        }
        else {
            // if request still can have new instances created, check if  all of them are created and terminated

            boolean canBeTerminated = terminatedInstanceCount == getInstanceCount(instanceRequestEntity);
            log.info(
                    "RequestId {}: Validating if request can be terminated because all instances are terminated. Can be terminated {}. Instances asked {}. Instances terminated {}",
                    instanceRequestEntity.getRequestId(),
                    canBeTerminated,
                    getInstanceCount(instanceRequestEntity),
                    terminatedInstanceCount);

            return canBeTerminated;
        }
    }

    private int getInstanceCount(InstanceRequestV2Entity instanceRequestEntity) {
        int instanceCount = instanceRequestEntity.getInstanceCount();

        // "instanceCount" field was added later to the request entity.
        // For older entries we have to fetch it from "request"
        if (instanceCount == 0) {
            return instanceServiceHelper.parseRequestInfo(instanceRequestEntity.getRequest())
                    .getInstanceCount();
        }
        return instanceCount;
    }

    // If request updateTime is more than configuration ClosedRequestInstanceDeletionDays then it can be deleted
    private boolean canClosedRequestDeleted(Instant updateTime) {
        if (updateTime == null) {
            throw new IllegalArgumentException("updateTime cannot be null");
        }
        long daysBetween = ChronoUnit.DAYS.between(updateTime, Instant.now());
        return daysBetween > icmsConfigurationProperties.getClosedRequestInstanceDeletionDays();
    }


    private boolean canRequestBeTerminatedDueToAge(Instant updateTime) {
        if (updateTime == null) {
            throw new IllegalArgumentException("updateTime cannot be null");
        }
        long daysBetween = ChronoUnit.DAYS.between(updateTime, Instant.now());
        return daysBetween > icmsConfigurationProperties.getWaitForInstancesToBeCreatedInDays();
    }

    private void sendCompletionEvent(Set<String> deletedInstances, Set<String> deletedRequest,
                                     Set<String> closedRequests, String status) {
        sendCompletionTelemetryEvent(deletedInstances.size(), deletedRequest.size(),
                                     closedRequests.size(), status);

        List<String> partitionedDeletedInstancesList = convertSetToStringList(deletedInstances);
        List<String> partitionedDeletedRequestsList = convertSetToStringList(deletedRequest);
        List<String> partitionedClosedRequestsList = convertSetToStringList(closedRequests);

        sendStaleDataDeletionEvent(DELETED_REQUESTS.getName(), partitionedDeletedRequestsList);
        sendStaleDataDeletionEvent(DELETED_INSTANCES.getName(), partitionedDeletedInstancesList);
        sendStaleDataDeletionEvent(CLOSED_REQUESTS.getName(), partitionedClosedRequestsList);
    }


    private void sendCompletionTelemetryEvent(int deletedInstanceCount,
                                              int deletedRequestCount,
                                              int closedRequestCount,
                                              String status) {

        try {
            Map<String, Object> metadata = new HashMap<>();
            metadata.put("STATUS", status);
            metadata.put(DELETED_REQUEST_COUNT.getName(), deletedRequestCount);
            metadata.put(DELETED_INSTANCE_COUNT.getName(), deletedInstanceCount);
            metadata.put(CLOSED_REQUEST_COUNT.getName(), closedRequestCount);

            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                    .withMetadata(metadata)
                    .withEventName(Events.STATE_REQUEST_DELETION_EVENT.toString())));


        } catch (Exception exception) {
            log.error("Job: {} failed to send telemetry event, error {} exception:",
                    STALE_REQUEST_DELETION_JOB_NAME, exception.getMessage(), exception);
            throw exception;
        }
    }

    private void sendStartedTelemetryEvent() {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withMetadata(
                                                                   Map.of("STATUS", "STARTED"))
                                                           .withEventName(
                                                                   Events.STATE_REQUEST_DELETION_EVENT.toString())));
    }


    private void sendStaleDataDeletionEvent(String action, List<String> deletedEntries) {

        try {

            for (String deletedEntry : deletedEntries) {
                List<GenericMetric> genericMetricList = new ArrayList<>();
                Map<String, Object> metadata = new HashMap<>();
                metadata.put(ACTION.getName(), action);
                metadata.put(action, deletedEntry);
                GenericMetric genericMetric = new GenericMetric()
                        .withMetadata(metadata)
                        .withEventName(Events.STALE_DATA_DELETION.toString());
                genericMetricList.add(genericMetric);
                telemetryEventClient.triggerEvent(genericMetricList);
            }

        } catch (Exception exception) {
            log.error("Job: {} failed to send telemetry event, error {} exception:",
                    STALE_REQUEST_DELETION_JOB_NAME, exception.getMessage(), exception);
            throw exception;
        }
    }

    private List<String> convertSetToStringList(Set<String> stringSet) {
        List<String> partitionedList = new ArrayList<>();
        StringBuilder stringBuilder = new StringBuilder();
        int count = 0;
        int maxPartitionSize = icmsConfigurationProperties.getStaleRequestsTelemetryEventSize();

        for (String s : stringSet) {
            if (count > 0) {
                stringBuilder.append(",");
            }
            stringBuilder.append(s);
            count++;

            // Creating partition of 300 request-ids/instance-ids
            if (count == maxPartitionSize) {
                partitionedList.add(stringBuilder.toString());
                // Reset the StringBuilder
                stringBuilder.setLength(0);
                count = 0;
            }
        }

        // Adding last string
        if (!stringBuilder.isEmpty()) {
            partitionedList.add(stringBuilder.toString());
        }

        return partitionedList;
    }

    private boolean isTerminalState(SpotInstanceRequestState state) {
        return state.equals(CLOSED) || state.equals(CANCELED);
    }
}
