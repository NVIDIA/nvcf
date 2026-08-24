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
package com.nvidia.icms.service.telemetry;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.boot.telemetry.client.CloudEventBuilderProvider;
import com.nvidia.boot.telemetry.client.TelemetryClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.service.telemetry.model.UnifiedErrorMetric;
import io.cloudevents.CloudEvent;
import io.cloudevents.core.data.PojoCloudEventData;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;

import java.net.URI;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;

@Slf4j
@AllArgsConstructor
@Data
public class TelemetryEventClient {

    // NOTE: DON'T CHANGE IT, it is taken from AWS ENV
    public static final String POD_NAME_ENV_KEY = "POD_NAME";
    public static final String AWS_REGION_ENV_KEY = "AWS_REGION";

    private static final String SOURCE = "spot-instance-service"; // TODO: Can these be changed?
    private static final String EVENT = "spot-event";
    private TelemetryClient telemetryClient;
    private CloudEventBuilderProvider cloudEventBuilderProvider;
    private String resourceName;
    private boolean isEnabled;
    private String env;
    private String region;
    private ObjectMapper objectMapper;
    private DatadogEventLogger datadogEventLogger;

    public void triggerEvent(List<GenericMetric> events) {
        try {
            List<CloudEvent> cloudEvents = new ArrayList<>();

            if (events != null && !events.isEmpty()) {
                for (GenericMetric genericMetric : events) {
                    genericMetric.setEventTimestamp((double) Instant.now().getEpochSecond());
                    genericMetric.setEnv(env);
                    genericMetric.setRegion(region);
                    genericMetric.withPodName(System.getenv(POD_NAME_ENV_KEY));

                    logToDatadog(genericMetric);

                    PojoCloudEventData<GenericMetric> pojoCloudEventData =
                            PojoCloudEventData.wrap(genericMetric, objectMapper::writeValueAsBytes);

                    var cloudEventBuilder = cloudEventBuilderProvider.getCloudEventBuilder();
                    var cloudEvent = cloudEventBuilder
                            //  type must identify the type of event
                            //  source must identify the machine/infra sending the event
                            .withType(EVENT)
                            .withSource(URI.create(SOURCE))
                            .withData(pojoCloudEventData)
                            .build();

                    cloudEvents.add(cloudEvent);
                }
                sendEvent(cloudEvents);
            }
        } catch (Exception exception) {
            // TODO: Setup alert based on below error log
            log.warn("Exception while sending the event {}", exception.getMessage(),
                    exception);
        }
    }

    public void triggerErrorEvent(List<UnifiedErrorMetric> events) {
        try {
            List<CloudEvent> cloudEvents = new ArrayList<>();

            if (events != null && !events.isEmpty()) {
                for (UnifiedErrorMetric unifiedErrorMetric : events) {
                    unifiedErrorMetric.setEventTimestamp((double) Instant.now().getEpochSecond());
                    unifiedErrorMetric.setEnv(env);
                    unifiedErrorMetric.setRegion(region);
                    unifiedErrorMetric.setPodName(System.getenv(POD_NAME_ENV_KEY));

                    logToDatadog(unifiedErrorMetric);

                    PojoCloudEventData<UnifiedErrorMetric> pojoCloudEventData =
                            PojoCloudEventData.wrap(unifiedErrorMetric, objectMapper::writeValueAsBytes);

                    var cloudEventBuilder = cloudEventBuilderProvider.getCloudEventBuilder();
                    CloudEvent cloudEvent = cloudEventBuilder
                            //  type must identify the type of event
                            //  source must identify the machine/infra sending the event
                            .withType(EVENT)
                            .withSource(URI.create(SOURCE))
                            .withData(pojoCloudEventData)
                            .build();

                    cloudEvents.add(cloudEvent);
                }
                sendEvent(cloudEvents);
            }
        } catch (Exception exception) {
            // TODO: Setup alert based on below error log
            log.warn("Exception while sending the event {}", exception.getMessage(),
                     exception);
        }
    }

    private void sendEvent(List<CloudEvent> cloudEvents) {
        if (isEnabled) {
            telemetryClient.sendAsync(resourceName, cloudEvents)
                    .exceptionally(ex -> {
                        log.warn("Failed to send telemetry events asynchronously: {}", ex.getMessage(), ex);
                        return null;
                    });
        }
    }

    private void logToDatadog(GenericMetric metric) {
        try {
            if (datadogEventLogger != null) {
                datadogEventLogger.logEvent(metric);
            }
        } catch (Exception e) {
            log.debug("Failed to log event to Datadog: {}", e.getMessage());
        }
    }

    private void logToDatadog(UnifiedErrorMetric metric) {
        try {
            if (datadogEventLogger != null) {
                datadogEventLogger.logErrorEvent(metric);
            }
        } catch (Exception e) {
            log.debug("Failed to log error event to Datadog: {}", e.getMessage());
        }
    }

    @Getter
    public enum EventMetaData {

        REQUEST_METHOD("requestMethod"),
        REQUEST_URI("requestURI"),
        RESPONSE_STATUS("responseStatus"),
        API_EXECUTION_TIME("executionTimeInMs"),

        SUBJECT_ID("subjectId"),

        UUID("uuid"),
        ERROR_RESPONSE("errorResponse"),

        CLIENT_ADDRESS("clientAddress"),
        INSTANCE_TYPE("instanceType"),

        CONTAINER_IMAGE("containerImage"),

        INSTANCE_COUNT("instanceCount"),

        INSTANCE_IDS("instanceIds"),

        CLUSTER_NAME("clusterName"),
        ZONE_NAME("zoneName"),

        CLUSTER_GROUP_ID("clusterGroupId"),

