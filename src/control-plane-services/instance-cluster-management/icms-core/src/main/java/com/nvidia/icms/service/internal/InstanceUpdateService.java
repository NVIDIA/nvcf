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

import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.RUNNING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.STARTING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.TERMINATED;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceStatus.FULFILLED;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REQUEST_STATUS;
import static com.nvidia.icms.util.InstanceStateUtils.validateInstanceStateTransition;
import static com.nvidia.icms.util.audit.AuditOperation.REQUEST_STATE_TRANSITION_TO_ACTIVE;
import static com.nvidia.icms.util.InstanceServiceUtil.getStringValue;
import static com.nvidia.icms.util.InstanceServiceUtil.getStringValueOfUuid;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_ID_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_OPERATION_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_INSTANCE_REQUEST_TYPE;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_STATE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_SUMMARY_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.AUDIT_TYPE_KEY;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForRunningInstance;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForStartingInstance;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForTerminateInstance;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsForbiddenException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.util.AuthUtils;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest.SpotInstanceHeathInfo;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.outbound.fnds.model.FndsStages;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.FunctionDeploymentStagesService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.internal.InternalInstanceServiceHelper.InstancePlacementValidationResponse;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.service.workers.WorkerIdentifierService;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.util.InstanceStateUtils;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.util.audit.AuditUtils;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.logging.log4j.util.Strings;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class InstanceUpdateService {

    private final InstanceRequestV2Repository instanceRequestV2Repository;
    private final InstanceV2Repository instanceV2Repository;

    private final AppAuditService auditService;
    private final TelemetryEventClient telemetryEventClient;

    private final InternalInstanceServiceHelper internalInstanceServiceHelper;

    private final InstanceServiceHelper instanceServiceHelper;

    private final InstanceLifecycleHelper instanceLifecycleHelper;

    private final FunctionDeploymentStagesService functionDeploymentStagesService;

    private final ClusterRepository clusterRepository;

    private final ReservationCapacityValidationHelper reservationCapacityValidationHelper;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final WorkerIdentifierService workerIdentifierService;

    /*
     We will not log the healthInfo which contains error logs from container
     */
    /**
     * Store or remove the worker-identifier set for an instance. Called only after the status
     * update itself has been accepted, so a rejected update never changes worker identity state.
     * On terminal status: always clean up (even if workerAuth is absent).
     * On non-terminal status: store only if workerAuth is present; no-op otherwise.
     */
    private void handleWorkerAuth(
            SpotInstanceStatusUpdateRequest request, String instanceId, String clientId) {
        if (isInstanceTerminated(request.getStatus())) {
            workerIdentifierService.deleteWorkerIdentifiers(clientId, instanceId);
        } else if (request.getWorkerAuth() != null) {
            workerIdentifierService.storeWorkerIdentifiers(clientId, instanceId,
                    request.getWorkerAuth());
        }
    }

    /**
     * A compute cluster may only report on instances it owns. {@code instanceId} and
     * {@code instanceRequestId} are caller-supplied path variables, so for cluster principals
     * (NVCA over cluster OIDC/PSAT or a cluster API key) the instance, when it already exists,
     * must belong to the addressed request and be placed on the caller's cluster; a not yet
     * registered instance is accepted only when the request targets the caller's cluster.
     * Control-plane service identities (managed NVCF) are not subject to this check.
     */
    void assertCallerOwnsInstance(String clientId, String clusterName,
            InstanceRequestV2Entity request, String instanceId) {
        if (!AuthUtils.isClusterPrincipal()) {
            return;
        }
        Optional<InstanceV2Entity> instance =
                instanceV2Repository.findInstanceByCustomerAndId(request.getCustomer(), instanceId);
        if (instance.isPresent()) {
            InstanceV2Entity existing = instance.get();
            if (!request.getRequestId().equals(existing.getRequestId())
                    || !clientId.equals(existing.getZone())) {
                log.warn("Cluster {} attempted to update instance {} of request {} (zone={}, request={})",
                        clientId, instanceId, request.getRequestId(), existing.getZone(),
                        existing.getRequestId());
                throw new IcmsForbiddenException("instance is not owned by the calling cluster");
            }
            return;
        }
        Set<String> targets = request.getClusters();
        if (targets != null && !targets.isEmpty()
                && !targets.contains(clientId)
                && (clusterName == null || !targets.contains(clusterName))) {
            log.warn("Cluster {} attempted to register instance {} for request {} targeted at {}",
                    clientId, instanceId, request.getRequestId(), targets);
            throw new IcmsForbiddenException("request is not targeted at the calling cluster");
        }
    }

    public static SpotInstanceStatusUpdateRequest ignoreSensitiveInformation(
            @NotNull SpotInstanceStatusUpdateRequest statusUpdateRequest) {
        return SpotInstanceStatusUpdateRequest.builder().action(statusUpdateRequest.getAction())
                .instanceState(statusUpdateRequest.getInstanceState())
                .placement(statusUpdateRequest.getPlacement())
                .status(statusUpdateRequest.getStatus()).imageId(statusUpdateRequest.getImageId())
                .terminationCause(statusUpdateRequest.getTerminationCause())
                .requestState(statusUpdateRequest.getRequestState())
                .systemFailure(statusUpdateRequest.getSystemFailure())
                .instanceIps(getInstanceIps(statusUpdateRequest))
                .capacityType(statusUpdateRequest.getCapacityType())
                .reservationId(statusUpdateRequest.getReservationId())
                .healthInfo(getFilteredHealthInfo(statusUpdateRequest)).build();
    }

    public static SpotInstanceHeathInfo getFilteredHealthInfo(
            @Nullable SpotInstanceStatusUpdateRequest statusUpdateRequest) {
        String sizeOfErrorLog = "0";
        if (statusUpdateRequest != null && statusUpdateRequest.getHealthInfo() != null) {
            if (statusUpdateRequest.getHealthInfo().getErrorLog() != null) {
                sizeOfErrorLog = Integer.toString(
                        statusUpdateRequest.getHealthInfo().getErrorLog().length());
            }
            if (statusUpdateRequest.getHealthInfo().getErrorSource() != null) {
                return new SpotInstanceHeathInfo(sizeOfErrorLog, statusUpdateRequest.getHealthInfo()
                        .getErrorSource());
            }
        }
        return new SpotInstanceHeathInfo(sizeOfErrorLog, null);
    }

    private static Set<String> getInstanceIps(@Nullable SpotInstanceStatusUpdateRequest request) {
        Set<String> instanceIps = null;
        if (request != null && request.getInstanceIps() != null) {
            instanceIps = request.getInstanceIps().stream().filter(Strings::isNotEmpty).collect(
                    Collectors.toSet());
        }
        return instanceIps;
    }

    @Observed
    public void updateInstanceStatus(
            @Nullable String instanceRequestId,
            @NotNull String instanceId,
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull String clientId,
            @NotNull Map<String, Object> auditProps) {

        // Logging initial request
        logInstanceUpdateInfo(instanceStatusUpdateRequest, instanceId, instanceRequestId,
                clientId, "Initial request");

        // Validate capacity type
        validateCapacityType(instanceStatusUpdateRequest);

        // Handle the case if this request is coming from BYOC cluster
        InstancePlacementValidationResponse validationResponse = internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), clientId, instanceRequestId);
        instanceStatusUpdateRequest.setPlacement(validationResponse.getInstancePlacement());
        ResourceProvider resourceProvider = validationResponse.getResourceProvider();
        CloudProvider cloudProvider = validationResponse.getCloudProvider();
        String clusterName = validationResponse.getClusterName();

        // Logging updated request
        logInstanceUpdateInfo(instanceStatusUpdateRequest, instanceId, instanceRequestId,
                clientId, "Updated request");

        // Get customer from the repository for requestId and validate requestId
        InstanceRequestV2Entity instanceRequestEntity = instanceRequestV2Repository.findRequestById(
                instanceRequestId).orElseThrow(() -> new IcmsNotFoundException(
                "Cannot find request with id " + instanceRequestId));

        // Cluster principals may only address instances of requests placed on their cluster.
        assertCallerOwnsInstance(clientId, clusterName, instanceRequestEntity, instanceId);

        // Handling terminated state update irrespective of request state
        if (isInstanceTerminated(instanceStatusUpdateRequest.getStatus())) {
            handleInstanceTerminationUpdate(instanceStatusUpdateRequest, instanceId, auditProps,
                    instanceRequestEntity, cloudProvider, resourceProvider,
                    clusterName);
            // Only an accepted terminal update drops the worker identity set.
            handleWorkerAuth(instanceStatusUpdateRequest, instanceId, clientId);
            return;
        }

        switch (instanceRequestEntity.getState()) {
            case OPEN, ACTIVE -> {
                handelInstanceUpdateForOpenOrActiveRequest(instanceRequestEntity, instanceId,
                        instanceStatusUpdateRequest,
                        auditProps, cloudProvider,
                        resourceProvider, clusterName);
                // Only an accepted update registers worker identities.
                handleWorkerAuth(instanceStatusUpdateRequest, instanceId, clientId);
            }
            case CLOSED -> handelInstanceUpdateForClosedRequest(instanceRequestEntity, instanceId,
                                                                instanceStatusUpdateRequest, cloudProvider);
            default -> {
                String errMsg = String.format("Invalid state %s for instance request with id %s",
                        instanceRequestEntity.getState(), instanceRequestId);
                sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestEntity,
                                                     instanceStatusUpdateRequest, errMsg, cloudProvider);
                throw new PreConditionFailedException(errMsg);
            }
        }
    }

    private boolean isInstanceTerminated(SpotInstanceStatus instanceStatus) {
        return instanceStatus == SpotInstanceStatus.INSTANCE_TERMINATED_NO_CAPACITY
                || instanceStatus == SpotInstanceStatus.INSTANCE_TERMINATED_BY_USER
                || instanceStatus == SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE;
    }

    /**
     * Handles instance updates when request is in OPEN or ACTIVE state.
     * OPEN: Request has no instances yet, allows only PENDING_FULFILLMENT status
     * ACTIVE: Request has instances, allows both PENDING_FULFILLMENT and FULFILLED status
     */
    private void handelInstanceUpdateForOpenOrActiveRequest(
            InstanceRequestV2Entity instanceRequestEntity, String instanceId,
            SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            Map<String, Object> auditProps, CloudProvider cloudProvider,
            ResourceProvider resourceProvider, String clusterName) {

        boolean isOpenState = instanceRequestEntity.getState() == SpotInstanceRequestState.OPEN;

        // Status code validation: OPEN accepts only PENDING_FULFILLMENT,
        // ACTIVE accepts PENDING_FULFILLMENT or FULFILLED
        if (isOpenState) {
            if (!instanceRequestEntity.getStatusCode()
                    .equals(SpotRequestStatusCode.PENDING_FULFILLMENT.toString())) {
                String errMsg =
                        String.format("Invalid request status %s for instance request with id %s",
                                instanceRequestEntity.getStatusCode(),
                                instanceRequestEntity.getRequestId());
                sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestEntity,
                                                     instanceStatusUpdateRequest, errMsg, cloudProvider);
                throw new PreConditionFailedException(errMsg);
            }
        } else {
            // ACTIVE state
            if (!instanceRequestEntity.getStatusCode()
                    .equals(SpotRequestStatusCode.PENDING_FULFILLMENT.toString()) &&
                    !instanceRequestEntity.getStatusCode()
                            .equals(SpotRequestStatusCode.FULFILLED.toString())) {
                String errMsg = String.format(
                        "Invalid request status %s for instance request with id %s in ACTIVE state",
                        instanceRequestEntity.getStatusCode(),
                        instanceRequestEntity.getRequestId());
                sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestEntity,
                                                     instanceStatusUpdateRequest, errMsg, cloudProvider);
                throw new PreConditionFailedException(errMsg);
            }
        }

        if (instanceStatusUpdateRequest.getStatus() == FULFILLED) {

            validateInstanceFulfillmentRequest(instanceStatusUpdateRequest,
                                               Set.of(STARTING, RUNNING), instanceId,
                                               instanceRequestEntity, cloudProvider);

            SpotInstanceInternalState instanceState = instanceStatusUpdateRequest.getInstanceState();

            if (instanceState == STARTING) {
                handleNewInstanceRegistration(instanceStatusUpdateRequest,
                        instanceId, auditProps,
                        instanceRequestEntity, cloudProvider,
                        resourceProvider, clusterName, isOpenState);
            } else if (instanceState == RUNNING) {
                handleRunningInstanceStateUpdate(instanceStatusUpdateRequest,
                        instanceId, auditProps,
                        instanceRequestEntity, cloudProvider,
                        resourceProvider, clusterName, isOpenState);
            }
            return;
        }

        String errMsg = String.format("Specified status %s for instance with id %s is invalid",
                instanceStatusUpdateRequest.getStatus().toString(),
                instanceId);
        logErrorMessage(instanceId, instanceRequestEntity.getRequestId(),
                instanceStatusUpdateRequest, errMsg);
        throw new IcmsBadRequestException(errMsg);
    }

    private void handleNewInstanceRegistration(
            SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest, String instanceId,
            Map<String, Object> auditProps, InstanceRequestV2Entity instanceRequestEntity,
            CloudProvider cloudProvider, ResourceProvider resourceProvider, String clusterName,
            boolean isOpenState) {

        Optional<InstanceV2Entity> optionalInstanceEntity =
                instanceV2Repository.findInstanceByCustomerAndId(instanceRequestEntity.getCustomer(),
                        instanceId);
        if (optionalInstanceEntity.isEmpty()) {
            insertNewInstance(instanceStatusUpdateRequest, instanceId, auditProps,
                    instanceRequestEntity, cloudProvider, resourceProvider,
                    clusterName, isOpenState);
            return;
        }

        InstanceV2Entity instanceEntity = optionalInstanceEntity.get();
        validateStateTransition(instanceRequestEntity, instanceEntity,
                                instanceStatusUpdateRequest, instanceId, cloudProvider);
        updateInstance(instanceStatusUpdateRequest, auditProps,
                instanceRequestEntity, cloudProvider, resourceProvider,
                instanceEntity, clusterName);
    }

    private void handleRunningInstanceStateUpdate(
            SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest, String instanceId,
            Map<String, Object> auditProps, InstanceRequestV2Entity instanceRequestEntity,
            CloudProvider cloudProvider, ResourceProvider resourceProvider, String clusterName,
            boolean isOpenState) {

        /*
         DON'T remove this check as NVCA can send running instance as first state update
         */
        Optional<InstanceV2Entity> optionalInstanceEntity =
                instanceV2Repository.findInstanceByCustomerAndId(
                        instanceRequestEntity.getCustomer(), instanceId);
        if (optionalInstanceEntity.isEmpty()) {
            log.info("Could not find instance with id {} in DB, considering RUNNING as first "
                            + "instance state update, request-state: {}, requestUpdate - {}",
                    instanceId, instanceRequestEntity.getState(),
                    ignoreSensitiveInformation(instanceStatusUpdateRequest));
            insertNewInstance(instanceStatusUpdateRequest, instanceId, auditProps,
                    instanceRequestEntity, cloudProvider, resourceProvider,
                    clusterName, isOpenState);
            return;
        }

        InstanceV2Entity instanceEntity = optionalInstanceEntity.get();
        validateStateTransition(instanceRequestEntity, instanceEntity,
                                instanceStatusUpdateRequest, instanceId, cloudProvider);
        updateInstance(instanceStatusUpdateRequest, auditProps,
                instanceRequestEntity, cloudProvider, resourceProvider,
                instanceEntity, clusterName);
    }

    private void insertNewInstance(
            SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest, String instanceId,
            Map<String, Object> auditProps, InstanceRequestV2Entity instanceRequestEntity,
            CloudProvider cloudProvider, ResourceProvider resourceProvider, String clusterName,
            boolean isOpenState) {

        // Validating reservationId for new instance report
        Optional<ReservationEntity> optionalReservationEntity = validateReservationIdForNewInstanceCreation(instanceStatusUpdateRequest,
                                                                                                            instanceRequestEntity, instanceId, cloudProvider);

        InstanceV2Entity instanceEntity = getConfiguredInstanceEntity(
                instanceStatusUpdateRequest, instanceRequestEntity, instanceId, resourceProvider, cloudProvider, optionalReservationEntity);

        try {
            instanceV2Repository.insert(instanceEntity);
        } catch (Exception e) {
            log.error(
                    "Exception while inserting the info of instance with id {} in database, error - {}",
                    instanceId, e.getMessage(), e);
            throw e;
        }

        // Transition request state from OPEN to ACTIVE when first instance is created (only for OPEN state)
        if (isOpenState) {
            transitionRequestToActiveState(instanceRequestEntity, auditProps);
        }

        sendFunctionDeploymentStage(instanceRequestEntity, instanceEntity, instanceStatusUpdateRequest, null);

        // sending event for instance state change
        triggerEventForInstanceStateChange(instanceEntity, instanceStatusUpdateRequest,
                Duration.between(instanceRequestEntity.getStatusUpdateTime(),
                        Instant.now()).toSeconds(),
                instanceRequestEntity, cloudProvider, resourceProvider,
                clusterName);

        sendAuditEventForInstanceStateUpdate(auditProps, instanceEntity,
                InstanceV2Entity.getEmptyEntity());
    }

    /**
     * Transitions the request state from OPEN to ACTIVE and
     * Transitions the request status to FULFILLED when the first instance is created.
     * This helps identify requests that have active instances running.
     */
    private void transitionRequestToActiveState(InstanceRequestV2Entity instanceRequestEntity,
                                                Map<String, Object> auditProps) {
        // Check if feature flag is enabled
        if (!icmsConfigurationProperties.isRequestStateTransitionToActiveEnabled()) {
            log.debug("Request state transition to ACTIVE is disabled, " +
                    "skipping transition for request {}", instanceRequestEntity.getRequestId());
            return;
        }

        // Only transition if request is still in OPEN state
        if (instanceRequestEntity.getState() != SpotInstanceRequestState.OPEN) {
            log.debug("Request {} is already in {} state, skipping transition to ACTIVE",
                    instanceRequestEntity.getRequestId(), instanceRequestEntity.getState());
            return;
        }

        InstanceRequestV2Entity entityBefore = AuditUtils.deepCopyInstanceRequestEntity(instanceRequestEntity);

        instanceRequestEntity.setState(SpotInstanceRequestState.ACTIVE);
        instanceRequestEntity.setStatusCode(SpotRequestStatusCode.FULFILLED.toString());
        instanceRequestEntity.setStatusUpdateTime(Instant.now());

        try {
            instanceRequestV2Repository.update(instanceRequestEntity);
            log.info("Transitioned request {} from OPEN to ACTIVE state after first " +
                    "instance creation", instanceRequestEntity.getRequestId());
        } catch (Exception e) {
            log.error("Failed to transition request {} to ACTIVE state, error - {}",
                    instanceRequestEntity.getRequestId(), e.getMessage(), e);

            sendTelemetryEventForRequestStateTransitionFailure(entityBefore, instanceRequestEntity, e);

            // Don't fail the instance creation if state transition fails
            // The request will remain in OPEN state but instance is created
            return;
        }

        // Send audit event for request state change
        sendAuditEventForRequestStateTransition(auditProps, entityBefore, instanceRequestEntity);

        // Send telemetry event for request state transition
        sendTelemetryEventForRequestStateTransition(instanceRequestEntity, entityBefore.getState());
    }

    private void sendAuditEventForRequestStateTransition(Map<String, Object> auditProps,
                                                         InstanceRequestV2Entity entityBefore,
                                                         InstanceRequestV2Entity entityAfter) {
        try {
            Map<String, Object> requestAuditProps = new HashMap<>(auditProps);
            requestAuditProps.put(AUDIT_TYPE_KEY, AUDIT_INSTANCE_REQUEST_TYPE);
            requestAuditProps.put(AUDIT_OPERATION_KEY, REQUEST_STATE_TRANSITION_TO_ACTIVE.toString());
            requestAuditProps.put(AUDIT_OBJECT_ID_KEY, entityAfter.getRequestId());
            requestAuditProps.put(AUDIT_OBJECT_LOCATION_KEY, AUDIT_OBJECT_LOCATION_INSTANCE_REQUEST);
            requestAuditProps.put(AUDIT_STATE_KEY, entityAfter.getState().toString());
            requestAuditProps.put(AUDIT_SUMMARY_KEY, String.format("Request state transitioned from %s to %s for request %s",
                    entityBefore.getState().toString(), entityAfter.getState().toString(), entityAfter.getRequestId()));
            auditService.sendAuditEventForInstanceRequest(requestAuditProps, entityBefore, entityAfter);
        } catch (Exception e) {
            log.error("Failed to send audit event for request state transition, error - {}",
                    e.getMessage(), e);
        }
    }

    private void sendTelemetryEventForRequestStateTransition(InstanceRequestV2Entity instanceRequestEntity,
                                                             SpotInstanceRequestState previousState) {
        try {
            ClientRequestDataModel.LaunchSpecification launchSpecification =
                    instanceServiceHelper.getLaunchSpecificationForTelemetry(instanceRequestEntity.getRequest());

            GenericMetric genericMetric = new GenericMetric()
                    .withEventName(Events.REQUEST_STATE_TRANSITION_TO_ACTIVE.toString())
                    .withRequestId(instanceRequestEntity.getRequestId())
                    .withCustomer(instanceRequestEntity.getCustomer())
                    .withRequestState(instanceRequestEntity.getState().toString())
                    .withResourceProvider(instanceRequestEntity.getResourceProvider())
                    .withNcaId(launchSpecification.getNcaId())
                    .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                    .withFunctionId(launchSpecification.getFunctionId())
                    .withFunctionVersionId(launchSpecification.getVersionId())
                    .withInstanceType(launchSpecification.getInstanceType())
                    .withDeploymentId(instanceRequestEntity.getDeploymentId())
                    .withGpuSpecificationId(instanceRequestEntity.getGpuSpecificationId())
                    .withMetadata(Map.of("previousState", previousState.toString()));

            telemetryEventClient.triggerEvent(List.of(genericMetric));
        } catch (Exception e) {
            log.error("Failed to send telemetry event for request state transition, error - {}",
                    e.getMessage(), e);
        }
    }

    private void sendTelemetryEventForRequestStateTransitionFailure(
            InstanceRequestV2Entity entityBefore,
            InstanceRequestV2Entity entityAfter,
            Exception exception) {
        try {
            ClientRequestDataModel.LaunchSpecification launchSpecification =
                    instanceServiceHelper.getLaunchSpecificationForTelemetry(
                            entityBefore.getRequest());

            Map<String, Object> metadata = new HashMap<>();
            metadata.put("previousState", entityBefore.getState().toString());
            metadata.put("attemptedState", entityAfter.getState().toString());
            metadata.put("errorMessage", exception.getMessage());
            metadata.put("errorType", exception.getClass().getSimpleName());

            GenericMetric genericMetric = new GenericMetric()
                    .withEventName(Events.REQUEST_STATE_TRANSITION_TO_ACTIVE_FAILED.toString())
                    .withRequestId(entityBefore.getRequestId())
                    .withCustomer(entityBefore.getCustomer())
                    // Use the actual persisted state from entityBefore since the transition failed
                    .withRequestState(entityBefore.getState().toString())
                    .withResourceProvider(entityBefore.getResourceProvider())
                    .withNcaId(launchSpecification.getNcaId())
                    .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                    .withFunctionId(launchSpecification.getFunctionId())
                    .withFunctionVersionId(launchSpecification.getVersionId())
                    .withInstanceType(launchSpecification.getInstanceType())
                    .withDeploymentId(entityBefore.getDeploymentId())
                    .withGpuSpecificationId(entityBefore.getGpuSpecificationId())
                    .withError(exception.getMessage())
                    .withMetadata(metadata);

            telemetryEventClient.triggerEvent(List.of(genericMetric));
        } catch (Exception e) {
            log.error("Failed to send telemetry event for request state transition failure, " +
                    "error - {}", e.getMessage(), e);
        }
    }

    private void updateInstance(
            SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            Map<String, Object> auditProps, InstanceRequestV2Entity instanceRequestEntity,
            CloudProvider cloudProvider, ResourceProvider resourceProvider,
            InstanceV2Entity instanceEntity, String clusterName) {

        validateRequestId(instanceEntity, instanceRequestEntity, instanceStatusUpdateRequest, cloudProvider);
        validateZone(instanceEntity, instanceRequestEntity, instanceStatusUpdateRequest, cloudProvider);

        InstanceV2Entity entityBefore = AuditUtils.deepCopyInstanceEntity(instanceEntity);

        // Updating instance state in database
        updateInstanceEntity(instanceEntity,
                instanceStatusUpdateRequest.getInstanceState(),
                instanceStatusUpdateRequest.getRequestState(),
                instanceStatusUpdateRequest.getStatus(), null,
                getInstanceIps(instanceStatusUpdateRequest));

        sendFunctionDeploymentStage(instanceRequestEntity, instanceEntity, instanceStatusUpdateRequest, entityBefore.getInstanceStateName());

        // sending telemetry event for instance state change
        triggerEventForInstanceStateChange(instanceEntity, instanceStatusUpdateRequest,
                Duration.between(instanceRequestEntity.getStatusUpdateTime(),
                        Instant.now()).toSeconds(),
                instanceRequestEntity, cloudProvider, resourceProvider,
                clusterName);

        // Sending audit event
        sendAuditEventForInstanceStateUpdate(auditProps, instanceEntity, entityBefore);
    }

    private void handelInstanceUpdateForClosedRequest(
            InstanceRequestV2Entity instanceRequestEntity, String instanceId,
            SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            CloudProvider cloudProvider) {

        // Handling new instance registration
        if (instanceStatusUpdateRequest.getStatus().equals(FULFILLED)) {

            validateInstanceFulfillmentRequest(instanceStatusUpdateRequest,
                                               Set.of(STARTING, RUNNING), instanceId,
                                               instanceRequestEntity, cloudProvider);

            sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestEntity,
                                                 instanceStatusUpdateRequest,
                                                 "Trying to register new instance but request is closed",
                                                 cloudProvider);
            throw new PreConditionFailedException(String.format("Instance request with id %s is closed",
                    instanceRequestEntity.getRequestId()));
        }

        String errMsg = String.format("Specified status %s for instance with id %s is invalid",
                instanceStatusUpdateRequest.getStatus(), instanceId);
        logErrorMessage(instanceId, instanceRequestEntity.getRequestId(),
                instanceStatusUpdateRequest, errMsg);

        throw new IcmsBadRequestException(errMsg);
    }

    // Handles terminated state update irrespective of requestState
    private void handleInstanceTerminationUpdate(
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull String instanceId,
            @NotNull Map<String, Object> auditProps,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @Nullable CloudProvider cloudProvider,
            @Nullable ResourceProvider resourceProvider,
            @Nullable String clusterName) {

        validateInstanceTerminationRequest(instanceStatusUpdateRequest, instanceId,
                                           instanceRequestEntity, cloudProvider);

        Optional<InstanceV2Entity> optionalInstanceEntity =
                instanceV2Repository.findInstanceByCustomerAndId(instanceRequestEntity.getCustomer(),
                        instanceId);
        if (optionalInstanceEntity.isEmpty()) {
            insertTerminatedState(instanceStatusUpdateRequest, instanceId, auditProps,
                    instanceRequestEntity, cloudProvider, resourceProvider, clusterName);
            return;
        }

        InstanceV2Entity instanceEntity = optionalInstanceEntity.get();

        /*
         If cloud health check of zone fails then instance is marked as terminated and put it for termination in SNS/SQS
         When zone come up it will read the request and terminate that instance and send status update as instance-terminated-by-user.
         We want to avoid the status update as the instance is already marked as instance-terminated-by-service by cloudHealthCheck
         */
        if (isInstanceAlreadyTerminated(instanceEntity)) {
            log.info("InstanceId {}: Instance is already marked as terminated, status-code: {}",
                    instanceId, instanceEntity.getRequestStatusCode());
            return;
        }

        validateRequestId(instanceEntity, instanceRequestEntity, instanceStatusUpdateRequest, cloudProvider);
        validateZone(instanceEntity, instanceRequestEntity, instanceStatusUpdateRequest, cloudProvider);

        InstanceV2Entity entityBefore = AuditUtils.deepCopyInstanceEntity(instanceEntity);

        // Update instance state in database
        updateInstanceEntity(instanceEntity,
                instanceStatusUpdateRequest.getInstanceState(),
                instanceStatusUpdateRequest.getRequestState(),
                instanceStatusUpdateRequest.getStatus(),
                instanceStatusUpdateRequest.getHealthInfo(),
                instanceEntity.getInstanceIps());

        sendFunctionDeploymentStage(instanceRequestEntity, instanceEntity, instanceStatusUpdateRequest, entityBefore.getInstanceStateName());

        // sending event for instance state change(terminated)
        triggerEventForInstanceStateChange(instanceEntity, instanceStatusUpdateRequest,
                Duration.between(entityBefore.getInstanceUpdateTime(),
                        Instant.now()).toSeconds(),
                instanceRequestEntity, cloudProvider, resourceProvider,
                clusterName);

        // Sending GPU usage event for terminated instance
        sendGpuUsageEventForTerminatedInstance(instanceEntity);

        sendAuditEventForInstanceStateUpdate(auditProps, instanceEntity, entityBefore);
    }

    private void sendGpuUsageEventForTerminatedInstance(InstanceV2Entity entity) {
        instanceServiceHelper.gpuUsageEventForTerminatedInstance(entity);
    }

    // This function will insert terminated state as FIRST state in DB irrespective of request state
    private void insertTerminatedState(
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull String instanceId,
            @NotNull Map<String, Object> auditProps,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @Nullable CloudProvider cloudProvider,
            @Nullable ResourceProvider resourceProvider,
            @Nullable String clusterName) {

        // Validating reservationId for new instance report
        Optional<ReservationEntity> optionalReservationEntity = validateReservationIdForNewInstanceCreation(
                instanceStatusUpdateRequest, instanceRequestEntity, instanceId, cloudProvider);

        InstanceV2Entity instanceEntity = getConfiguredInstanceEntity(
                instanceStatusUpdateRequest, instanceRequestEntity, instanceId, resourceProvider, cloudProvider, optionalReservationEntity);

        try {
            log.info("Inserting terminated as first state for instance-id {} request-id {}",
                    instanceId, instanceEntity.getRequestId());
            instanceV2Repository.insert(instanceEntity);
        } catch (Exception e) {
            log.error(
                    "Exception while inserting the info of instance with id {} in database, error - {}",
                    instanceId, e.getMessage(), e);
            // rethrowing same exception as it is handled in Global error handler
            throw e;
        }

        sendFunctionDeploymentStage(instanceRequestEntity, instanceEntity, instanceStatusUpdateRequest, null);

        // sending event for terminated as first state
        triggerEventForInstanceStateChange(instanceEntity, instanceStatusUpdateRequest, 0,
                instanceRequestEntity, cloudProvider, resourceProvider,
                clusterName);

        sendAuditEventForInstanceStateUpdate(auditProps, instanceEntity,
                InstanceV2Entity.getEmptyEntity());
    }

    private boolean isInstanceAlreadyTerminated(@NotNull InstanceV2Entity instanceEntity) {
        return instanceEntity.getInstanceStateName()
                .equals(SpotInstanceInternalState.TERMINATED);
    }

    private InstanceV2Entity getConfiguredInstanceEntity(
            @NotNull SpotInstanceStatusUpdateRequest updateRequest,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull String instanceId,
            @Nullable ResourceProvider resourceProvider,
            @Nullable CloudProvider cloudProvider, Optional<ReservationEntity> optionalReservationEntity) {

        Instant instant = Instant.now();
        ClientRequestDataModel.LaunchSpecification requestInfo = instanceServiceHelper.parseRequestInfo(
                instanceRequestEntity.getRequest()).getLaunchSpecification();

        Optional<ClusterEntity> clusterEntity = clusterRepository.getClusterInfoByClusterId(
                updateRequest.getPlacement().getAvailabilityZone(), true);

        InstanceV2Entity instanceV2Entity = InstanceV2Entity.builder()
                .instanceId(instanceId)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(instant))
                .requestId(instanceRequestEntity.getRequestId())
                .customer(instanceRequestEntity.getCustomer())
                .instanceStateCode(
                        SpotInstanceInternalState.getStateCode(updateRequest.getInstanceState()))
                .instanceStateName(updateRequest.getInstanceState())
                .requestStatusCode(updateRequest.getStatus())
                .requestState(updateRequest.getRequestState())
                .requestStatusMessage(
                        String.format("Instance status updated to %s", updateRequest.getStatus()))
                .requestStatusUpdateTime(instant)
                .instanceUpdateTime(instant)
                .imageId(updateRequest.getImageId())
                .zone(updateRequest.getPlacement().getAvailabilityZone())
                .resourceProvider(resourceProvider)
                .ncaId(requestInfo.getNcaId()).instanceType(requestInfo.getInstanceType())
                .backend(requestInfo.getBackend()).gpu(requestInfo.getGpu())
                .instanceIps(getInstanceIps(updateRequest))
                .requestRawData(instanceRequestEntity.getRequest())
                .region(clusterEntity.isPresent() ? clusterEntity.get().getRegion() : null)
                .attributes(instanceRequestEntity.getAttributes())
                .customAttributes(instanceRequestEntity.getCustomAttributes())
                .cloudProvider(cloudProvider != null ? cloudProvider.toString() : null)
                .reservationId(updateRequest.getReservationId())
                .gpuCountPerInstance(instanceRequestEntity.getGpuCountPerInstance())
                .deploymentId(getDeploymentId(instanceRequestEntity, requestInfo))
                .gpuSpecificationId(getGpuSpecificationId(instanceRequestEntity, requestInfo))
                .build();

        updateErrorInfo(instanceV2Entity, updateRequest.getHealthInfo());
        updateCapacityInfo(instanceV2Entity, updateRequest, optionalReservationEntity);
        return instanceV2Entity;
    }

    private UUID getDeploymentId(
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull ClientRequestDataModel.LaunchSpecification requestInfo) {
        return instanceRequestEntity.getDeploymentId() != null
                ? instanceRequestEntity.getDeploymentId()
                : requestInfo.getDeploymentId();
    }

    private UUID getGpuSpecificationId(
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull ClientRequestDataModel.LaunchSpecification requestInfo) {
        return instanceRequestEntity.getGpuSpecificationId() != null
                ? instanceRequestEntity.getGpuSpecificationId()
                : requestInfo.getGpuSpecificationId();
    }


    /**
     * Sends the function deployment stage event based on the instance state and status.
     * <p>
     * This method determines the appropriate deployment stage event to send based on the
     * current state of the instance and its status. It avoids sending events if the
     * instance state has not changed.
     * </p>
     *
     * @param instanceRequestEntity The request entity associated with the instance.
     * @param instanceEntity The instance entity whose state is being updated.
     * @param statusUpdateRequest The request containing the updated status of the instance.
     */
    private void sendFunctionDeploymentStage(
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull InstanceV2Entity instanceEntity,
            @NotNull SpotInstanceStatusUpdateRequest statusUpdateRequest,
            @Nullable SpotInstanceInternalState currentState ) {
        String event = null;

        try {
            // Avoid sending events if the instance state has not changed
            if (currentState != null &&
                    currentState.equals(statusUpdateRequest.getInstanceState())) {
                return;
            }

            // Determine the event based on the instance status and state
            if (SpotInstanceStatus.FULFILLED.equals(statusUpdateRequest.getStatus())) {
                if (STARTING.equals(statusUpdateRequest.getInstanceState())) {
                    // Pending event is discarded for now.
                    //event = FndsStages.STAGE_PENDING.toString();
                    return;
                } else { // If the instance state is RUNNING
                    event = FndsStages.STAGE_READY.toString();
                }
            } else {
                event = FndsStages.STAGE_DESTROYED.toString();
            }

            // Send the event if determined
            if (event != null) {
                functionDeploymentStagesService.sendFunctionDeploymentStage(instanceRequestEntity,
                        instanceEntity,
                        event);
            }
        } catch (Exception e) {
            log.error("Exception on sending function deployment stage {}", e.getMessage(), e);
        }
    }


    private void triggerEventForInstanceStateChange(
            @NotNull InstanceV2Entity instanceEntity,
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            long timeDifference,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @Nullable CloudProvider cloudProvider,
            @Nullable ResourceProvider resourceProvider,
            @Nullable String clusterName) {

        String terminationCause = instanceStatusUpdateRequest.getTerminationCause();
        Map<String, Object> metadata = new HashMap<>();
        SpotInstanceHeathInfo healthInfo = instanceStatusUpdateRequest.getHealthInfo();
        if (healthInfo != null && !StringUtils.isEmpty(healthInfo.getErrorLog())) {
            metadata.put(EventMetaData.ERROR_LOG.getName(),
                    StringUtils.substring(healthInfo.getErrorLog(), 0, 250));
        }
        if (healthInfo != null && !StringUtils.isEmpty(healthInfo.getErrorSource())) {
            metadata.put(EventMetaData.ERROR_SOURCE.getName(), healthInfo.getErrorSource());

            // If error source is present then send metrics for ncaId, errorSource
            instanceServiceHelper.sendInstanceTaskError(instanceEntity.getNcaId(),
                    healthInfo.getErrorSource());
        }
        // Since system failures are only available in telemetry
        // let's show most of the system error in telemetry
        if (!StringUtils.isEmpty(instanceStatusUpdateRequest.getSystemFailure())) {
            metadata.put(EventMetaData.DOWNSTREAM_SYSTEM_FAILURE.getName(),
                    StringUtils.substring(instanceStatusUpdateRequest.getSystemFailure(),
                            0, 700));
            log.info(String.format(
                    "Downstream system failure for instance id %s with request id %s is %s",
                    instanceEntity.getInstanceId(),
                    instanceEntity.getRequestId(),
                    instanceStatusUpdateRequest.getSystemFailure()));
        }

        var instanceIps = getInstanceIps(instanceStatusUpdateRequest);
        if (instanceIps != null && !instanceIps.isEmpty()) {
            metadata.put(EventMetaData.RESOURCE_IPS.getName(), String.join(",", instanceIps));
        }

        ClientRequestDataModel.LaunchSpecification launchSpecification = instanceServiceHelper.getLaunchSpecificationForTelemetry(
                instanceRequestEntity.getRequest());

        // Adding instanceType in metadata for backward compatibility
        if (!StringUtils.isBlank(launchSpecification.getInstanceType())) {
            metadata.put(EventMetaData.INSTANCE_TYPE.getName(),
                         launchSpecification.getInstanceType());
        }

        // Add task details in metadata
        instanceServiceHelper.updateMetadataForTask(metadata, instanceRequestEntity, launchSpecification);

        GenericMetric genericMetric = new GenericMetric().withCustomer(
                        instanceRequestEntity.getCustomer()).withCloudProvider(cloudProvider)
                .withResourceProvider(resourceProvider).withMetadata(metadata)
                .withInstanceId(instanceEntity.getInstanceId())
                .withRequestId(instanceEntity.getRequestId())
                .withInstanceState(instanceEntity.getInstanceStateName().getStateName())
                .withRequestState(instanceEntity.getRequestState().toString())
                .withNcaId(launchSpecification.getNcaId())
                .withFunctionId(launchSpecification.getFunctionId())
                .withFunctionName(launchSpecification.getFunctionName())
                .withFunctionVersionId(launchSpecification.getVersionId())
                .withInstanceType(launchSpecification.getInstanceType())
                .withGpuName(instanceEntity.getGpu())
                .withClusterName(clusterName)
                .withTaskId(getStringValue(instanceRequestEntity.getTaskId()))
                .withTaskName(launchSpecification.getTaskName())
                .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                .withZoneName(instanceEntity.getZone())
                .withDeploymentId(instanceRequestEntity.getDeploymentId())
                .withGpuSpecificationId(instanceRequestEntity.getGpuSpecificationId())
                .withReservationId(getStringValueOfUuid(instanceEntity.getReservationId()))
                .withCapacityType(instanceEntity.getCapacityType())
                .withInstanceExpirationTime(instanceEntity.getInstanceExpirationTime())
                .withRegionName(instanceEntity.getRegion());

        if (FULFILLED.equals(instanceStatusUpdateRequest.getStatus())) {

            // Instance state starting
            if (STARTING.equals(instanceStatusUpdateRequest.getInstanceState())) {
                genericMetric.withEventName(Events.STARTING_INSTANCE.toString())
                        .withInstanceWaitingTime(timeDifference);

                // Instance state running
            } else if (RUNNING.equals(instanceStatusUpdateRequest.getInstanceState())) {
                genericMetric.withEventName(Events.STARTED_RUNNING_INSTANCE.toString())
                        .withInstanceWaitingTime(timeDifference);
            }

        } else {
            if (Objects.equals(instanceStatusUpdateRequest.getStatus(),
                    SpotInstanceStatus.INSTANCE_TERMINATED_NO_CAPACITY)) {

                genericMetric.withEventName(Events.INSTANCE_TERMINATED_BY_ZONE.toString())
                        .withReasonForTermination(
                                SpotInstanceStatus.INSTANCE_TERMINATED_NO_CAPACITY.toString())
                        .withInstanceLifeTime(timeDifference);
            } else if (Objects.equals(instanceStatusUpdateRequest.getStatus(),
                    SpotInstanceStatus.INSTANCE_TERMINATED_BY_USER)) {

                genericMetric.withEventName(Events.INSTANCE_TERMINATED_BY_USER.toString())
                        .withReasonForTermination(
                                SpotInstanceStatus.INSTANCE_TERMINATED_BY_USER.toString())
                        .withInstanceLifeTime(timeDifference);

            } else if (Objects.equals(instanceStatusUpdateRequest.getStatus(),
                    SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE)) {

                genericMetric.withEventName(Events.INSTANCE_TERMINATED_BY_ZONE.toString())
                        .withReasonForTermination(
                                SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE.toString())
                        .withInstanceLifeTime(timeDifference);
            }
            if (!StringUtils.isEmpty(terminationCause)) {
                genericMetric.getMetadata()
                        .put(EventMetaData.TERMINATION_CAUSE.getName(),
                             instanceStatusUpdateRequest.getTerminationCause());
            }
            log.info("Instance with id {} is terminated with {} status with {} cause for {}"
                            + " cloud provider", instanceEntity.getInstanceId(),
                    instanceStatusUpdateRequest.getStatus(),
                    StringUtils.defaultIfBlank(terminationCause, "-"), cloudProvider);
        }
        telemetryEventClient.triggerEvent(List.of(genericMetric));
        // Send latest instance state telemetry alongside state-change events
        instanceServiceHelper.sendLatestInstanceStateEvent(instanceEntity);
    }

    private void updateInstanceEntity(
            @NotNull InstanceV2Entity instanceEntity,
            @NotNull SpotInstanceInternalState instanceInternalState,
            @Nullable SpotInstanceRequestState instanceRequestState,
            @Nullable SpotInstanceStatus instanceStatus,
            @Nullable SpotInstanceHeathInfo healthInfo,
            @Nullable Set<String> instanceIps) {

        instanceEntity.setInstanceStateCode(
                SpotInstanceInternalState.getStateCode(instanceInternalState));
        instanceEntity.setInstanceStateName(instanceInternalState);
        instanceEntity.setRequestStatusCode(instanceStatus);
        instanceEntity.setRequestStatusMessage(
                String.format("Instance status updated to %s", instanceStatus));
        instanceEntity.setRequestStatusUpdateTime(Instant.now());
        instanceEntity.setInstanceUpdateTime(Instant.now());
        instanceEntity.setRequestState(instanceRequestState);
        updateErrorInfo(instanceEntity, healthInfo);

        if (instanceIps != null && !instanceIps.isEmpty()) {
            instanceEntity.setInstanceIps(instanceIps);
        }

        try {
            instanceV2Repository.update(instanceEntity);
        } catch (Exception e) {
            log.error(
                    "Exception while updating the info of instance with id {} in database, error - {}",
                    instanceEntity.getInstanceId(), e.getMessage(), e);
            throw e;
        }
    }

    private InstanceV2Entity updateErrorInfo(@Nullable InstanceV2Entity instanceEntity,
                                                 @Nullable SpotInstanceHeathInfo healthInfo) {
        if (instanceEntity != null && healthInfo != null) {
            if (!StringUtils.isEmpty(healthInfo.getErrorLog())) {
                instanceEntity.setErrorLog(healthInfo.getErrorLog());
            }
            if (!StringUtils.isEmpty(healthInfo.getErrorSource())) {
                instanceEntity.setErrorSource(healthInfo.getErrorSource());
            }
        }
        return instanceEntity;
    }


    private void validateInstanceTerminationRequest(
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull String instanceId,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull CloudProvider cloudProvider) {
        validateOriginalAction(instanceStatusUpdateRequest.getAction(),
                               List.of(SpotInstanceRequestAction.TERMINATE_INSTANCES), instanceId,
                               instanceRequestEntity, instanceStatusUpdateRequest, cloudProvider);

        validateRequestState(SpotInstanceRequestState.CLOSED, instanceId, instanceRequestEntity,
                             instanceStatusUpdateRequest, cloudProvider);

        validateInstanceState(instanceRequestEntity, instanceStatusUpdateRequest,
                              Set.of(SpotInstanceInternalState.TERMINATED),
                              instanceId, cloudProvider);
    }

    private void validateInstanceFulfillmentRequest(
            SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            Set<SpotInstanceInternalState> instanceInternalState, String instanceId,
            InstanceRequestV2Entity instanceRequestEntity,
            @NotNull CloudProvider cloudProvider) {
        validateOriginalAction(instanceStatusUpdateRequest.getAction(),
                               List.of(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES,
                                       SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES_FOR_TASK,
                                       SpotInstanceRequestAction.REQUEST_INSTANCES,
                                       SpotInstanceRequestAction.REQUEST_INSTANCES_FOR_TASK),
                               instanceId, instanceRequestEntity,
                               instanceStatusUpdateRequest, cloudProvider);
        validateRequestState(SpotInstanceRequestState.ACTIVE, instanceId, instanceRequestEntity,
                             instanceStatusUpdateRequest, cloudProvider);

        validateInstanceState(instanceRequestEntity, instanceStatusUpdateRequest,
                              instanceInternalState, instanceId, cloudProvider);
    }

    private void validateOriginalAction(
            @NotNull SpotInstanceRequestAction actual,
            @NotNull List<SpotInstanceRequestAction> expected,
            @Nullable String instanceId,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull CloudProvider cloudProvider) {
        if (expected.contains(actual)) {
            return;
        }
        String msg = String.format("Invalid action %s for updating instance status",
                actual.getRequestAction());
        logErrorMessage(instanceId, instanceRequestEntity.getRequestId(), instanceStatusUpdateRequest, msg);

        sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestEntity, instanceStatusUpdateRequest,
                                             msg, cloudProvider);
        throw new PreConditionFailedException(msg);
    }

    private void validateRequestState(
            @NotNull SpotInstanceRequestState expected,
            @NotNull String instanceId,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceStatusUpdateRequest statusUpdateRequest,
            @NotNull CloudProvider cloudProvider) {
        if (statusUpdateRequest.getRequestState() == expected) {
            return;
        }
        String msg = String.format(
                "Invalid request state %s for updating instance status, provided state %s expected state %s",
                statusUpdateRequest.getRequestState(), statusUpdateRequest.getRequestState(),
                expected);
        sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestEntity, statusUpdateRequest,
                                             msg, cloudProvider);

        throw new PreConditionFailedException(msg);
    }

    private void validateRequestId(
            @NotNull InstanceV2Entity instanceEntity,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceStatusUpdateRequest statusUpdateRequest,
            @NotNull CloudProvider cloudProvider) {
        if (instanceEntity.getRequestId().equals(instanceRequestEntity.getRequestId())) {
            return;
        }

        String logErrorMsg = String.format(
                "Invalid requestId for updating instance status, providedRequestId %s expectedRequestId %s",
                instanceRequestEntity.getRequestId(), instanceEntity.getRequestId());
        logErrorMessage(instanceEntity.getInstanceId(), instanceRequestEntity.getRequestId(),
                statusUpdateRequest, logErrorMsg);

        String errMsg = String.format("Invalid requestId %s for updating instance status",
                instanceRequestEntity.getRequestId());
        sendPreConditionFailedTelemetryEvent(instanceEntity.getInstanceId(), instanceRequestEntity,
                                             statusUpdateRequest, errMsg, cloudProvider);

        throw new PreConditionFailedException(errMsg);
    }

    private void validateZone(
            @NotNull InstanceV2Entity instanceEntity,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull CloudProvider cloudProvider) {
        String zone = instanceStatusUpdateRequest.getPlacement().getAvailabilityZone();

        if (instanceEntity.getZone().equals(zone)) {
            return;
        }

        String logErrorMsg = String.format(
                "Invalid zone for updating instance status, provided zone %s expectedZone %s",
                zone, instanceEntity.getZone());
        logErrorMessage(instanceEntity.getInstanceId(), instanceRequestEntity.getRequestId(),
                instanceStatusUpdateRequest, logErrorMsg);

        String errMsg = String.format("Invalid zone %s for updating instance status", zone);
        sendPreConditionFailedTelemetryEvent(instanceEntity.getInstanceId(), instanceRequestEntity,
                                             instanceStatusUpdateRequest, errMsg, cloudProvider);

        throw new PreConditionFailedException(errMsg);
    }

    private void logInstanceUpdateInfo(
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @Nullable String instanceId,
            @Nullable String instanceRequestId,
            @Nullable String clientId,
            @Nullable String logMsg) {
        try {
            log.info(
                    "{} From cluster with id - {} Received request to update the status of instance with instanceId - {},"
                            + " requestId - {}, updateRequest- {}", logMsg, clientId, instanceId,
                    instanceRequestId,
                    GsonCompatMapper.toJson(ignoreSensitiveInformation(instanceStatusUpdateRequest)));
        } catch (Exception exception) {
            log.error("Failed to log incoming SpotInstanceStatusUpdateRequest, error: {}, exception: ",
                    exception.getMessage(), exception);
        }
    }

    private void sendPreConditionFailedTelemetryEvent(
            @NotNull String instanceId,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull SpotInstanceStatusUpdateRequest statusUpdateRequest,
            @Nullable String errMsg,
            @NotNull CloudProvider cloudProvider) {
        logErrorMessage(instanceId, instanceRequestEntity.getRequestId(), statusUpdateRequest,
                        String.format("PRE_CONDITION_FAILED: %s", errMsg));
        try {
            ClientRequestDataModel.LaunchSpecification launchSpecification = instanceServiceHelper.getLaunchSpecificationForTelemetry(
                    instanceRequestEntity.getRequest());

            String clusterId = null;
            if (statusUpdateRequest.getPlacement() != null) {
                clusterId = statusUpdateRequest.getPlacement().getAvailabilityZone();
            }

            GenericMetric metric = new GenericMetric()
                    .withEventName(Events.PRE_CONDITION_FAILED.toString())
                    .withInstanceId(instanceId)
                    .withInstanceType(launchSpecification.getInstanceType())
                    .withGpuName(launchSpecification.getGpu())
                    .withRequestId(instanceRequestEntity.getRequestId())
                    .withCustomer(instanceRequestEntity.getCustomer())
                    .withInstanceCount(instanceRequestEntity.getInstanceCount())
                    .withResourceProvider(instanceRequestEntity.getResourceProvider())
                    .withCloudProvider(cloudProvider)
                    .withRequestState(instanceRequestEntity.getState().toString())
                    .withClusterId(clusterId)
                    .withError(errMsg)
                    .withFunctionId(launchSpecification.getFunctionId())
                    .withFunctionName(launchSpecification.getFunctionName())
                    .withFunctionVersionId(launchSpecification.getVersionId())
                    .withTaskId(launchSpecification.getTaskId())
                    .withTaskName(launchSpecification.getTaskName())
                    .withNcaId(launchSpecification.getNcaId())
                    .withNcaIdPartnerName(launchSpecification.getNcaIdAccountName())
                    .withDeploymentId(instanceRequestEntity.getDeploymentId())
                    .withGpuSpecificationId(instanceRequestEntity.getGpuSpecificationId())
                    .withCapacityType(statusUpdateRequest.getCapacityType());

            Map<String, Object> metadata = new HashMap<>();
            metadata.put(EventMetaData.REQUEST_BODY.getName(),
                         GsonCompatMapper.toJson(ignoreSensitiveInformation(statusUpdateRequest)));
            metadata.put(REQUEST_STATUS.getName(), instanceRequestEntity.getStatusCode());
            metric.withMetadata(metadata);

            telemetryEventClient.triggerEvent(List.of(metric));
        } catch (Exception exception) {
            log.error(
                    "Failed to send telemetry event for precondition failure error - {}, exception",
                    exception.getMessage(), exception);
        }
    }

    private void logErrorMessage(
            @Nullable String instanceId,
            @Nullable String requestId,
            @NotNull SpotInstanceStatusUpdateRequest statusUpdateRequest,
            @Nullable String errMsg) {
        log.error("error - {}, instanceId - {}, requestId - {}, statusUpdateRequest - {}", errMsg,
                instanceId, requestId,
                GsonCompatMapper.toJson(ignoreSensitiveInformation(statusUpdateRequest)));

    }

    private void sendAuditEventForInstanceStateUpdate(
            @NotNull Map<String, Object> auditProps,
            @NotNull InstanceV2Entity entityAfter,
            @NotNull InstanceV2Entity entityBefore) {

        SpotInstanceInternalState instanceInternalState = entityAfter.getInstanceStateName();
        SpotInstanceInternalState instanceInternalStateBefore = entityBefore.getInstanceStateName();

        log.info("InstanceUpdateService: instanceId: {} previousInstanceState: {} updatedInstanceState: {} ",
                entityAfter.getInstanceId(), instanceInternalStateBefore, instanceInternalState);
        if (entityBefore.getInstanceStateName() == instanceInternalState) {
            log.debug(
                    "For instance-id: {} state: {} is already present hence avoiding populating audit logs",
                    entityAfter.getInstanceId(), instanceInternalState);
            return;
        }

        if (instanceInternalState == STARTING) {
            populateAuditValuesForStartingInstance(auditProps, entityAfter.getInstanceId());
        } else if (instanceInternalState == RUNNING) {
            populateAuditValuesForRunningInstance(auditProps, entityAfter.getInstanceId());
        } else if (instanceInternalState == TERMINATED) {
            populateAuditValuesForTerminateInstance(auditProps, entityAfter.getInstanceId());
        }

        auditService.sendAuditEventForInstance(auditProps, entityBefore, entityAfter);
    }

    private void validateInstanceState(
            @NotNull InstanceRequestV2Entity instanceRequestV2Entity,
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull Set<SpotInstanceInternalState> expected,
            @NotNull String instanceId,
            @NotNull CloudProvider cloudProvider) {

        try {
            InstanceStateUtils.validateInstanceState(
                    instanceStatusUpdateRequest.getInstanceState(),
                    expected, instanceId,
                    instanceRequestV2Entity.getRequestId());
        } catch (PreConditionFailedException preConditionFailedException) {
            // Sending telemetry event for 412 error
            sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestV2Entity,
                                                 instanceStatusUpdateRequest,
                                                 preConditionFailedException.getBody().getDetail(), cloudProvider);

            throw preConditionFailedException;
        }
    }

    private void validateStateTransition(
            @NotNull InstanceRequestV2Entity instanceRequestV2Entity,
            @NotNull InstanceV2Entity instanceV2Entity,
            @NotNull SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest,
            @NotNull String instanceId,
            @NotNull CloudProvider cloudProvider) {
        try {
            validateInstanceStateTransition(instanceV2Entity.getInstanceStateName(),
                    instanceStatusUpdateRequest.getInstanceState(),
                    instanceId, instanceRequestV2Entity.getRequestId());
        } catch (PreConditionFailedException preConditionFailedException) {

            // Sending telemetry event for 412 error
            sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestV2Entity,
                                                 instanceStatusUpdateRequest,
                                                 preConditionFailedException.getBody().getDetail(), cloudProvider);

            throw preConditionFailedException;
        }
    }

    private void updateCapacityInfo(@NotNull InstanceV2Entity instanceV2Entity,
                                    @NotNull SpotInstanceStatusUpdateRequest updateRequest,
                                    @NotNull Optional<ReservationEntity> optionalReservationEntity) {

        // Setting capacityType as default value is SPOT
        instanceV2Entity.setCapacityType(updateRequest.getCapacityType().toString());

        // Setting reservation details if provided by NVCFGRAM
        if (optionalReservationEntity.isPresent()) {
            instanceV2Entity.setReservationId(optionalReservationEntity.get().getReservationId());

            // Setting instanceExpirationTime only for RESERVED_BACKUP instances
            if (updateRequest.getCapacityType().equals(CapacityType.RESERVED_BACKUP)) {
                instanceV2Entity.setInstanceExpirationTime(instanceLifecycleHelper.getReservationTtl(optionalReservationEntity.get().getEndTime()));
            }
        }
    }

    /**
     * Validate reservationId when an instance is reported for the first time. The first reported state could be:
     * <br>
     * 1. starting/running<br>
     * 2. terminated
     *
     * @param statusUpdateRequest status update request
     * @return Optional<ReservationEntity>
     */
    private Optional<ReservationEntity> validateReservationIdForNewInstanceCreation(@NotNull SpotInstanceStatusUpdateRequest statusUpdateRequest,
                                                                                    @NotNull InstanceRequestV2Entity instanceRequestEntity,
                                                                                    @NotNull String instanceId,
                                                                                    @NotNull CloudProvider cloudProvider) {

        try {

            // Validate and get ReservationEntity
            Optional<ReservationEntity> optionalReservationEntity = reservationCapacityValidationHelper.validateAndGetReservationEntity(statusUpdateRequest.getReservationId());

            // Validate RESERVED_BACKUP capacity to prevent over-allocation in reservation
            if (optionalReservationEntity.isPresent() && statusUpdateRequest.getCapacityType().equals(CapacityType.RESERVED_BACKUP)) {
                reservationCapacityValidationHelper.validateReservationBackupCapacityForInstanceStateUpdate(
                        optionalReservationEntity.get(), instanceRequestEntity);
            }

            return optionalReservationEntity;

        } catch (PreConditionFailedException preConditionFailedException) {
            // Sending telemetry event for 412 error
            sendPreConditionFailedTelemetryEvent(instanceId, instanceRequestEntity,
                                                 statusUpdateRequest,
                                                 preConditionFailedException.getBody().getDetail(), cloudProvider);

            throw preConditionFailedException;
        }
    }

    private void validateCapacityType(SpotInstanceStatusUpdateRequest updateRequest) {

        // ReservationId should be provided when capacityType: [RESERVED, RESERVED_BACKUP]
        if ((CapacityType.RESERVED.equals(updateRequest.getCapacityType()) ||
                CapacityType.RESERVED_BACKUP.equals(updateRequest.getCapacityType()))
                && updateRequest.getReservationId() == null) {

            String errMsg = "ReservationId must be provided for capacityType: [RESERVED, RESERVED_BACKUP]";
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }
    }
}
