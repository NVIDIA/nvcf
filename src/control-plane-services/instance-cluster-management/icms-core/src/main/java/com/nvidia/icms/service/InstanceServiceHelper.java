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

import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.event.NcaIdAccountNameEvent;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.TaskType;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstancePerZoneRepository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.nats.NatsMessageSenderClient;
import com.nvidia.icms.outbound.sqs.SqsMessageSenderClient;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocTerminatePodMessageModel;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.metrics.InstanceErrorMetricsService;
import com.nvidia.icms.service.scheduled.gpuusage.GpuUsageEventService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import io.micrometer.tracing.Tracer;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.context.ApplicationEventPublisher;
import org.springframework.stereotype.Service;

import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.Base64;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;

import static com.nvidia.icms.scheduled.GpuUsageTaskController.GPU_USAGE_EVENT_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.ACCOUNT_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.MAX_QUEUED_DURATION;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.MAX_RUNTIME_DURATION;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.RESULT_HANDLING_STRATEGY;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TASK_ID;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TERMINATION_GRACE_PERIOD_DURATION;
import static com.nvidia.icms.service.telemetry.model.Events.ASYNC_EVENT_TRIGGER_FAILED;
import static com.nvidia.icms.util.InstanceServiceUtil.getStringValue;


@Service
@Slf4j
@AllArgsConstructor
public class InstanceServiceHelper {

    public static final String ICMS_ERROR_SOURCE = "sis";

    private final ObjectMapper objectMapper;

    private final ByocConfigurationProperties byocConfigurationProperties;

    private final ClusterRepository clusterRepository;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final SqsMessageSenderClient sqsMessageSenderClient;

    private final InstancePerZoneRepository instancePerZoneRepository;

    private final InstanceErrorMetricsService instanceErrorMetricsService;

    private final NvcaConfigurationProperties nvcaConfigurationProperties;

    private final NatsConfigurationProperties natsConfigurationProperties;

    private final NatsMessageSenderClient natsMessageSenderClient;

    private final GpuUsageEventService gpuUsageEventService;

    private final CloudHealthRepository cloudHealthRepository;

    private final ApplicationEventPublisher eventPublisher;

    private final TelemetryEventClient telemetryEventClient;

    private final LatestInstanceStateEventService latestInstanceStateEventService;

    private final Tracer tracer;

    public static boolean isReservedBackupInstance(@NotNull InstanceV2Entity instanceV2Entity) {
        String capacityType = instanceV2Entity.getCapacityType();
        return capacityType != null && capacityType.equals(CapacityType.RESERVED_BACKUP.name());
    }

    /**
     * Checks if an instance request is in OPEN or ACTIVE state.
     * Returns true if the request is in OPEN state, or in ACTIVE state when the feature flag is enabled.
     *
     * @param instanceRequestEntity the instance request entity to check
     * @param icmsConfigurationProperties configuration properties containing the feature flag
     * @return true if the request is in OPEN state or ACTIVE state (when feature flag is enabled)
     */
    public static boolean isRequestInOpenOrActiveState(
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull IcmsConfigurationProperties icmsConfigurationProperties) {
        return instanceRequestEntity.getState() == SpotInstanceRequestState.OPEN ||
                (icmsConfigurationProperties.isRequestStateTransitionToActiveEnabled() &&
                        instanceRequestEntity.getState() == SpotInstanceRequestState.ACTIVE);
    }

    @Observed
    public void gpuUsageEventForTerminatedInstance(
            InstanceV2Entity entity) {

        // Sending GPU usage event for terminated instances ONLY if the cluster is healthy
        // It might happen that cluster was unhealthy hence SIS terminated the instance, in that case
        // we should not consider GPU usage of terminated instance
        Optional<CloudHealthEntity> optionalCloudHealth = cloudHealthRepository.findByCloudAndZone(
                entity.getResourceProvider(), entity.getZone());
        if (optionalCloudHealth.isEmpty()) {
            log.warn(
                    "Event: {} skipping GPU usage from unhealthy cluster, instanceId: {} instanceState: {} zoneName: {}",
                    GPU_USAGE_EVENT_NAME, entity.getInstanceId(),
                    entity.getInstanceStateName().getStateName(),
                    entity.getZone());
            return;
        }

        gpuUsageEventService.sendGpuUsageEventForTerminatedInstance(entity);
    }

