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
package com.nvidia.icms.service;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.audit.AuditOperation;
import com.nvidia.icms.util.audit.AuditState;
import com.nvidia.icms.util.audit.AuditUtils;
import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import java.time.Instant;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_EVALUATION;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OPERATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_INSTANCE_REQUEST_TYPE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_STATE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUMMARY_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_TYPE_KEY;

@Service
@AllArgsConstructor
@Slf4j
public class CancelInstanceService {
    private final InstanceRequestV2Repository instanceRequestV2Repository;
    private TelemetryEventClient telemetryEventClient;
    private final AppAuditService auditService;
    private final InstanceServiceHelper instanceServiceHelper;

    @Observed
    public void cancelInstanceRequests(
            String customer, Set<String> requestIds,
            Map<String, Object> auditProps) {
        // 1. Get current state for each request id from database
        // 2. Cancel request as long as it is “open”
        // 3. Update request state in database

        List<InstanceRequestV2Entity> entities =
                validateCancelInstanceRequestIds(customer, requestIds);

        List<InstanceRequestV2Entity> entitiesBefore = new ArrayList<>();
        List<InstanceRequestV2Entity> entitiesAfter = new ArrayList<>();

        for (InstanceRequestV2Entity entity : entities) {
            entitiesBefore.add(AuditUtils.deepCopyInstanceRequestEntity(entity));
            entity.setAction(SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS);
            entity.setState(SpotInstanceRequestState.CANCELED);
            entity.setStatusCode(SpotRequestStatusCode.CANCELED_BEFORE_FULFILLMENT.toString());
            entity.setStatusMessage("Your instance request is canceled");
            entity.setStatusUpdateTime(Instant.now());
            entitiesAfter.add(AuditUtils.deepCopyInstanceRequestEntity(entity));
        }
        try {
            instanceRequestV2Repository.updateRequests(entities);
        } catch (Exception e) {
            log.error("failed to update state of requestIds {} in DB when cancelling.", requestIds,
                    e);
            // Rethrowing same exception as it is handled in global error handler
            throw e;
        }

        for (int i = 0; i < entitiesBefore.size(); i++) {
            populateAuditValuesForCancelInstanceRequest(auditProps,
                    entitiesBefore.get(i).getRequestId());
            auditService.sendAuditEventForInstanceRequest(auditProps, entitiesBefore.get(i),
                    entitiesAfter.get(i));
        }

        // Sending event for cancelling an instance request.
        sendRequestStateChangeEvent(entities, customer,
                Events.CANCEL_INSTANCE_REQUEST.toString());
    }

    private void populateAuditValuesForCancelInstanceRequest(
            Map<String, Object> auditProps,
            String requestId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.CANCEL_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, requestId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
        auditProps.put(AUDIT_STATE_KEY, AuditState.CANCELLED_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_SUMMARY_KEY, "Cancelled instance request with id " + requestId);
    }

    /**
     * @param entities list of instance request entities
     * @param customer owner of the instance request
     * @param eventName This function sends the event with eventName, customer, requestId and requestState
     */
    private void sendRequestStateChangeEvent(
            List<InstanceRequestV2Entity> entities, String customer,
            String eventName) {
        List<GenericMetric> genericMetricList = new ArrayList<>();
        for (InstanceRequestV2Entity entity : entities) {

            ClientRequestDataModel.LaunchSpecification launchSpecification =
                    instanceServiceHelper.getLaunchSpecificationForTelemetry(entity.getRequest());

            genericMetricList.add(
                    new GenericMetric().withEventName(eventName).withCustomer(customer)
                            .withRequestId(entity.getRequestId())
                            .withInstanceType(launchSpecification.getInstanceType())
                            .withFunctionId(launchSpecification.getFunctionId())
                            .withFunctionVersionId(launchSpecification.getVersionId())
                            .withNcaId(launchSpecification.getNcaId())
                            .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                            .withRequestState(entity.getState().toString())
                            .withDeploymentId(entity.getDeploymentId())
                            .withGpuSpecificationId(entity.getGpuSpecificationId()));
        }
        telemetryEventClient.triggerEvent(genericMetricList);
    }

    private boolean canRequestCancelled(InstanceRequestV2Entity instanceRequestEntity) {
        // A request can be cancelled only when we didn't get ACK from Cluster Agent for that request
        // 1. Request state must be "open"
        // 2. Request status must be "pending-evaluation"
        return (instanceRequestEntity.getState() == SpotInstanceRequestState.OPEN) &&
                (Objects.equals(instanceRequestEntity.getStatusCode(),
                        PENDING_EVALUATION.toString()));
    }

    @VisibleForTesting
    List<InstanceRequestV2Entity> validateCancelInstanceRequestIds(
            String customer,
            Set<String> requestIds) {

        List<InstanceRequestV2Entity> instanceRequestEntities =
                getInstanceRequestEntityListForRequestIds(customer, requestIds);

        // validate state of requestIds
        List<String> conflictingRequestIds = new ArrayList<>();
        for (InstanceRequestV2Entity instanceRequestEntity : instanceRequestEntities) {
            if (!canRequestCancelled(instanceRequestEntity)) {
                log.error(
                        "Cancellation failed for request id {}. State '{}', Status '{}'",
                        instanceRequestEntity.getRequestId(), instanceRequestEntity.getState(),
                        instanceRequestEntity.getStatusCode());
                conflictingRequestIds.add(instanceRequestEntity.getRequestId());
            }
        }
        if (!conflictingRequestIds.isEmpty()) {
            String errorMsg = String.format(
                    "Cancellation failed for following requestIds because the request state " +
                            "is not 'open' and request status is not 'pending-evaluation' - %s",
                    conflictingRequestIds);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
        return instanceRequestEntities;
    }

    /**
     * @param customer owner of request
     * @param requestIds set of requestIds
     * @return instance request entities for the given request ids
     * @throws IcmsNotFoundException if any request-id is invalid
     */
    private List<InstanceRequestV2Entity> getInstanceRequestEntityListForRequestIds(
            String customer,
            Set<String> requestIds) {
        List<InstanceRequestV2Entity> instanceRequestEntities =
                instanceRequestV2Repository.findRequestsByIdsAndCustomer(requestIds, customer);
        if (instanceRequestEntities.isEmpty()) {
            log.error("Invalid requestIds received {}", requestIds);
            throw new IcmsNotFoundException(
                    String.format("Invalid requestIds : %s", requestIds));
        }
        Set<String> requestIdsExistingInDB = new HashSet<>();
        for (InstanceRequestV2Entity instanceRequestEntity : instanceRequestEntities) {
            requestIdsExistingInDB.add(instanceRequestEntity.getRequestId());
        }
        Set<String> requestIdsReceivedFromUser = new HashSet<>(requestIds);
        requestIdsReceivedFromUser.removeAll(requestIdsExistingInDB);
        // requestIdsReceivedFromUser contains the requestIds which differ
        if (!requestIdsReceivedFromUser.isEmpty()) {
            log.error("{} RequestIds does not associated with customer {}", requestIds, customer);
            throw new IcmsNotFoundException(
                    String.format("Invalid requestIds : %s", requestIdsReceivedFromUser));
        }
        return instanceRequestEntities;
    }
}
