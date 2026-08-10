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

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.SqsMessageRepository;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.audit.AuditOperation;
import com.nvidia.icms.util.audit.AuditState;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.util.audit.AuditUtils;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_FULFILLMENT;
import static com.nvidia.icms.scheduled.CancelRequestEventController.CANCEL_LINGERING_REQUEST_JOB_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.MESSAGE_BATCH_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.PREVIOUS_REQUEST_STATE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.PREVIOUS_REQUEST_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REQUEST_STATUS;
import static com.nvidia.icms.util.InstanceServiceUtil.getStringValue;
import static com.nvidia.icms.util.InstanceServiceUtil.isModelCacheEnabled;
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
public class CancelRequestEventService {

    // 1. Get requests which are 4 days older
    // 2. Find request which have the state as OPEN and status code as:
    //      i. PENDING_EVALUATION
    //     ii. PENDING_FULFILLMENT - Before cancelling PENDING_FULFILLMENT request we need to check
    //                               if there is any instance allocated to this request.
    // 3. Update these request with status as “schedule-expired” and state “canceled”

    public static final String NO_CAPACITY_USER_VISIBLE_MSG = "Backend '%s', GPU '%s', "
            + "Instance Type '%s': Capacity not available at this time - Exceeded max queued duration '%d' minutes";

    public static final String UNABLE_TO_FULFILL_REQUEST_USER_VISIBLE_MSG =
            "Backend '%s', GPU '%s',"
                    + " Instance Type '%s': Failed to create instances in the stipulated '%d' minutes";

    private static final String OPEN_REQUEST_COUNT = "OpenRequestCount";
    private static final String ACTIVE_REQUEST_COUNT = "ActiveRequestCount";
    private static final String CLOSED_REQUEST_COUNT = "ClosedRequestCount";
    private static final String CANCELED_REQUEST_COUNT = "CanceledRequestCount";

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final InstanceRequestV2Repository instanceRequestV2Repository;

    private final InstanceV2Repository instanceV2Repository;

    private final AppAuditService auditService;

    private final TelemetryEventClient telemetryEventClient;

    private final ObjectMapper objectMapper;

    private final CloudHealthRepository cloudHealthRepository;

    private final SqsMessageRepository sqsMessageRepository;

    private final InstanceServiceHelper instanceServiceHelper;

    private final ComputePlatformService computePlatformService;

    public void execute() {

        List<InstanceRequestV2Entity> olderInstanceRequestEntities = findCandidateRequestsForCancelling();
        Map<String, Set<String>> mapOfCustomerIdVsRequestIds = new HashMap<>();
        Set<String> healthyZones = getHealthyZones();
        Map<String, Integer> requestStateCount = new HashMap<>();

        for (InstanceRequestV2Entity requestEntity : olderInstanceRequestEntities) {
            updateRequestStateCount(requestEntity, requestStateCount);

            long requestExpirationInMinutes = icmsConfigurationProperties.getRequestCancelDurationInMin();
            if (StringUtils.isNotBlank(getStringValue(requestEntity.getTaskId()))) {
                requestExpirationInMinutes = Duration.parse(requestEntity.getMaxQueuedDuration()).toMinutes();
            }

            if (requestEntity.getState() == SpotInstanceRequestState.OPEN &&
                    isRequestExpired(requestEntity, requestExpirationInMinutes)) {

                /*
                  Mark request canceled for:
                    1. Request-state: open request-status: pending-evaluation
                    2. Request-state: open request-status: pending-fulfillment
                        a) If ackedInstances are not populated & no instances created
                 */

                // 1. Request-state: open request-status: pending-evaluation
                if (requestEntity.getStatusCode()
                        .equals(SpotRequestStatusCode.PENDING_EVALUATION.toString())) {
                    cancelAndUpdateRequest(requestEntity, requestExpirationInMinutes);

                    // 2. Request-state: open request-status: pending-fulfillment
                } else if (requestEntity.getStatusCode()
                        .equals(SpotRequestStatusCode.PENDING_FULFILLMENT.toString())) {

                    // Optimize this flow with NVCFSPOT-1758
                    if (anyInstanceCreatedForRequest(requestEntity,
                                                     healthyZones,
                                                     mapOfCustomerIdVsRequestIds)) {
                        continue;
                    }
                    cancelAndUpdateRequest(requestEntity, requestExpirationInMinutes);
                }
            }
        }

        // Logging request state count
        logRequestStateCount(requestStateCount);
    }