    public void sendLatestInstanceStateEvent(InstanceV2Entity entity) {
        latestInstanceStateEventService.sendLatestInstanceStateEvent(entity);
    }

    /**
     * @return TraceParent from current span
     */
    @Observed
    public String getTraceParent() {
        var span = tracer.currentSpan();
        if (span != null) {
            var context = span.context();
            if (context != null) {
                String traceId = context.traceId();
                String spanId = context.spanId();
                String sampledFlag = Boolean.TRUE.equals(context.sampled()) ? "01" : "00";
                return String.format("00-%s-%s-%s", traceId, spanId, sampledFlag);
            }
        }
        return null;
    }

    /**
     * @return Map representation of TraceState or empty map if TraceState is empty.
     * Micrometer's Tracer abstraction does not expose W3C tracestate.
     * In practice this was always empty with the previous OTel setup
     * since no vendor-specific propagator was configured.
     */
    @Observed
    public Map<String, String> getTraceStateMap() {
        return new HashMap<>();
    }

    /**
     * This function generate Map<String, String> using provided environment
     *
     * @param environment base64 encoded environment data separated by
     * @return Map<String, String> of environment variables
     */
    public static Map<String, String> parseEnvironmentVariable(
            String environment) {
        Map<String, String> environmentVars = new HashMap<>();
        if (!StringUtils.isEmpty(environment)) {
            Base64.Decoder decoder = Base64.getDecoder();
            String decodedEnvironment = new String(decoder.decode(environment));
            for (String environmentPair : StringUtils.split(decodedEnvironment, "\n")) {
                environmentPair = environmentPair.trim();
                // TODO: Use StringUtils.isEmpty()
                if (!Objects.equals(environmentPair, "")) {
                    String[] environmentPairTokens = StringUtils.split(environmentPair, "=", 2);
                    if (environmentPairTokens.length == 2) {
                        environmentVars.put(environmentPairTokens[0], environmentPairTokens[1]);
                    }
                }
            }
        }

        return environmentVars;
    }

    public static CloudProvider getCloudProvider(InstanceV2Entity instanceV2Entity) {
        String cloudProviderStringValue = instanceV2Entity.getCloudProvider();
        if (cloudProviderStringValue != null) {
            try {
                // Try to find the CloudProvider enum by matching the cloudProviderName field
                for (CloudProvider cloudProvider : CloudProvider.values()) {
                    if (cloudProvider.toString().equals(cloudProviderStringValue.toUpperCase())) {
                        return cloudProvider;
                    }
                }

                // If no match found, log error and return UNKNOWN
                log.error(
                        "Event: {}, No matching CloudProvider enum found for cloudProvider: {}",
                        GPU_USAGE_EVENT_NAME,
                        cloudProviderStringValue);
                return CloudProvider.UNKNOWN;

            } catch (Exception exception) {
                log.error(
                        "Event: {}, Failed to convert cloudProvider to enum, cloudProvider: {}, exception: {}",
                        GPU_USAGE_EVENT_NAME,
                        cloudProviderStringValue, exception.getMessage(),
                        exception);
                return CloudProvider.UNKNOWN;
            }
        }
        return null;
    }

    /**
     * @return boolean true if both SIS and NVCA enabled tasks cluster creation queues
     */
    public boolean isTaskClusterCreationQueuesAllowed(Boolean allowTaskClusterCreationQueues) {
        // Return false if ICMS disabled tasks cluster creation queues
        if (!nvcaConfigurationProperties.isTasksCreationQueuesEnabled()) {
            return false;
        }
        return Boolean.TRUE.equals(allowTaskClusterCreationQueues);
    }

    public boolean isAirGappedModeEnabled() {
        return icmsConfigurationProperties.isAirGappedModeEnabled();
    }

    public boolean isNatsEnabled() {
        return natsConfigurationProperties.isNatsEnabled();
    }

