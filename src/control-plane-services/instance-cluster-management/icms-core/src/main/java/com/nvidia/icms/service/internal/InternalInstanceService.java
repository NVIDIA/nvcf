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
package com.nvidia.icms.service.internal;

import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getClusterIdFromAuthClientId;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.MESSAGE_BATCH_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REQUEST_BODY;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REQUEST_STATUS;
import static com.nvidia.icms.service.telemetry.model.Events.CANNOT_FULFILL_STATE_UPDATE;
import static com.nvidia.icms.service.telemetry.model.Events.SCHEDULE_EXPIRED_STATE_UPDATE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OPERATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_INSTANCE_REQUEST_TYPE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_STATE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUMMARY_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_TYPE_KEY;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest.InstancePlacement;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.SqsMessageRepository;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageKey;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.ByocService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.internal.InternalInstanceServiceHelper.InstancePlacementValidationResponse;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.util.audit.AuditOperation;
import com.nvidia.icms.util.audit.AuditState;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.util.audit.AuditUtils;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class InternalInstanceService {
    public static final String MSG_REQUEST_FULFILLED =
            "Already fulfilled requested capacity of {} for request id {}. " +
                    "Throwing PreconditionFailed error as a respond for ACK.";
    public static final String MSG_ALREADY_FULFILLED = "The request is %s is already fulfilled.";

    public static final String MSG_ALREADY_ACK = "The %s messageBatchId for %s request is already acknowledged.";

    private final InstanceRequestV2Repository instanceRequestV2Repository;

    private final TelemetryEventClient telemetryEventClient;

    private final AppAuditService auditService;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final InstanceUpdateService instanceUpdateService;

    private final ByocService byocService;

    private final SqsMessageRepository sqsMessageRepository;

    private final InternalInstanceServiceHelper internalInstanceServiceHelper;

    private final InstanceServiceHelper instanceServiceHelper;

    private final ReservationCapacityValidationHelper reservationCapacityValidationHelper;

    private final ComputePlatformService computePlatformService;

    public void updateInstanceStatus(
            @Nullable String instanceRequestId,
            @NotNull String instanceId,
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull String clientId,
            @NotNull Map<String, Object> auditProps) {

        instanceUpdateService.updateInstanceStatus(instanceRequestId, instanceId,
                                                       instanceStatusUpdateRequest, clientId,
                                                       auditProps);
    }

    @Observed
    public void updateInstanceRequestStatus(
            @NotNull String clientId,
            @Nullable String requestId,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @NotNull Map<String, Object> auditProps) {

        logIncomingRequest(requestId, clientId, updateRequest, "Initial request");

        validateCapacityType(updateRequest);

        // 1. "requestId" must be valid
        // 2. Existing request in DB must be in "open" state else error
        // 3. Check if status in request body == "pending-fulfillment" or "schedule-expired" else error
        // 4. Request must come within "RequestCancelDurationInMin" (configurable) for "pending-fulfillment" else error
        // 5. handle each request state update and update state in DB

        // 1. "requestId" must be valid
        InstanceRequestV2Entity instanceRequestEntity =
                instanceRequestV2Repository.findRequestById(requestId).orElseThrow(
                        () -> new IcmsNotFoundException(
                                "Cannot find request with id " + requestId)
                );

        // 2. Existing request in DB must be in "open" or "active" state else error
        if (!InstanceServiceHelper
                .isRequestInOpenOrActiveState(instanceRequestEntity, icmsConfigurationProperties)) {
            String errMsg = String.format(
                    "Request associated with given id is not in 'open' or 'active' state, " +
                            "existing request state is '%s'", instanceRequestEntity.getState());
            sendPreConditionFailedTelemetryEvent(instanceRequestEntity, clientId, updateRequest, errMsg,
                    null, null);
            throw new PreConditionFailedException(errMsg);
        }

        CloudProvider cloudProvider = computePlatformService.primaryComputePlatformCloudProvider()
                .orElse(CloudProvider.UNKNOWN);
        String clusterName = null;
        if (isMessageBatchIdProvided(updateRequest)) {
            InstancePlacementValidationResponse validationResponse =
                    internalInstanceServiceHelper.validateInstancePlacement(
                            updateRequest.getPlacement(),
                            clientId, instanceRequestEntity.getRequestId());
            updateRequest.setPlacement(validationResponse.getInstancePlacement());
            cloudProvider = validationResponse.getCloudProvider();
            clusterName = validationResponse.getClusterName();

            // If message-batch-id not provided then finding cloudProvider from clientId
        } else if (instanceRequestEntity.getResourceProvider() == ResourceProvider.BYOC) {
            ClusterEntity clusterEntity = byocService.validateAndGetClusterEntityFromByocClusterId(clientId);
            clusterName = clusterEntity.getClusterName();
            cloudProvider = CloudProvider.getCloudProviderFromClusterProvider(
                    clusterEntity.getClusterProvider());
        }

        logIncomingRequest(requestId, clientId, updateRequest, "Updated request");

        ClientRequestDataModel.LaunchSpecification launchSpecification =
                instanceServiceHelper.getLaunchSpecificationForTelemetry(instanceRequestEntity.getRequest());

        GenericMetric genericMetric = new GenericMetric()
                .withCloudProvider(cloudProvider)
                .withCustomer(instanceRequestEntity.getCustomer())
                .withMessageBatchId(updateRequest.getMessageBatchId())
                .withSqsMessageAcknowledgeInstanceCount(updateRequest.getInstanceCount())
                .withClusterName(clusterName)
                .withClusterId(getClusterIdFromRequest(updateRequest))
                .withInstanceType(launchSpecification.getInstanceType())
                .withFunctionId(launchSpecification.getFunctionId())
                .withFunctionVersionId(launchSpecification.getVersionId())
                .withNcaId(launchSpecification.getNcaId())
                .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                .withRequestId(instanceRequestEntity.getRequestId())
                .withDeploymentId(instanceRequestEntity.getDeploymentId())
                .withCapacityType(updateRequest.getCapacityType())
                .withReservationId(updateRequest.getReservationId())
                .withGpuSpecificationId(instanceRequestEntity.getGpuSpecificationId());

        // 3. Check if status in request body == "pending-fulfillment" or "schedule-expired" or "cannot-fulfill" else error
        switch (updateRequest.getStatus()) {
            case SCHEDULE_EXPIRED ->
                    handleScheduledExpiredStateUpdate(updateRequest, instanceRequestEntity, genericMetric);

            case PENDING_FULFILLMENT ->
                    handlePendingFulfillmentStateUpdate(clientId, instanceRequestEntity, updateRequest,
                                                        auditProps, genericMetric, cloudProvider, clusterName);

            case CANNOT_FULFILL ->
                    handleCannotFulfill(updateRequest, clientId, instanceRequestEntity, genericMetric);

            default -> {
                String errMsg = String.format("'status' field in request body must be '%s'",
                                              List.of(SpotRequestStatusCode.SCHEDULE_EXPIRED,
                                                      SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                      SpotRequestStatusCode.CANNOT_FULFILL));
                logError(instanceRequestEntity.getRequestId(), clientId, updateRequest, errMsg);
                throw new IcmsBadRequestException(errMsg);
            }
        }
    }

    private void populateAuditValuesForUpdateInstanceRequest(
            @NotNull Map<String, Object> auditProps,
            @Nullable String requestId) {

        auditProps.put(AUDIT_OPERATION_KEY, AuditOperation.UPDATE_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
        auditProps.put(AUDIT_OBJECT_ID_KEY, requestId);
        auditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
        auditProps.put(AUDIT_STATE_KEY, AuditState.UPDATED_INSTANCE_REQUEST.toString());
        auditProps.put(AUDIT_SUMMARY_KEY,
                       "Updated status of instance request with id " + requestId);
    }

    private void handleCannotFulfill(
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @NotNull String clientId,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull GenericMetric genericMetric) {

        // For cannot-fulfill update messageBatchId must be provided
        if (!isMessageBatchIdProvided(updateRequest)) {
            String errMsg = "messageBatchId must be provided for cannot-fulfill status update";
            logError(instanceRequestEntity.getRequestId(), clientId, updateRequest, errMsg);
            throw new IcmsBadRequestException(errMsg);
        }

        // Update SQS batch status
        Optional<SqsMessageEntity> optionalSqsMessageEntity =
                sqsMessageRepository.findByRequestIdAndMessageBatchId(
                        instanceRequestEntity.getRequestId(), updateRequest.getMessageBatchId());
        if (optionalSqsMessageEntity.isEmpty()) {
            String errMsg = String.format("RequestId %s or messageBatchId %s doesn't exist",
                                          instanceRequestEntity.getRequestId(),
                                          updateRequest.getMessageBatchId());

            logError(instanceRequestEntity.getRequestId(), clientId, updateRequest, errMsg);
            throw new IcmsNotFoundException(errMsg);
        }

        // Preserve the acknowledged instance count so the deployment GET API can
        // surface accurate per-batch TERMINATED placeholders for cannot-fulfill.
        // All existing consumers of acknowledgedInstances filter by status, so
        // not zeroing here has no effect on legacy describe / over-allocation paths.
        SqsMessageEntity sqsMessageEntity = optionalSqsMessageEntity.get();
        sqsMessageEntity.setStatus(updateRequest.getStatus());
        sqsMessageRepository.update(sqsMessageEntity);

        sendTelemetryEventForCannotFulfill(instanceRequestEntity, updateRequest, genericMetric);
    }

    /*
   waitingTimeForScheduleExpireStateUpdate=15*(25+24+23+22+21+20+19+18+17+16+15+14+13+12+11+10) + (10 * 60) sec = 83.75 min
   As the request-cancel-duration(30 mins) < waitingTimeForScheduleExpireStateUpdate(83 min),
   schedule-expired typically arrives only after the request cancelation cron has already
   moved the request to CANCELED. We still persist the status on the matching batch row
   (if one exists) so the deployment GET API can surface schedule-expired as a per-batch
   TERMINATED reason and so the over-allocation guard skips the batch on retries.
     */
    private void handleScheduledExpiredStateUpdate(
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull GenericMetric genericMetric) {

        if (isMessageBatchIdProvided(updateRequest)) {
            Optional<SqsMessageEntity> optionalSqsMessageEntity =
                    sqsMessageRepository.findByRequestIdAndMessageBatchId(
                            instanceRequestEntity.getRequestId(), updateRequest.getMessageBatchId());
            if (optionalSqsMessageEntity.isPresent()) {
                SqsMessageEntity sqsMessageEntity = optionalSqsMessageEntity.get();
                sqsMessageEntity.setStatus(updateRequest.getStatus());
                sqsMessageRepository.update(sqsMessageEntity);
            }
        }

        genericMetric.withEventName(SCHEDULE_EXPIRED_STATE_UPDATE.toString());
        genericMetric.withRequestState(instanceRequestEntity.getState().toString());
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    private void validateRequestStateUpdateDuration(
            SpotInstanceRequestStatusUpdateRequest updateRequest,
            String clientId,
            InstanceRequestV2Entity instanceRequestEntity,
            @Nullable CloudProvider cloudProvider,
            @Nullable String clusterName) {
        // Request must come within "RequestCancelDurationInMin" (configurable) for "pending-fulfillment" else error
        Instant requestCreationTime = TimeUtils.getInstantFromUuid(instanceRequestEntity.getCreateTimeuuid());
        long cancelDurationInMin = icmsConfigurationProperties.getRequestCancelDurationInMin();
        if (Instant.now().compareTo(requestCreationTime
                                            .plus(cancelDurationInMin, ChronoUnit.MINUTES)) > 0) {
            String errMsg =
                    String.format("Request is not fulfilled within %d min", cancelDurationInMin);
            sendPreConditionFailedTelemetryEvent(instanceRequestEntity, clientId, updateRequest, errMsg,
                    cloudProvider, clusterName);
            throw new PreConditionFailedException(errMsg);
        }
    }

    private void handlePendingFulfillmentStateUpdate(
            @NotNull String clientId,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @NotNull Map<String, Object> auditProps,
            @NotNull GenericMetric genericMetric,
            @NotNull CloudProvider cloudProvider,
            @Nullable String clusterName) {



        if (isMessageBatchIdProvided(updateRequest)) {
            pendingFulfillmentWithMessageBatchId(clientId, instanceRequestEntity, updateRequest,
                                                 auditProps, genericMetric, cloudProvider, clusterName);
            return;
        }

        pendingFulfillmentLegacyFlow(clientId, instanceRequestEntity, updateRequest, auditProps,
                                     genericMetric, cloudProvider, clusterName);
    }

    // TODO: Evaluate if we can deprecate this flow
    private void pendingFulfillmentLegacyFlow(
            @NotNull String clientId,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @NotNull Map<String, Object> auditProps,
            @NotNull GenericMetric genericMetric,
            @NotNull CloudProvider cloudProvider,
            @Nullable String clusterName) {

        /*
        1. If the request is not within 30 mins then we should reject the update
        2. update request status to pending-fulfillment if not already updated (if this is first update for request)
         */

        // 1. If the request is not within 30 mins then we should reject the update
        validateRequestStateUpdateDuration(updateRequest, clientId, instanceRequestEntity,
                cloudProvider, clusterName);

        // 2. Update request status to pending-fulfillment if not already updated (if this is first update for request)
        if (Objects.equals(updateRequest.getStatus().toString(),
                           instanceRequestEntity.getStatusCode())) {

            return;
        }

        log.info("Legacy flow for pending-fulfillment state update in use, requestId {}",
                 instanceRequestEntity.getRequestId());
        updateRequestStateUpdateInDb(updateRequest, instanceRequestEntity, clientId, auditProps, genericMetric);
    }

    private void pendingFulfillmentWithMessageBatchId(@NotNull String clientId,
                                             @NotNull InstanceRequestV2Entity instanceRequestEntity,
                                             @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
                                             @NotNull Map<String, Object> auditProps,
                                             @NotNull GenericMetric genericMetric,
                                             @NotNull CloudProvider cloudProvider,
                                             @Nullable String clusterName) {

        /*
        1. Check if messageBatchId is already reported then avoid further processing
        2. If the request is not within 30 mins then we should reject the update
        3. Check for total ack count from previous updates
        4. Insert message-batch-id in DB
        4. If request status is already updated by previous batch, insert new batchId in DB and exit without updating status again
        5. Update request status for first time and insert batchId in DB
         */

        List<SqsMessageEntity> sqsMessageEntities = sqsMessageRepository
                .findByRequestId(instanceRequestEntity.getRequestId());

        InstancePlacement instancePlacement = updateRequest.getPlacement();

        // 1. Check if messageBatchId is already reported then avoid further processing
        // For model caching request, NVCA will send multiple pending-fulfilment update until model is downloaded
        // This will help in resource cleanup if request is closed in between.
        for (SqsMessageEntity sqsMessageEntity : sqsMessageEntities) {
            if (sqsMessageEntity.getKey().getMessageBatchId()
                    .equals(updateRequest.getMessageBatchId()) && instancePlacement != null) {

                // If from same zone then avoid further processing and return
                if (sqsMessageEntity.getZone().equals(instancePlacement.getAvailabilityZone())) {
                    sendSqsBatchStatusUpdateTelemetryEvent(instanceRequestEntity,
                            CopyUtil.deepCopy(genericMetric),
                            Events.RECEIVED_MULTIPLE_SQS_BATCH_STATUS_UPDATE.toString(),
                            updateRequest);
                    return;
                }

                // If from different zone then throw the error
                sendPreConditionFailedTelemetryEvent(instanceRequestEntity,
                        instancePlacement.getAvailabilityZone(), updateRequest,
                        MSG_ALREADY_ACK, cloudProvider, clusterName);
                throw new PreConditionFailedException(
                        String.format(MSG_ALREADY_ACK, updateRequest.getMessageBatchId(),
                                instancePlacement.getAvailabilityZone()));
            }
        }

        // 2. If the request is not within 30 mins then we should reject the update
        // TODO: We can think of accepting the update if request is already fulfilled
        validateRequestStateUpdateDuration(updateRequest, clientId, instanceRequestEntity,
                cloudProvider, clusterName);

        // 3. Check for total ack count from previous updates
        validateTotalAckInstancesForTargetingFlow(sqsMessageEntities, instancePlacement, instanceRequestEntity, updateRequest,
                cloudProvider, clusterName);

        // 5. Validate request if it is for reservation
        validateReservationIdForRequestStateUpdate(updateRequest, instancePlacement, instanceRequestEntity,
                cloudProvider, clusterName);

        // 6. If request status is already updated by previous batch, insert new batchId in DB and exit without updating status again
        if (Objects.equals(updateRequest.getStatus().toString(),
                instanceRequestEntity.getStatusCode())) {

            // Insert message-batch-id in DB
            insertMessageBatchId(updateRequest, instanceRequestEntity, clientId, genericMetric, cloudProvider);
            return;
        }

        // 5. Update request status for first time and insert batchId in DB
        updateRequestStateUpdateInDb(updateRequest, instanceRequestEntity, clientId, auditProps, genericMetric);
        insertMessageBatchId(updateRequest, instanceRequestEntity, clientId, genericMetric, cloudProvider);
    }

    private void updateRequestStateUpdateInDb(
            SpotInstanceRequestStatusUpdateRequest updateRequest,
            InstanceRequestV2Entity instanceRequestEntity,
            String clientId,
            @NotNull Map<String, Object> auditProps,
            @NotNull GenericMetric genericMetric) {

        // Creating copy of instanceRequestEntity before updating it
        InstanceRequestV2Entity entityBefore = AuditUtils.deepCopyInstanceRequestEntity(instanceRequestEntity);

        // Updating instanceRequestEntity with new state
        instanceRequestEntity.setStatusCode(updateRequest.getStatus().toString());
        instanceRequestEntity.setStatusMessage(String.format("Instance request status set to %s",
                                                         updateRequest.getStatus()));
        instanceRequestEntity.setStatusUpdateTime(Instant.now());

        // For first "pending-fulfillment" update for given request, updating batchWiseCheck to true
        if (isMessageBatchIdProvided(updateRequest)) {
            log.debug("Enabling batchWise check for {} request-id",
                      instanceRequestEntity.getRequestId());
            instanceRequestEntity.setCheckBatchwiseInfo(true);
        }

        // 5. handle each request state update and update state in DB
        updateStateInDbAndSendEvent(clientId, instanceRequestEntity, genericMetric, auditProps,
                                    entityBefore, updateRequest);
    }

    private void validateTotalAckInstancesForTargetingFlow(
            List<SqsMessageEntity> sqsMessageEntities,
            @NotNull InstancePlacement instancePlacement,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @Nullable CloudProvider cloudProvider,
            @Nullable String clusterName) {

        /*
        Check if we already fulfilled requested capacity. With targeting feature we are sending
        SQS messages to all eligible clusters, that may lead to more instances that requests.
        Here we check, if all needed instances are created. We perform this check only for
        clusters with allowClusterTargeting=true and when backend is empty
        */
        int requestedCapacity = instanceRequestEntity.getInstanceCount();
        int alreadyAllocated = getAlreadyAllocated(sqsMessageEntities);
        int instanceCountInUpdateRequest = updateRequest.getInstanceCount() != null
                ? updateRequest.getInstanceCount() : 0;
        int totalWithIncomingAckInstances = alreadyAllocated + instanceCountInUpdateRequest;
        log.debug("Request update for pending fulfilment with {} id, {} message batchId, "
                          + "{} totalWithIncomingAckInstances, {} totalRequestedCapacity",
                  instanceRequestEntity.getRequestId(), updateRequest.getMessageBatchId(),
                  totalWithIncomingAckInstances, requestedCapacity);

        if (totalWithIncomingAckInstances > requestedCapacity) {
            // this is not an error as we intentionally send more requests that needed
            log.info(MSG_REQUEST_FULFILLED, requestedCapacity,
                     instanceRequestEntity.getRequestId());
            String errMsg = String.format(MSG_ALREADY_FULFILLED, instanceRequestEntity.getRequestId());
            sendPreConditionFailedTelemetryEvent(instanceRequestEntity,
                                                 instancePlacement.getAvailabilityZone(),
                                                 updateRequest, errMsg,
                                                 cloudProvider, clusterName);
            throw new PreConditionFailedException(errMsg);

        }
    }

    private int getAlreadyAllocated(@NotNull List<SqsMessageEntity> sqsMessageEntities) {
        // Skip batches that already reported a terminal failure - their ack count
        // is preserved on the row (so we can surface TERMINATED placeholders) but
        // they no longer hold allocated capacity, so a retry from another batch
        // must be allowed through.
        return sqsMessageEntities
                .stream()
                .filter(e -> e.getStatus() != SpotRequestStatusCode.CANNOT_FULFILL
                        && e.getStatus() != SpotRequestStatusCode.SCHEDULE_EXPIRED)
                .map(SqsMessageEntity::getAcknowledgedInstances)
                .filter(Objects::nonNull)
                .mapToInt(i -> i)
                .sum();
    }

    private void updateStateInDbAndSendEvent(
            @NotNull String clientId,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull GenericMetric genericMetric,
            @NotNull Map<String, Object> auditProps,
            @Nullable InstanceRequestV2Entity entityBefore,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest) {
        try {
            instanceRequestV2Repository.update(instanceRequestEntity);
        } catch (Exception e) {
            logError(instanceRequestEntity.getRequestId(), clientId, updateRequest, String.format(
                    "failed to update state of requestId in DB when updating status, exception - %s",
                    e.getMessage()));
            throw e;
        }

        // Send audit logs
        populateAuditValuesForUpdateInstanceRequest(auditProps, instanceRequestEntity.getRequestId());
        auditService.sendAuditEventForInstanceRequest(auditProps, entityBefore,
                                                  AuditUtils.deepCopyInstanceRequestEntity(
                                                          instanceRequestEntity));

        // Set resource provider so that we can get to know if it's a byoc request or a non-BYOC request
        Map<String, Object> metaData = new HashMap<>();
        metaData.put(TelemetryEventClient.EventMetaData.REQUEST_BODY.getName(),
                     GsonCompatMapper.toJson(updateRequest));
        metaData.put(MESSAGE_BATCH_STATUS.getName(), instanceRequestEntity.getCheckBatchwiseInfo());
        metaData.put(REQUEST_STATUS.getName(), instanceRequestEntity.getStatusCode());

        // Setting common fields in genericMetric
        genericMetric.withRequestState(instanceRequestEntity.getState().toString())
                .withMetadata(metaData)
                .withResourceProvider(instanceRequestEntity.getResourceProvider())
                .withEventName(Events.STARTED_PROCESSING_INSTANCE_REQUEST.toString())
                .withInstanceRequestAcceptanceTime(
                        Duration.between(TimeUtils.getInstantFromUuid(instanceRequestEntity.getCreateTimeuuid()), Instant.now())
                                .toSeconds());

        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    private void insertMessageBatchId(
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull String clientId,
            @NotNull GenericMetric genericMetric,
            @NotNull CloudProvider cloudProvider) {
        if (isMessageBatchIdProvided(updateRequest) && isBatchWiseInfoFetchingEnabled(
                instanceRequestEntity)) {
            try {
                log.info("Received SQS batch status update, request-id {} updateRequest {}",
                         instanceRequestEntity.getRequestId(), GsonCompatMapper.toJson(updateRequest));



                SqsMessageEntity entity = SqsMessageEntity.builder()
                        .key(SqsMessageKey.builder()
                                     .messageBatchId(updateRequest.getMessageBatchId() != null ? updateRequest.getMessageBatchId() : "FixedNull_" + UUID.randomUUID())
                                     .requestId(instanceRequestEntity.getRequestId())
                                     .build())
                        .acknowledgedInstances(updateRequest.getInstanceCount())
                        .zone(getClusterIdFromRequest(updateRequest))
                        .status(updateRequest.getStatus())
                        .creationTime(Instant.now())
                        .cloudProvider(cloudProvider.toString())
                        .reservationId(updateRequest.getReservationId())
                        .capacityType(updateRequest.getCapacityType())
                        .build();

                /*
                We will update the entity instead of insert:
                If request-state update comes for first time, it will insert in DB
                If request-state update comes for multiple times(with same or different request-state), it will update in DB (cannot-fullfill -> pending-fulfillment)
                 */
                sqsMessageRepository.update(entity);

                sendSqsBatchStatusUpdateTelemetryEvent(instanceRequestEntity,
                                                       CopyUtil.deepCopy(genericMetric),
                                                       Events.RECEIVED_SQS_BATCH_STATUS_UPDATE.toString(),
                                                       updateRequest);

            } catch (Exception exception) {
                logError(instanceRequestEntity.getRequestId(), clientId, updateRequest,
                        String.format("Error occurred while inserting SQS message entry in DB %s",
                                exception.getMessage()));

                // rethrowing same exception
                throw exception;
            }
        }
    }

    private void sendPreConditionFailedTelemetryEvent(
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull String clientId,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @Nullable String errMsg,
            @Nullable CloudProvider cloudProvider,
            @Nullable String clusterName) {
        logError(instanceRequestEntity.getRequestId(), clientId, updateRequest,
                 String.format("PRE_CONDITION_FAILED: %s", errMsg));
        try {

            ClientRequestDataModel.LaunchSpecification launchSpecification = instanceServiceHelper.getLaunchSpecificationForTelemetry(
                    instanceRequestEntity.getRequest());

            GenericMetric metric = new GenericMetric()
                    .withEventName(Events.PRE_CONDITION_FAILED.toString())
                    .withRequestId(instanceRequestEntity.getRequestId())
                    .withCustomer(instanceRequestEntity.getCustomer())
                    .withInstanceCount(instanceRequestEntity.getInstanceCount())
                    .withResourceProvider(instanceRequestEntity.getResourceProvider())
                    .withRequestState(instanceRequestEntity.getState().toString())
                    .withMessageBatchId(updateRequest.getMessageBatchId())
                    .withSqsMessageAcknowledgeInstanceCount(updateRequest.getInstanceCount())
                    .withClusterId(getClusterIdFromRequest(updateRequest))
                    .withClusterName(clusterName)
                    .withCloudProvider(cloudProvider)
                    .withDeploymentId(instanceRequestEntity.getDeploymentId())
                    .withGpuSpecificationId(instanceRequestEntity.getGpuSpecificationId())
                    .withCapacityType(updateRequest.getCapacityType())
                    .withReservationId(updateRequest.getReservationId())
                    .withError(errMsg);

            if (launchSpecification != null) {
                metric = metric
                        .withInstanceType(launchSpecification.getInstanceType())
                        .withFunctionId(launchSpecification.getFunctionId())
                        .withFunctionVersionId(launchSpecification.getVersionId())
                        .withNcaId(launchSpecification.getNcaId());
            }

            Map<String, Object> metaData = new HashMap<>();
            metaData.put(REQUEST_BODY.getName(), GsonCompatMapper.toJson(updateRequest));
            metaData.put(REQUEST_STATUS.getName(), instanceRequestEntity.getStatusCode());
            metric.withMetadata(metaData);

            telemetryEventClient.triggerEvent(List.of(metric));
        } catch (Exception exception) {
            log.error(
                    "Failed to send telemetry event for precondition failure error - {}, exception ",
                    exception.getMessage(),
                    exception);
        }
    }

    private void logError(
            @Nullable String requestId,
            @NotNull String clientId,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @Nullable String errorMsg) {
        log.error("Request-id {} client-id {}  hashedClientId {} updateRequest {} error {}",
                  requestId, clientId, getClusterIdFromAuthClientId(clientId),
                  GsonCompatMapper.toJson(updateRequest), errorMsg);
    }

    private void sendSqsBatchStatusUpdateTelemetryEvent(
            InstanceRequestV2Entity instanceRequestEntity,
            GenericMetric genericMetric,
            String eventName,
            SpotInstanceRequestStatusUpdateRequest updateRequest) {

        // Set resource provider so that we can get to know if it's a byoc request or a non-BYOC request
        genericMetric.withEventName(eventName);
        Map<String, Object> metaData = new HashMap<>();
        metaData.put(TelemetryEventClient.EventMetaData.REQUEST_BODY.getName(),
                     GsonCompatMapper.toJson(updateRequest));
        metaData.put(MESSAGE_BATCH_STATUS.getName(), instanceRequestEntity.getCheckBatchwiseInfo());
        metaData.put(REQUEST_STATUS.getName(), instanceRequestEntity.getStatusCode());

        genericMetric.withMetadata(metaData)
                .withResourceProvider(instanceRequestEntity.getResourceProvider())
                .withInstanceRequestAcceptanceTime(
                        Duration.between(TimeUtils.getInstantFromUuid(instanceRequestEntity.getCreateTimeuuid()), Instant.now())
                                .toSeconds())
                .withRequestState(instanceRequestEntity.getState().toString());

        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    private void sendTelemetryEventForCannotFulfill(
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @NotNull GenericMetric genericMetric) {

        // Set resource provider so that we can get to know if it's a byoc request or a non-BYOC request
        Map<String, Object> metaData = new HashMap<>();
        metaData.put(TelemetryEventClient.EventMetaData.REQUEST_BODY.getName(),
                     GsonCompatMapper.toJson(updateRequest));

        genericMetric.withEventName(CANNOT_FULFILL_STATE_UPDATE.toString());
        genericMetric.withRequestState(instanceRequestEntity.getState().toString());
        genericMetric.withMetadata(metaData);
        genericMetric.withResourceProvider(instanceRequestEntity.getResourceProvider());
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    private boolean isMessageBatchIdProvided(@NotNull SpotInstanceRequestStatusUpdateRequest updateRequest) {
        return !StringUtils.isEmpty(updateRequest.getMessageBatchId()) &&
                updateRequest.getInstanceCount() != null && updateRequest.getInstanceCount() != 0;
    }

    private boolean isBatchWiseInfoFetchingEnabled(@NotNull InstanceRequestV2Entity instanceRequestEntity) {
        return instanceRequestEntity.getCheckBatchwiseInfo() != null &&
                instanceRequestEntity.getCheckBatchwiseInfo();
    }

    @Nullable
    private String getClusterIdFromRequest(@NotNull SpotInstanceRequestStatusUpdateRequest updateRequest) {
        if (updateRequest.getPlacement() != null &&
                updateRequest.getPlacement().getAvailabilityZone() != null) {
            return updateRequest.getPlacement().getAvailabilityZone();
        }
        return null;
    }

    private void logIncomingRequest(
            @Nullable String requestId,
            @NotNull String clientId,
            @NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
            @Nullable String logMsg) {
        log.info(
                "{} Received request status update for request-id {}, from client {} hashedClientId {}, updateRequest {}",
                logMsg, requestId, clientId, getClusterIdFromAuthClientId(clientId),
                GsonCompatMapper.toJson(updateRequest));
    }

    private void validateCapacityType(SpotInstanceRequestStatusUpdateRequest updateRequest) {

        // ReservationId should be provided when capacityType: [RESERVED, RESERVED_BACKUP]
        if ((CapacityType.RESERVED.equals(updateRequest.getCapacityType()) ||
                CapacityType.RESERVED_BACKUP.equals(updateRequest.getCapacityType()))
                && updateRequest.getReservationId() == null) {

            String errMsg = "ReservationId must be provided for capacityType: [RESERVED, RESERVED_BACKUP]";
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }
    }

    private int getInstanceCountFromRequest(SpotInstanceRequestStatusUpdateRequest updateRequest) {
        Integer instanceCountFromRequest = updateRequest.getInstanceCount();
        return instanceCountFromRequest != null ? instanceCountFromRequest : 0;
    }

    private void validateReservationIdForRequestStateUpdate(@NotNull SpotInstanceRequestStatusUpdateRequest updateRequest,
                                                            @NotNull InstancePlacement instancePlacement,
                                                            @NotNull InstanceRequestV2Entity instanceRequestEntity,
                                                            @Nullable CloudProvider cloudProvider,
                                                            @Nullable String clusterName) {
        try {
            // Validate and get ReservationEntity
            Optional<ReservationEntity> optionalReservationEntity = reservationCapacityValidationHelper.validateAndGetReservationEntity(updateRequest.getReservationId());

            // Validate RESERVED_BACKUP capacity to prevent over-allocation in reservation
            if (optionalReservationEntity.isPresent() && updateRequest.getCapacityType().equals(CapacityType.RESERVED_BACKUP)) {
                reservationCapacityValidationHelper.validateReservationBackupCapacityForRequestStateUpdate(optionalReservationEntity.get(),
                        instanceRequestEntity, getInstanceCountFromRequest(updateRequest));
            }

        } catch (PreConditionFailedException preConditionFailedException) {
            sendPreConditionFailedTelemetryEvent(instanceRequestEntity,
                    instancePlacement.getAvailabilityZone(), updateRequest,
                    preConditionFailedException.getBody().getDetail(),
                    cloudProvider, clusterName);
            throw preConditionFailedException;
        }
    }
}