    /**
     * Checks if request is expired or not
     *
     * @return TRUE if request is expired
     */
    private boolean isRequestExpired(InstanceRequestV2Entity requestEntity, long expirationTimeInMinutes) {
        return TimeUtils.getInstantFromUuid(requestEntity.getCreateTimeuuid()).isBefore(
                Instant.now().minus(expirationTimeInMinutes, ChronoUnit.MINUTES));
    }

    private List<InstanceRequestV2Entity> findCandidateRequestsForCancelling() {
        return instanceRequestV2Repository.findRequestsInLastMonths(icmsConfigurationProperties.getCancelRequestUpToPastMonths());
    }

    private void cancelAndUpdateRequest(InstanceRequestV2Entity requestEntity, long requestExpirationInMinutes) {
        String previousStatusCode = requestEntity.getStatusCode();
        String previousState = requestEntity.getState().toString();

        log.debug("Canceled lingering request with id {}, status {} and state {} and batchWiseCheck {}",
                requestEntity.getRequestId(), previousStatusCode,
                previousState, requestEntity.getCheckBatchwiseInfo());

        final InstanceRequestV2Entity entityBefore = AuditUtils.deepCopyInstanceRequestEntity(requestEntity);
        requestEntity.setState(SpotInstanceRequestState.CANCELED);
        setCanceledRequestStatusMessage(requestEntity, requestExpirationInMinutes);
        requestEntity.setStatusCode(SpotRequestStatusCode.SCHEDULE_EXPIRED.toString());
        requestEntity.setStatusUpdateTime(Instant.now());
        try {
            instanceRequestV2Repository.update(requestEntity);
        } catch (Exception e) {
            log.error("Failed to update canceled request in database with request id {}",
                      requestEntity.getRequestId());
            throw e;
        }
        Map<String, Object> auditProps = new HashMap<>();
        populateAuditValuesForCancelInstanceRequest(auditProps, requestEntity.getRequestId());

        auditService.sendAuditEventForInstanceRequest(auditProps, entityBefore, requestEntity);
        Map<String, Object> metadata = new HashMap<>();
        metadata.put(MESSAGE_BATCH_STATUS.getName(), requestEntity.getCheckBatchwiseInfo());
        metadata.put(REQUEST_STATUS.getName(), requestEntity.getStatusCode());
        metadata.put(PREVIOUS_REQUEST_STATUS.getName(), previousStatusCode);
        metadata.put(PREVIOUS_REQUEST_STATE.getName(), previousState);

        ClientRequestDataModel.LaunchSpecification launchSpecification =
                instanceServiceHelper.getLaunchSpecificationForTelemetry(requestEntity.getRequest());

        // Add task details in metadata
        instanceServiceHelper.updateMetadataForTask(metadata, requestEntity, launchSpecification);

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withEventName(Events.ASYNC_TASK_CANCEL_INSTANCE_REQUEST.toString())
                .withResourceProvider(requestEntity.getResourceProvider())
                .withRequestId(requestEntity.getRequestId())
                .withCustomer(requestEntity.getCustomer())
                .withMetadata(metadata)
                .withNcaId(launchSpecification.getNcaId())
                .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                .withFunctionId(launchSpecification.getFunctionId())
                .withFunctionVersionId(launchSpecification.getVersionId())
                .withInstanceType(launchSpecification.getInstanceType())
                .withTaskId(getStringValue(requestEntity.getTaskId()))
                .withRequestState(requestEntity.getState().toString())
                .withDeploymentId(requestEntity.getDeploymentId())
                .withGpuSpecificationId(requestEntity.getGpuSpecificationId())));
    }

    private void setCanceledRequestStatusMessage(InstanceRequestV2Entity requestEntity, long requestExpirationInMinutes) {
        String instanceType;
        String backend;
        String gpu;
        try {
            ClientRequestDataModel requestData;
            requestData = objectMapper.readValue(requestEntity.getRequest(),
                                       ClientRequestDataModel.class);
            instanceType = requestData.getLaunchSpecification().getInstanceType();
            backend = requestData.getLaunchSpecification().getBackend();
            gpu = requestData.getLaunchSpecification().getGpu();
        } catch (Exception e) {
            String errMsg = String.format("Failed to parse request information, error: %s", e.getMessage());
            log.error("error: {}, exception: ", errMsg, e);
            throw new IcmsInternalServerException(errMsg, e);
        }
        if (requestEntity.getStatusCode().equals(SpotRequestStatusCode.PENDING_EVALUATION.toString())) {
            requestEntity.setStatusMessage(
                    NO_CAPACITY_USER_VISIBLE_MSG.formatted(backend,
                                                           gpu,
                                                           instanceType,
                                                           requestExpirationInMinutes));
        } else {
            requestEntity.setStatusMessage(
                    UNABLE_TO_FULFILL_REQUEST_USER_VISIBLE_MSG.formatted(backend,
                                                                         gpu,
                                                                         instanceType,
                                                                         requestExpirationInMinutes));
        }
    }

    private void populateAuditValuesForCancelInstanceRequest(
            Map<String, Object> auditProps,
            String requestId) {

        auditProps.put(AUDIT_ACTOR_ID_KEY, CANCEL_LINGERING_REQUEST_JOB_NAME);
        auditProps.put(AUDIT_SUBJECT_ID_KEY, CANCEL_LINGERING_REQUEST_JOB_NAME);
        auditProps.put(AUDIT_ACTOR_LOCATION_KEY, AUDIT_ACTOR_LOCATION_ASYNC_OPERATION);
        auditProps.put(AUDIT_SUBJECT_LOCATION_KEY, AUDIT_SUBJECT_LOCATION_ASYNC_OPERATION);

        auditProps.put(AUDIT_OPERATION_KEY,
                       AuditOperation.CANCEL_PENDING_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, requestId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
        auditProps.put(AUDIT_STATE_KEY, AuditState.CANCELLED_PENDING_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_SUMMARY_KEY,
                       "Cancelled pending instance request with id " + requestId);
    }

    /**
     * Function to fetch allInstanceIds by Customer
     *
     * @return Set of request ids that have instances associated for given customer
     */
    private Set<String> getListOfRequestIdsHavingInstancesAssociated(String customer) {

        List<InstanceV2Entity> instancesInfoForGivenCustomer =
                instanceV2Repository.findAllByCustomer(customer);

        Set<String> requestIdsHavingInstances = new HashSet<>();
        instancesInfoForGivenCustomer.forEach(instanceEntity -> {
            requestIdsHavingInstances.add(instanceEntity.getRequestId());
        });
        return requestIdsHavingInstances;
    }

    private boolean anyInstanceCreatedForRequest(
            InstanceRequestV2Entity instanceRequestEntity,
            Set<String> healthyZones,
            Map<String, Set<String>> mapOfCustomerIdVsRequestIds) {

        /*
        If request having reported instances then, we will not cancel that request
        If request is not having reported instances but have acked instances, we will not cancel that request
        If request is not having reported instances and not having acked instances as well, we will cancel that request
         */
        return areInstancesAssociatedToRequest(instanceRequestEntity, mapOfCustomerIdVsRequestIds) ||
                isAcknowledgedInstancesPresent(instanceRequestEntity, healthyZones);
    }

    private boolean areInstancesAssociatedToRequest(
            InstanceRequestV2Entity requestEntity,
            Map<String, Set<String>> mapOfCustomerIdVsRequestIds) {
        String customerId = requestEntity.getCustomer();
        if (!mapOfCustomerIdVsRequestIds.containsKey(customerId)) {
            Set<String> requestIdsHavingAnInstanceAssociated =
                    getListOfRequestIdsHavingInstancesAssociated(customerId);
            mapOfCustomerIdVsRequestIds.put(customerId,
                                            requestIdsHavingAnInstanceAssociated);
        }
        Set<String> requestIdsHavingAnInstanceAssociated =
                mapOfCustomerIdVsRequestIds.get(customerId);

        boolean areInstancesPresent =
                requestIdsHavingAnInstanceAssociated.contains(requestEntity.getRequestId());
        if (!areInstancesPresent) {
            log.debug("CancelRequestEventService: request-id {} instances are not created hence considering request for cancellation",
                    requestEntity.getRequestId());
        }
        return areInstancesPresent;
    }

    // Acknowledged instances will be considered based on health of that zone
    private boolean isAcknowledgedInstancesPresent(
            InstanceRequestV2Entity instanceRequestEntity,
            Set<String> healthyZones) {

        if (instanceRequestEntity.getCheckBatchwiseInfo() != null &&
                instanceRequestEntity.getCheckBatchwiseInfo()) {
            List<SqsMessageEntity> sqsMessageEntities =
                    sqsMessageRepository.findByRequestId(instanceRequestEntity.getRequestId());

            int nonByocAckedInstances = 0;
            int byocAckInstances = 0;
            ClientRequestDataModel clientData = instanceServiceHelper.parseRequestInfo(instanceRequestEntity.getRequest());
            boolean isModelCacheEnabled = isModelCacheEnabled(clientData.getLaunchSpecification().getModelCacheEnabled());
            int requestCancelDurationInMinForByoc = getByocValidationDurationWithoutModel();
            // If the request uses model caching then the request cancellation time will be 4 hrs in byoc flow
            if (isModelCacheEnabled) {
                requestCancelDurationInMinForByoc = getByocValidationDurationWithModel();
            }

            for (SqsMessageEntity sqsMessageEntity : sqsMessageEntities) {
                if (healthyZones.contains(sqsMessageEntity.getZone()) &&
                        sqsMessageEntity.getStatus() == PENDING_FULFILLMENT) {

                    if (computePlatformService.isComputePlatformProvider(sqsMessageEntity.getCloudProvider(), instanceRequestEntity.getResourceProvider()) &&
                            !isNonByocMessageBatchIdExpired(instanceRequestEntity.getResourceProvider(),
                                                        sqsMessageEntity)) {
                        nonByocAckedInstances += sqsMessageEntity.getAcknowledgedInstances();
                    } else if (!computePlatformService.isComputePlatformProvider(sqsMessageEntity.getCloudProvider(),
                                              instanceRequestEntity.getResourceProvider()) &&
                            !isByocMessageBatchIdExpired(instanceRequestEntity.getResourceProvider(),
                                                           sqsMessageEntity, requestCancelDurationInMinForByoc)) {
                        byocAckInstances += sqsMessageEntity.getAcknowledgedInstances();
                    } else {
                        log.debug("CancelRequestEventService: For request-id {} message-batch-id {} zone {}" +
                                        " waiting time for {} cloud provider passed. not considering {} acked instances",
                                sqsMessageEntity.getKey().getRequestId(),
                                sqsMessageEntity.getKey().getMessageBatchId(), sqsMessageEntity.getZone(),
                                sqsMessageEntity.getCloudProvider(),
                                sqsMessageEntity.getAcknowledgedInstances());
                    }
                }
            }
            log.debug("CancelRequestEventService: for request-id {} found {} non-byoc acked and {} byoc acked instances",
                     instanceRequestEntity.getRequestId(),
                     nonByocAckedInstances, byocAckInstances);
            return (nonByocAckedInstances + byocAckInstances) > 0;
        } else {
            log.debug(
                    "CancelRequestEventService: for request-id {} checkBatchwiseInfo {} hence ignoring acked instances",
                    instanceRequestEntity.getRequestId(), instanceRequestEntity.getCheckBatchwiseInfo());
        }

        return false;
    }

    private Set<String> getHealthyZones() {
        return cloudHealthRepository.finalAllHealthyZones();
    }

    private boolean isNonByocMessageBatchIdExpired(ResourceProvider resourceProvider,
                                               SqsMessageEntity sqsMessageEntity) {

        return isNonByocMessageBatchIdValidationEnabled() &&
                computePlatformService.isComputePlatformProvider(resourceProvider) && sqsMessageEntity.getCreationTime()
                .isBefore(Instant.now().minus(getNonByocValidationDuration(), ChronoUnit.MINUTES));
    }

    private boolean isNonByocMessageBatchIdValidationEnabled() {
        return icmsConfigurationProperties.getMessageBatchIdConfig()
                .isCancelRequestValidationEnabled();
    }

    private int getNonByocValidationDuration() {
        return icmsConfigurationProperties.getMessageBatchIdConfig()
                .getValidationDurationInMin();
    }

    private int getByocValidationDurationWithModel() {
        return icmsConfigurationProperties.getMessageBatchIdConfig()
                .getValidationDurationForByocWithModelInMin();
    }

    private int getByocValidationDurationWithoutModel() {
        return icmsConfigurationProperties.getMessageBatchIdConfig()
                .getValidationDurationForByocWithoutModelInMin();
    }

    private void logRequestStateCount(Map<String, Integer> requestStateCount) {
        log.debug("{} job: {} {} {} {} {} {} {} {}", CANCEL_LINGERING_REQUEST_JOB_NAME,
                OPEN_REQUEST_COUNT, requestStateCount.getOrDefault(OPEN_REQUEST_COUNT, 0),
                ACTIVE_REQUEST_COUNT, requestStateCount.getOrDefault(ACTIVE_REQUEST_COUNT, 0),
                CLOSED_REQUEST_COUNT, requestStateCount.getOrDefault(CLOSED_REQUEST_COUNT, 0),
                CANCELED_REQUEST_COUNT, requestStateCount.getOrDefault(CANCELED_REQUEST_COUNT, 0));
    }

    private void updateRequestStateCount(InstanceRequestV2Entity instanceRequestEntity,
                                         Map<String, Integer> requestStateCount) {
        switch (instanceRequestEntity.getState()) {
            case OPEN -> incrementValueInMap(OPEN_REQUEST_COUNT, requestStateCount);
            case ACTIVE -> incrementValueInMap(ACTIVE_REQUEST_COUNT, requestStateCount);
            case CLOSED -> incrementValueInMap(CLOSED_REQUEST_COUNT, requestStateCount);
            case CANCELED -> incrementValueInMap(CANCELED_REQUEST_COUNT, requestStateCount);
        }
    }

    private void incrementValueInMap(String key, Map<String, Integer> requestStateCount) {
        requestStateCount.put(key, requestStateCount.getOrDefault(key, 0) + 1);
    }

    private boolean isByocMessageBatchIdExpired(ResourceProvider resourceProvider,
                                                  SqsMessageEntity sqsMessageEntity,
                                                  int requestCancelDurationInMin) {

        return icmsConfigurationProperties.isMessageBatchIdExpiryValidationInGet() &&
                resourceProvider.equals(ResourceProvider.BYOC) && sqsMessageEntity.getCreationTime()
                .isBefore(Instant.now().minus(requestCancelDurationInMin, ChronoUnit.MINUTES));
    }
}