    /**
     * Generates RequestInfo for storing in DB, it will include all the params provided by users in request
     *
     * @param instanceRequest  instance request object sent by client in request
     * @param customer         Customer making the request
     * @param requestId        Request-id attached to that request
     * @param ncaIdAccountName AccountName associated to ncaId
     * @return String representation of {@link ClientRequestDataModel}
     */
    @Observed
    public String generateRequestInfo(
            SpotInstanceRequestSchema instanceRequest, String customer, String requestId,
            @Nullable String ncaIdAccountName) {

        ClientRequestDataModel.LaunchSpecification launchSpecification =
                ClientRequestDataModel.LaunchSpecification.builder()
                        .gpu(instanceRequest.getGpu())
                        .backend(instanceRequest.getBackend())
                        .instanceType(instanceRequest.getInstanceType())
                        .containerImage(instanceRequest.getContainerImage())
                        .ncaId(instanceRequest.getNcaId())
                        .modelCacheEnabled(isModelCachingEnabled(instanceRequest))
                        .functionId(getFunctionId(instanceRequest))
                        .functionName(instanceRequest.getFunctionName())
                        .versionId(getFunctionVersionId(instanceRequest))
                        .deploymentId(instanceRequest.getDeploymentId())
                        .gpuSpecificationId(instanceRequest.getGpuSpecificationId())
                        .build();

        if (instanceRequest.getTaskId() != null) {
            if (instanceRequest.getMaxRuntimeDuration() != null) {
                launchSpecification.setMaxRuntimeDuration(
                        instanceRequest.getMaxRuntimeDuration().toString());
            }
            // Null validation for terminationGracePeriodDuration and resultHandlingStrategy not needed as they are must field
            launchSpecification.setTerminationGracePeriodDuration(
                    instanceRequest.getTerminationGracePeriodDuration().toString());
            launchSpecification.setResultHandlingStrategy(instanceRequest.getResultHandlingStrategy());
            launchSpecification.setTaskId(instanceRequest.getTaskId().toString());
            launchSpecification.setTaskName(instanceRequest.getTaskName());
        }

        if (!StringUtils.isBlank(ncaIdAccountName)) {
            launchSpecification.setNcaIdAccountName(ncaIdAccountName);
        }

        ClientRequestDataModel clientRequestDataModel = ClientRequestDataModel.builder()
                .instanceCount(instanceRequest.getInstanceCount())
                .sub(customer)
                .spotInstanceRequestAction(instanceRequest.getAction())
                .requestId(requestId)
                .launchSpecification(launchSpecification)
                .build();

        return writeValueAsString(clientRequestDataModel);
    }

    public String writeValueAsString(ClientRequestDataModel clientRequestDataModel) {
        try {
            return objectMapper.writeValueAsString(clientRequestDataModel);

        } catch (JacksonException e) {
            String errMsg = String.format("Could not generate request from instance request, error: %s",
                                          e.getMessage());
            log.error("error: {}", errMsg, e);
            throw new IcmsInternalServerException(errMsg, e);
        }
    }

    // Only used in scheduled tasks
    public Set<String> getClusterIdsOfClusterToSkipHealthCheck() {

        Set<String> skipHealthCheckStatus = new HashSet<>();
        List<Map<String, String>> ncaIdToClusterNameMappingFromConfig =
                byocConfigurationProperties.getSkipHealthCheckClusters();

        if (ncaIdToClusterNameMappingFromConfig != null &&
                !ncaIdToClusterNameMappingFromConfig.isEmpty()) {
            for (Map<String, String> ncaIdToClusterNameMap : ncaIdToClusterNameMappingFromConfig) {
                for (Map.Entry<String, String> entrySet : ncaIdToClusterNameMap.entrySet()) {
                    String ncaId = entrySet.getKey();
                    String clusterName = entrySet.getValue();
                    Optional<ClusterEntity> optionalClusterEntity =
                            clusterRepository.getClusterByAccountAndName(ncaId, clusterName);
                    optionalClusterEntity.ifPresent(
                            entity -> skipHealthCheckStatus.add(entity.getClusterId()));
                }
            }
        }
        return skipHealthCheckStatus;
    }

    public void sendInstanceTaskError(String ncaId, String errorSource) {
        instanceErrorMetricsService.recordTaskError(ncaId, errorSource);
    }

