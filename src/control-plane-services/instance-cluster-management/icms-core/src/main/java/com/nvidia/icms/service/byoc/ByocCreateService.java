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
package com.nvidia.icms.service.byoc;

import static com.nvidia.icms.service.FunctionBillingService.BILLING_NCA_ID_ENV_VAR;
import static com.nvidia.icms.service.InstanceServiceHelper.parseEnvironmentVariable;
import static com.nvidia.icms.service.byoc.ByocServiceHelper.getUuid;
import static com.nvidia.icms.util.InstanceServiceUtil.isRequestForTask;
import static com.nvidia.icms.util.InstanceServiceUtil.isTargetingEnabled;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.ACCOUNT_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CACHE_HANDLE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CACHE_SIZE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_GROUP_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CONTAINER_IMAGE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CREATION_QUEUE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.HELM_CHART;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.INSTANCE_COUNT;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.INSTANCE_TYPE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.IS_CACHE_ARTIFACTS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.IS_CLUSTER_GROUP;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.MAX_QUEUED_DURATION;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.MAX_RUNTIME_DURATION;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.RESULT_HANDLING_STRATEGY;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.ROUNDED_OF_CACHE_SIZE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TASK_ID;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TERMINATION_GRACE_PERIOD_DURATION;
import static com.nvidia.icms.util.InstanceServiceUtil.extractAttributes;
import static com.nvidia.icms.util.InstanceServiceUtil.getStringValue;
import static com.nvidia.icms.util.audit.AuditUtils.deepCopyInstanceRequestEntity;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForCreateInstanceRequest;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.sqs.model.TaskDetails;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel.ByocLaunchSpecification;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.outbound.sqs.model.GdnLaunchSpecification;
import com.nvidia.icms.configuration.bean.ComputePlatform;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.extensions.api.InstanceCreationService;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.FunctionBillingService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.LaunchSpecificationService;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ByocCreateService {

    private final InstanceRequestV2Repository instanceRequestV2Repository;
    private final ByocConfigurationProperties byocConfigurationProperties;
    private final TelemetryEventClient telemetryEventClient;
    private final AppAuditService auditService;
    private final InstanceServiceHelper instanceServiceHelper;
    private final InstanceCreationService instanceCreationService;
    private final ByocServiceHelper byocServiceHelper;
    private final FunctionBillingService functionBillingService;
    private final ByocMessageGenerator byocMessageGenerator;
    private final LaunchSpecificationService launchSpecificationService;
    private final ComputePlatformService computePlatformService;

    /*
    If non-BYOC clusters are targeted then nonByocDestinations won't be empty and requestInstanceDestinations will be empty
    If NVCA clusters are targeted then requestInstanceDestinations won't be empty and nonByocDestinations will be empty
     */
    public @NotNull CreateSpotInstancesResponse processCreateRequest(
            @NotNull String customer,
            @NotNull Set<RequestInstanceDestination> destinations,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Map<String, Object> auditProps) {

        log.info("{}: Processing instance request for cluster groups {}",
                 instanceRequest.getLoggingId(),
                 String.join(", ",
                             destinations.stream().map(
                                             r -> r.getClusterGroupName() + " - " +
                                                     (r.isReserved() ? "Reserved"
                                                             : computePlatformService.platformFor(r.getClusterGroupName()).map(ComputePlatform::getName).orElse("BYOC")))
                                     .collect(Collectors.toSet())));

        // 1. Get sub from the token
        // 2. Create Queue message
        // 3. Store message to cassandra
        // 4. Send message to SQS

        UUID requestId = getUuid();
        Map<String, String> envVars =
                parseEnvironmentVariable(instanceRequest.getEnvironment());
        UUID functionIdUUID = instanceRequest.getFunctionId();
        UUID functionVersionIdUUID = instanceRequest.getFunctionVersionId();
        // TODO: Evaluate if this is expected flow:
        //  We send modified(billingNcaId injected) environment to non-BYOC but for BYOC we send environment accepcted from NVCF
        //  If this is expected, move billingId injection logic to sendNonByocSqsMessages function
        setBillingNcaId(functionIdUUID, functionVersionIdUUID, envVars, instanceRequest);

        GdnLaunchSpecification gdnLaunchSpecification =
                launchSpecificationService.resolveGdnLaunchSpecification(instanceRequest, envVars, auditProps);

        sendSqsMessages(customer, destinations, instanceRequest, envVars, requestId, gdnLaunchSpecification);

        String request =
                instanceServiceHelper.generateRequestInfo(instanceRequest, customer, requestId.toString(),
                                                      null);
        Set<String> attributes = extractAttributes(instanceRequest);
        InstanceRequestV2Entity instanceRequestEntity = byocMessageGenerator.generateInstanceRequestEntity(
                customer, instanceRequest,
                requestId, request, attributes);

        // If request is for task then set task specific fields in entity
        if (instanceRequest.getTaskId() != null && StringUtils.isNotBlank(
                instanceRequest.getTaskId().toString())) {
            instanceRequestEntity.setTaskId(instanceRequest.getTaskId());
            instanceRequestEntity.setMaxQueuedDuration(instanceRequest.getMaxQueuedDuration().toString());
            instanceRequestEntity.setAccountName(instanceRequest.getAccountName());
        }
        try {
            setGpuCountPerInstance(destinations, instanceRequestEntity);

            instanceRequestV2Repository.insert(instanceRequestEntity);

            populateAuditValuesForCreateInstanceRequest(auditProps, requestId.toString());
            auditService.sendAuditEventForInstanceRequest(auditProps, new InstanceRequestV2Entity(),
                                                      deepCopyInstanceRequestEntity(instanceRequestEntity));

            // Publish event for asynchronous account name update
            instanceServiceHelper.sendNcaIdAccountNameEventAsync(instanceRequestEntity.getNcaId(),
                                                             instanceRequestEntity.getRequestId());


        } catch (Exception e) {
            String msg =
                    String.format("Exception while inserting the instance request with id" +
                                          " %s in database, error - %s",
                                  requestId, e.getMessage());
            log.error(msg);
            // rethrowing same exception as it is handled in Global error handler
            throw e;
        }

        // Send telemetry events
        sendTelemetry(destinations, instanceRequest, instanceRequestEntity, envVars, gdnLaunchSpecification);

        return new CreateSpotInstancesResponse(requestId);
    }

    private void sendSqsMessages(
            @NotNull String customer,
            @NotNull Set<RequestInstanceDestination> destinations,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Map<String, String> envVars,
            @NotNull UUID requestId,
            @Nullable GdnLaunchSpecification gdnLaunchSpecification) {

        // Add SQS messages for first-party compute platform queues
        instanceCreationService.sendSqsMessages(customer, getPlatformDestinations(destinations), instanceRequest, requestId, envVars, gdnLaunchSpecification);

        // Add SQS messaged for BYOC clusters
        // If both task and function details are present, Task will have higher preference
        if (isRequestForTask(instanceRequest)) {
            // For task we don't need to create any pod specs
            sendSqsMessageForByocTask(customer, instanceRequest, getByocDestinations(destinations), requestId);
        } else {
            sendSqsMessageForByocFunction(instanceRequest, getByocDestinations(destinations), requestId, customer);
        }
    }

    private @NotNull Set<RequestInstanceDestination> getPlatformDestinations(@NotNull Set<RequestInstanceDestination> destinations) {
        return destinations.stream().filter(
                d -> computePlatformService.isPlatformCluster(d.getClusterGroupName())).collect(Collectors.toSet());
    }

    // All the destinations will be for same instanceType hence the no of GPU per instance will be same in every destination
    private void setGpuCountPerInstance(
            @NotNull Set<RequestInstanceDestination> requestInstanceDestinations,
            @NotNull InstanceRequestV2Entity instanceRequestV2Entity) {

        if (!requestInstanceDestinations.isEmpty()) {
            requestInstanceDestinations.stream().findFirst().ifPresent(destination ->
                                                                    instanceRequestV2Entity.setGpuCountPerInstance(
                                                                            destination.getInstanceType()
                                                                                    .getGpuCount()));
        }
    }

    private @NotNull Set<RequestInstanceDestination> getByocDestinations(@NotNull Set<RequestInstanceDestination> destinations) {
        return destinations.stream().filter(
                r -> !computePlatformService.isPlatformCluster(r.getClusterGroupName())).collect(Collectors.toSet());
    }


    private void sendSqsMessageForByocTask(
            @NotNull String customer,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Set<RequestInstanceDestination> requestInstanceDestinations,
            @NotNull UUID requestId) {

        int instanceCount = instanceRequest.getInstanceCount();

        ByocLaunchSpecification byocLaunchSpecification =
                ByocLaunchSpecification.builder()
                        .gpuType(instanceRequest.getGpu())
                        .containerImage(instanceRequest.getContainerImage())
                        .environment(instanceRequest.getEnvironment())
                        .maxRuntimeDuration(getStringValue(instanceRequest.getMaxRuntimeDuration()))
                        .maxQueuedDuration(getStringValue(instanceRequest.getMaxQueuedDuration()))
                        .terminationGracePeriodDuration(
                                getStringValue(instanceRequest.getTerminationGracePeriodDuration()))
                        .resultHandlingStrategy(getStringValue(instanceRequest.getResultHandlingStrategy()))
                        .clusters(instanceRequest.getClusters())
                        .regions(instanceRequest.getRegions())
                        .attributes(instanceRequest.getAttributes())
                        .spotEnvironment(getPodEnvironment())
                        .icmsEnvironment(getPodEnvironment())
                        // Send helm chart details
                        .helmChart(instanceRequest.getHelmChart())
                        .configuration(instanceRequest.getConfiguration())
                        .models(instanceRequest.getModels())
                        .telemetries(getStringValue(instanceRequest.getTelemetries()))
                        // Send model cache details
                        .cacheArtifacts(instanceRequest.isCacheArtifacts())
                        .cacheHandle(instanceRequest.getCacheHandle())
                        .cacheSize(byocServiceHelper.getRoundedOfCacheSizeInBytes(instanceRequest.getCacheSize()))
                        .build();

        TaskDetails taskDetails = TaskDetails.builder()
                .taskId(instanceRequest.getTaskId().toString())
                .taskType(instanceServiceHelper.getTaskType(instanceRequest))
                .build();

        for (RequestInstanceDestination requestInstanceDestination : requestInstanceDestinations) {
            // Use instance batch count from destination, fallback to byoc configuration if not set
            int instanceBatchCount = requestInstanceDestination.getInstanceBatchCount() != null 
                ? requestInstanceDestination.getInstanceBatchCount() 
                : byocConfigurationProperties.getInstanceBatchCount();
            
            List<ByocSqsMessageModel> byocSqsMessageModels = new ArrayList<>();
            for (int i = 0; i < instanceCount; i += instanceBatchCount) {
                ByocSqsMessageModel byocTaskSqsMessageModel =
                        byocMessageGenerator.generateSqsMessageModelForByocTask(customer, requestId,
                                                                                requestInstanceDestination, instanceRequest,
                                                                                Math.min(instanceCount - i, instanceBatchCount), byocLaunchSpecification,
                                                                                taskDetails);
                byocSqsMessageModels.add(byocTaskSqsMessageModel);
            }

            instanceServiceHelper.sendTaskMessage(requestInstanceDestination.getCreationQueueUrl(),
                                              byocSqsMessageModels,
                                              "byoc-create",
                                              requestInstanceDestination.getClusterId());
        }
    }

    /**
     * Returns the same values as provided by Cluster Agent for NVCF
     * @return "prod" or "stage"
     */
    private String getPodEnvironment() {
        return byocConfigurationProperties.getEnv();
    }


    private void sendSqsMessageForByocFunction(
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Set<RequestInstanceDestination> requestInstanceDestinations,
            @NotNull UUID requestId,
            @NotNull String customer) {

        int instanceCount = instanceRequest.getInstanceCount();

        for (RequestInstanceDestination requestInstanceDestination : requestInstanceDestinations) {
            // Use instance batch count from destination, fallback to byoc configuration if not set
            int instanceBatchCount = requestInstanceDestination.getInstanceBatchCount() != null 
                ? requestInstanceDestination.getInstanceBatchCount() 
                : byocConfigurationProperties.getInstanceBatchCount();
            
            List<ByocSqsMessageModel> byocSqsMessageModels = new ArrayList<>();
            for (int i = 0; i < instanceCount; i += instanceBatchCount) {
                byocSqsMessageModels.add(
                        byocMessageGenerator.generateSqsMessageModelForByocFunction(
                                requestId.toString(),
                                Math.min(instanceCount - i, instanceBatchCount),
                                instanceRequest,
                                requestInstanceDestination,
                                customer));
            }
            instanceServiceHelper.sendFunctionMessage(requestInstanceDestination.getCreationQueueUrl(),
                                                    byocSqsMessageModels,
                                                  "byoc-create",
                                                  requestInstanceDestination.getClusterId());
        }
    }

    private void setBillingNcaId(UUID functionIdUUID, UUID functionVersionIdUUID,
                                 Map<String, String> envVars, SpotInstanceRequestSchema instanceRequest) {
        if (functionIdUUID != null && functionVersionIdUUID != null) {
            functionBillingService.addFunctionBillingInfo(functionIdUUID, functionVersionIdUUID,
                                                          envVars);
        } else if (isRequestForTask(instanceRequest)) {
            // For task owner nca-id is billing nca-id
            envVars.put(BILLING_NCA_ID_ENV_VAR, instanceRequest.getOwnerNcaIdForTask());
        }
    }

    private void sendTelemetry(
            @NotNull Set<RequestInstanceDestination> requestInstanceDestinations,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull Map<String, String> envVars,
            @Nullable GdnLaunchSpecification gdnLaunchSpecification) {
        if (requestInstanceDestinations.isEmpty()) {
            return;
        }

        sendTelemetryForNvca(getByocDestinations(requestInstanceDestinations), instanceRequest, instanceRequestEntity, envVars);
        instanceCreationService.sendTelemetry(getPlatformDestinations(requestInstanceDestinations), instanceRequest, instanceRequestEntity, envVars, gdnLaunchSpecification);
    }

    private void sendTelemetryForNvca(@NotNull  Set<RequestInstanceDestination> nvcaDestinations,
                                      @NotNull SpotInstanceRequestSchema instanceRequest,
                                      @NotNull InstanceRequestV2Entity instanceRequestEntity,
                                      @NotNull Map<String, String> envVars){
        if (nvcaDestinations.isEmpty()) {
            return;
        }

        // We will send SQS message to each NVCA destination hence sending telemetry event per destination
        for (RequestInstanceDestination requestInstanceDestination : nvcaDestinations) {
            sendTelemetryEvent(instanceRequestEntity.getCustomer(), instanceRequest,
                    requestInstanceDestination.getCloudProvider(), instanceRequestEntity,
                    requestInstanceDestination, envVars);
            logInstanceCreationRequest(requestInstanceDestination, instanceRequest, instanceRequestEntity);
        }
    }

    private void sendTelemetryEvent(
            String customer,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            CloudProvider cloudProvider,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull RequestInstanceDestination requestInstanceDestination,
            @NotNull Map<String, String> envVars) {
        // Sending telemetry event for request creation
        Map<String, Object> metaData = new HashMap<>();
        metaData.put(INSTANCE_TYPE.getName(), instanceRequest.getInstanceType());
        metaData.put(CONTAINER_IMAGE.getName(), instanceRequest.getContainerImage());
        metaData.put(INSTANCE_COUNT.getName(), String.valueOf(instanceRequest.getInstanceCount()));
        metaData.put(CLUSTER_GROUP_NAME.getName(), requestInstanceDestination.getClusterGroupName());
        metaData.put(IS_CLUSTER_GROUP.getName(), String.valueOf(true));
        metaData.put(TelemetryEventClient.EventMetaData.IS_TARGETING.getName(),
                isTargetingEnabled(instanceRequest));
        metaData.put(CREATION_QUEUE.getName(), requestInstanceDestination.getCreationQueueUrl());
        metaData.put(TelemetryEventClient.EventMetaData.CLUSTER_REGISTERED_NCA_ID.getName(),
                     requestInstanceDestination.getNcaId());
        metaData.put(TelemetryEventClient.EventMetaData.CLUSTER_AUTHORIZED_NCA_ID.getName(),
                     requestInstanceDestination.getAuthorizedNcaIds());
        if (StringUtils.isNotBlank(instanceRequest.getHelmChart())) {
            metaData.put(HELM_CHART.getName(), instanceRequest.getHelmChart());
        }

        var billingNcaId = getBillingNcaIdValueFromEnv(envVars);
        if (StringUtils.isNotBlank(billingNcaId)) {
            metaData.put(TelemetryEventClient.EventMetaData.BILLING_NCA_ID.getName(), billingNcaId);
        }

        // Add task details in metadata
        if (StringUtils.isNotBlank(getStringValue(instanceRequestEntity.getTaskId()))) {
            metaData.put(TASK_ID.getName(), getStringValue(instanceRequestEntity.getTaskId()));
            metaData.put(ACCOUNT_NAME.getName(), instanceRequestEntity.getAccountName());
            metaData.put(RESULT_HANDLING_STRATEGY.getName(), getStringValue(instanceRequest.getResultHandlingStrategy()));
            metaData.put(MAX_QUEUED_DURATION.getName(), instanceRequestEntity.getMaxQueuedDuration());
            metaData.put(TERMINATION_GRACE_PERIOD_DURATION.getName(),
                         getStringValue(instanceRequest.getTerminationGracePeriodDuration()));
            metaData.put(MAX_RUNTIME_DURATION.getName(), getStringValue(instanceRequest.getMaxRuntimeDuration()));
        }

        // Send model cache telemetry
        metaData.put(IS_CACHE_ARTIFACTS.getName(), instanceRequest.isCacheArtifacts());
        metaData.put(CACHE_HANDLE.getName(), instanceRequest.getCacheHandle());
        metaData.put(CACHE_SIZE.getName(), getStringValue(instanceRequest.getCacheSize()));

        if (instanceRequest.getCacheSize() != null && instanceRequest.getCacheSize().intValue() != 0) {
            metaData.put(ROUNDED_OF_CACHE_SIZE.getName(),
                         getStringValue(byocServiceHelper.getRoundedOfCacheSizeInBytes(instanceRequest.getCacheSize())));
        }

        // Todo :- This should be a simpler event with just bare minimum things requested in the request body and customer
        // Other things like cloud provider resource provider etc can be part of request update and instance update events.
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withCustomer(customer)
                .withCloudProvider(cloudProvider)
                .withResourceProvider(ResourceProvider.BYOC)
                .withMetadata(metaData)
                .withRequestId(instanceRequestEntity.getRequestId())
                .withEventName(Events.CREATE_INSTANCES.toString())
                .withInstanceCount(instanceRequest.getInstanceCount())
                .withFunctionId(instanceRequest.getFunctionId())
                .withFunctionName(instanceRequest.getFunctionName())
                .withFunctionVersionId(instanceRequest.getFunctionVersionId())
                .withNcaId(instanceRequest.getNcaId())
                .withInstanceType(instanceRequest.getInstanceType())
                .withGpuName(instanceRequest.getGpu())
                .withTaskId(getStringValue(instanceRequest.getTaskId()))
                .withTaskName(instanceRequest.getTaskName())
                .withRequestState(instanceRequestEntity.getState().toString())
                .withDeploymentId(instanceRequest.getDeploymentId())
                .withCapacityType(requestInstanceDestination.getCapacityType())
                .withReservationId(requestInstanceDestination.getReservationId())
                .withClusterId(requestInstanceDestination.getClusterId())
                .withClusterName(requestInstanceDestination.getClusterName())
                .withGpuSpecificationId(instanceRequest.getGpuSpecificationId())));
    }

    private String getBillingNcaIdValueFromEnv(Map<String, String> envVar) {
        return envVar.get(BILLING_NCA_ID_ENV_VAR);
    }

    private void logInstanceCreationRequest(@NotNull RequestInstanceDestination requestInstanceDestination,
                                                @NotNull SpotInstanceRequestSchema instanceRequest,
                                                @NotNull InstanceRequestV2Entity instanceRequestV2Entity) {
        log.info(
                "{} | version-id {} | clusterGroupId {} | clusterId {} | reservationId: {} | capacityType {}:  Created request-id {} in queueUrl {}",
                instanceRequest.getLoggingId(),
                instanceRequest.getFunctionVersionId(),
                requestInstanceDestination.getClusterGroupId(),
                requestInstanceDestination.getClusterId(),
                requestInstanceDestination.getReservationId(),
                requestInstanceDestination.getCapacityType(),
                instanceRequestV2Entity.getRequestId(),
                requestInstanceDestination.getCreationQueueUrl()
        );
    }
}