        CLUSTER_GROUP_NAME("clusterGroupName"),

        CLUSTER_PROVIDER("clusterProvider"),

        CLUSTER_STATUS("clusterStatus"),
        CLUSTER_UPGRADE_STATUS("clusterUpgradeStatus"),

        AVAILABILITY_ZONE("availabilityZone"),

        IS_CLUSTER_GROUP("isClusterGroup"),

        IS_TARGETING("isTargeting"),

        CLUSTER_REGISTRATION_STATUS("clusterRegistrationStatus"),

        SCHEDULED_TASK_NAME("ScheduledTaskName"),

        NODE_NAME("NodeName"),

        TERMINATION_CAUSE("TerminationCause"),

        NCA_ID("NcaId"),

        AUTHORIZED_NCA_ID("AuthorizedNcaId"),

        QUERY_PATH_PARAM("QueryPathParam"),

        DOWNSTREAM_SYSTEM_FAILURE("DownstreamSystemFailure"),
        RESOURCE_IPS("ResourceIps"),

        ERROR_LOG("ErrorLog"),
        ERROR_SOURCE("ErrorSource"),

        ERROR_ORIGIN_INFO("ErrorOriginInfo"),

        TERMINATION_QUEUE("TerminationQueue"),

        ALLOW_TASK_CLUSTER_CREATION_QUEUES("AllowTaskClusterCreationQueues"),

        CREATION_QUEUE("CreationQueue"),

        CLUSTER_CREATION_QUEUES("ClusterCreationQueue"),

        TASKS_CLUSTER_CREATION_QUEUE("TasksClusterCreationQueue"),

        NVCA_VERSION("NvcaVersion"),

        AUTH_CLIENT_ID("AuthClientId"),

        REGION("Region"),

        ATTRIBUTES("Attributes"),

        CAPABILITIES("Capabilities"),

        REQUEST_BODY("RequestBody"),

        CLUSTER_REGISTERED_NCA_ID("ClusterRegisteredNcaId"),


        CLUSTER_AUTHORIZED_NCA_ID("ClusterAuthorizedNcaId"),

        GPU("GPU"),

        GPU_USAGE("GPU_USAGE"),

        EXECUTION_TIME("ExecutionTime"),

        DELETED_INSTANCE_COUNT("DeletedInstanceCount"),

        DELETED_REQUEST_COUNT("DeletedRequestCount"),

        CLOSED_REQUEST_COUNT("ClosedRequestCount"),

        DELETED_INSTANCES("DeletedInstances"),

        DELETED_REQUESTS("DeletedRequests"),

        CLOSED_REQUESTS("ClosedRequests"),

        ACTION("Action"),

        MESSAGE_BATCH_STATUS("MessageBatchStatus"),

        REQUEST_STATUS("RequestStatus"),

        PREVIOUS_REQUEST_STATE("PreviousRequestState"),

        PREVIOUS_REQUEST_STATUS("PreviousRequestStatus"),

        HELM_CHART("HelmChart"),

        TASK_ID("TaskId"),

        ACCOUNT_NAME("AccountName"),

        RESULT_HANDLING_STRATEGY("ResultHandlingStrategy"),

        MAX_QUEUED_DURATION("MaxQueuedDuration"),

        TERMINATION_GRACE_PERIOD_DURATION("terminationGracePeriodDuration"),
        
        ERROR_CLASS_NAME("ErrorClassName"),

        ERROR_METHOD_NAME("ErrorMethodName"),

        MAX_RUNTIME_DURATION("maxRuntimeDuration"),

        JOB_NAME("jobName"),

        PROCESSED_DAY("processedDay"),

        TASK_STATUS("taskStatus"),

        DEPLOYMENT_STAGE("deploymentStage"),

        QUEUE_URL("queueUrl"),

        IS_CACHE_DATA_STALE("IsCacheDataStale"),

        LAST_UPDATED_TIME("LastUpdatedTime"),

        THREAD_NAME("ThreadName"),


        IS_CACHE_ARTIFACTS("IsCacheArtifacts"),

        CACHE_HANDLE("CacheHandle"),

        CACHE_SIZE("CacheSize"),

        ROUNDED_OF_CACHE_SIZE("RoundedOfCacheSize"),

        CLUSTER_OWNER_NCA_ID("ClusterOwnerNcaId"),

        NVCA_AGENT_VERSION("NvcaAgentVersion"),

        NVCA_OPERATOR_VERSION("NvcaOperatorVersion"),

        RESERVATION_ID("ReservationId"),

        RESERVATION_ACTIVE("Active"),

        RESERVATION_INACTIVE("InActive"),

        HEARTBEAT_REQUEST_BODY("HeartbeatRequestBody"),

        NVCA_HEARTBEAT_ACTION_RESPONSE("NvcaHeartbeatActionResponse"),

        FUNCTION_TYPE("FunctionType"),

        BILLING_NCA_ID("BillingNcaId"),

        NVCA_LAST_CONNECTED("NvcaLastConnected"),

        PREVIOUS_INSTANCE_EXPIRATION_TIME("PreviousInstanceExpirationTime"),

        NEW_INSTANCE_EXPIRATION_TIME("NewInstanceExpirationTime"),

        RESERVATION_ZONE("ReservationZone"),
        
        TENANT("Tenant"),
        
        REGISTRATION_ID("RegistrationId"),

        GDN_REGISTRATION_ID("GdnRegistrationId"),
        
        TENANT_REGISTRATION_DATA_KEYS("TenantRegistrationDataKeys")
        ;

        private final String name;

        EventMetaData(String name) {
            this.name = name;
        }

        @Override
        public String toString() {
            return this.name;
        }
    }
}