    public void sendNcaIdAccountNameEventAsync(String ncaId, String requestId) {
        try {
            eventPublisher.publishEvent(NcaIdAccountNameEvent.builder()
                                                .ncaId(ncaId)
                                                .requestId(requestId)
                                                .build());
        } catch (Exception exception) {
            // Suppressing the error
            log.error("Event: {} Failed to publish NcaIdAccountNameEvent, error: {}",
                      ASYNC_EVENT_TRIGGER_FAILED, exception.getMessage(), exception);
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withEventName(
                                                                       ASYNC_EVENT_TRIGGER_FAILED.toString())
                                                               .withError(exception.getMessage())));
        }
    }

    @Observed
    public ClientRequestDataModel parseRequestInfo(String request) {
        try {
            ClientRequestDataModel requestData;
            requestData =
                    objectMapper.readValue(request, ClientRequestDataModel.class);
            return requestData;
        } catch (Exception e) {
            String errMsg =
                    String.format("Failed to parse request information, error: %s", e.getMessage());
            log.error("error: {}", errMsg, e);
            throw new IcmsInternalServerException(errMsg, e);
        }
    }

    /**
     * This function parse JSON String value of {@link ClientRequestDataModel.LaunchSpecification}
     * If any parsing error occur it will suppress it and return default value
     *
     * @param request JSON String value of {@link ClientRequestDataModel.LaunchSpecification}
     * @return {@link ClientRequestDataModel.LaunchSpecification}
     */
    public ClientRequestDataModel.LaunchSpecification getLaunchSpecificationForTelemetry(
            @Nullable String request) {
        try {
            return getLaunchSpecificationFromRequest(request);
        } catch (Exception exception) {
            log.error("Failed to parse request info for telemetry, error: {} ",
                      exception.getMessage(), exception);
        }

        // Adding default values
        return ClientRequestDataModel.LaunchSpecification.builder().build();
    }


    public ClientRequestDataModel.LaunchSpecification getLaunchSpecificationFromRequest(
            @Nullable String request) {
        if (!StringUtils.isEmpty(request)) {
            ClientRequestDataModel clientRequestDataModel = parseRequestInfo(request);
            if (clientRequestDataModel.getLaunchSpecification() != null) {
                return clientRequestDataModel.getLaunchSpecification();
            }
        }

        // Adding default values
        return ClientRequestDataModel.LaunchSpecification.builder().build();
    }


    /**
     * Use this function when we want to mock exact response having Instant.now()
     * With this function we can mock Instant.now() in response
     *
     * @return Instant.now()
     */
    public Instant getCurrentTimestamp() {
        return Instant.now();
    }


    @Observed
    public void sendTaskMessage(
            @NotNull String queueUrl,
            @Nullable List<ByocSqsMessageModel> messages,
            @NotNull String messageBodyPrefix,
            @NotNull String clusterId) {
        if (natsConfigurationProperties.isNatsEnabled()) {
            natsMessageSenderClient.sendTaskMessages(messages, clusterId);
        } else {
            sendMessageToSqsQueue(queueUrl, messages, messageBodyPrefix);
        }
    }

    @Observed
    public void sendFunctionMessage(
            @NotNull String queueUrl,
            @Nullable List<ByocSqsMessageModel> messages,
            @NotNull String messageBodyPrefix,
            @NotNull String clusterId) {
        if (natsConfigurationProperties.isNatsEnabled()) {
            natsMessageSenderClient.sendFunctionMessages(messages, clusterId);
        } else {
            sendMessageToSqsQueue(queueUrl, messages, messageBodyPrefix);
        }
    }


    @Observed
    public void sendTerminateMessage(
            @NotNull String queueUrl,
            @Nullable List<ByocTerminatePodMessageModel> messages,
            @NotNull String messageBodyPrefix,
            @NotNull String clusterId) {
        if (natsConfigurationProperties.isNatsEnabled()) {
            natsMessageSenderClient.sendTerminateInstanceMessages(messages, clusterId);
        } else {
            sendMessageToSqsQueue(queueUrl, messages, messageBodyPrefix);
        }
    }


    /**
     * Use this function to send Messages to SQS queue
     *
     * @param queueUrl          URL of the SQS queue
     * @param messages          List of messages
     * @param messageBodyPrefix message body prefix
     */
    @Observed
    public void sendMessageToSqsQueue(
            @NotNull String queueUrl,
            @Nullable List<?> messages,
            @NotNull String messageBodyPrefix) {
        sqsMessageSenderClient.sendSqsMessages(queueUrl, messages, messageBodyPrefix);
    }

    public static TaskType getTaskType(@NotNull SpotInstanceRequestSchema instanceRequestSchema) {
        TaskType taskType = TaskType.CONTAINER;
        if (StringUtils.isNotEmpty(instanceRequestSchema.getHelmChart())) {
            taskType = TaskType.HELM_CHART;
        }
        return taskType;
    }

    @Observed
    public Set<String> getActiveInstancesFromZoneForInstanceType(
            String zoneName,
            Set<String> instanceTypes) {

        return instancePerZoneRepository.findAllActiveInstancesByZone(zoneName).stream()
                .filter(instance -> instanceTypes.contains(instance.getInstanceType()))
                .map(InstanceByZoneEntity::getInstanceId)
                .collect(Collectors.toSet());
    }

    @Observed
    public List<String> getActiveInstancesFromZone(String zoneName) {
        return getActiveInstanceEntitiesFromZone(zoneName).stream()
                .map(InstanceByZoneEntity::getInstanceId).toList();
    }

    @Observed
    public List<InstanceByZoneEntity> getActiveInstanceEntitiesFromZone(String zoneName) {
        return instancePerZoneRepository.findAllActiveInstancesByZone(zoneName);
    }

    @Observed
    public void updateMetadataForTask(
            Map<String, Object> metadata,
            @NotNull InstanceRequestV2Entity instanceRequestEntity,
            @NotNull ClientRequestDataModel.LaunchSpecification launchSpecification) {
        // Add task details in metadata
        if (StringUtils.isNotBlank(getStringValue(instanceRequestEntity.getTaskId()))) {
            metadata.put(TASK_ID.getName(), getStringValue(instanceRequestEntity.getTaskId()));
            metadata.put(ACCOUNT_NAME.getName(), instanceRequestEntity.getAccountName());
            metadata.put(RESULT_HANDLING_STRATEGY.getName(),
                         getStringValue(launchSpecification.getResultHandlingStrategy()));
            metadata.put(MAX_QUEUED_DURATION.getName(), instanceRequestEntity.getMaxQueuedDuration());
            metadata.put(TERMINATION_GRACE_PERIOD_DURATION.getName(),
                         getStringValue(launchSpecification.getTerminationGracePeriodDuration()));
            metadata.put(MAX_RUNTIME_DURATION.getName(),
                         getStringValue(launchSpecification.getMaxRuntimeDuration()));
        }
    }

    /**
     * This method updates the InstanceV2Entity state to terminated
     * This method can be used from:
     * 1. Instance termination due to cloud offline
     * 2. Instance termination due to missing cluster info
     * 3. Instance expired
     * 4. Reserved backup instance expired
     */
    public static InstanceV2Entity updateInstanceEntityStateToTerminated(
            InstanceV2Entity instanceEntity,
            String errorLog) {
        instanceEntity.setInstanceStateCode(SpotInstanceInternalState.getStateCode(
                SpotInstanceInternalState.TERMINATED));
        instanceEntity.setInstanceStateName(SpotInstanceInternalState.TERMINATED);
        instanceEntity.setRequestStatusCode(SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE);
        instanceEntity.setRequestStatusMessage(String.format("Instance status updated to %s",
                                                                 SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE));
        instanceEntity.setRequestStatusUpdateTime(Instant.now());
        instanceEntity.setInstanceUpdateTime(Instant.now());
        instanceEntity.setRequestState(SpotInstanceRequestState.CLOSED);
        instanceEntity.setErrorLog(errorLog);
        // Instance is terminated by SIS either for unhealthy cloud, missing cluster info, or expired instance
        instanceEntity.setErrorSource(ICMS_ERROR_SOURCE);
        return instanceEntity;
    }

    /**
     * Checks if model caching is enabled for an instance request by validating cache size.
     *
     * @param instanceRequest the instance request schema containing cache configuration
     * @return true if cache size is non-null and non-zero, false otherwise
     */
    private Boolean isModelCachingEnabled(@NotNull SpotInstanceRequestSchema instanceRequest) {
        return instanceRequest.getCacheSize() != null && instanceRequest.getCacheSize().intValue() != 0;
    }

    public static String getFunctionId(@NotNull SpotInstanceRequestSchema instanceRequest) {
        return instanceRequest.getFunctionId() == null ? null : instanceRequest.getFunctionId().toString();
    }

    public static String getFunctionVersionId(@NotNull SpotInstanceRequestSchema instanceRequest) {
        return instanceRequest.getFunctionVersionId() == null ? null
                : instanceRequest.getFunctionVersionId().toString();
    }

    public static String getFunctionType(SpotInstanceRequestSchema instanceRequest) {
        return instanceRequest.getFunctionType() == null ? null :
                instanceRequest.getFunctionType().toString();
    }

}
