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
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.byoc.ByocTerminateService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Map;

import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import static com.nvidia.icms.scheduled.ShuttingDownInstanceTerminationTaskController.SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME;

@Slf4j
@Service
@AllArgsConstructor
public class ShuttingDownInstanceTerminationTask {

    public static final String STUCK_IN_SHUTTING_DOWN_ERROR_LOG = "Instance terminated - stuck in shutting-down state";

    private final InstanceV2Repository instanceV2Repository;
    private final TelemetryEventClient telemetryEventClient;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final ByocTerminateService byocTerminateService;
    private final InstanceServiceHelper instanceServiceHelper;

    @Observed
    public void execute() {
        log.info("job: {}, Starting execution", SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME);

        try {
            // Filter instances that are in SHUTTING_DOWN state for more than configured threshold
            int thresholdHours = icmsConfigurationProperties.getShuttingDownInstanceTerminationThresholdInHours();
            Instant cutoffTime = Instant.now().minus(thresholdHours, ChronoUnit.HOURS);

            log.info("Job: {}, Looking for instances in shutting-down state for more than {} hours", SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME, thresholdHours);
            
            instanceV2Repository.findAllInstancesAndApplyAction(
                    instance -> processShuttingDownInstance(instance, cutoffTime),
                    500 // 500ms pause between pages to avoid DB overload
            );
            
            log.info("job: {}, completed", SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME);
            
        } catch (Exception e) {
            log.error("job: {}, Error during job execution: {}",SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME, e.getMessage(), e);
        }
    }

    /**
     * Process a single instance to check if it needs to be terminated
     */
    private void processShuttingDownInstance(@NotNull InstanceV2Entity instance,
                                             @NotNull Instant cutoffTime) {
        try {
            if (instance.getInstanceStateName() == SpotInstanceInternalState.SHUTTING_DOWN &&
                instance.getInstanceUpdateTime() != null &&
                instance.getInstanceUpdateTime().isBefore(cutoffTime)) {
                
                // Update instance state to TERMINATED using shared service method like CloudHealthCheckTask
                byocTerminateService.updateInstanceEntityState(instance, STUCK_IN_SHUTTING_DOWN_ERROR_LOG);
                
                instanceV2Repository.update(instance);
                
                // Send individual telemetry event for the terminated instance
                sendInstanceTerminationTelemetry(instance);
                
                log.info("job: {}, Successfully terminated instance {} that was in shutting-down state since {}",
                        SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME,
                        instance.getInstanceId(), instance.getInstanceUpdateTime());
            }
        } catch (Exception e) {
            String errorMsg = String.format("Failed to terminate instance %s: %s", 
                                           instance.getInstanceId(), e.getMessage());
            log.error("job: {}, error: {}, exception: ", SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME, errorMsg, e);
            
            // Send error telemetry for failed termination attempt
            sendFailureTerminationTelemetry(instance, e);
        }
    }

    /**
     * Send telemetry event for individual instance termination
     */
    private void sendInstanceTerminationTelemetry(InstanceV2Entity instance) {
        Map<String, Object> successMetadata = Map.of(
                "terminationReason", "stuck_in_shutting_down_state"
        );
        
        sendInstanceTelemetry(instance, 
                            Events.TERMINATED_STUCK_SHUTTING_DOWN_INSTANCE,
                            SpotInstanceInternalState.TERMINATED.getStateName(),
                            null,
                            successMetadata,
                            "termination");
        // Send latest instance state event alongside TERMINATED_STUCK_SHUTTING_DOWN_INSTANCE
        instanceServiceHelper.sendLatestInstanceStateEvent(instance);
    }

    /**
     * Send telemetry event for failed instance termination attempts
     */
    private void sendFailureTerminationTelemetry(InstanceV2Entity instance, Exception exception) {
        Map<String, Object> failureMetadata = Map.of(
                "failureReason", "termination_process_failed",
                "exceptionType", exception.getClass().getSimpleName()
        );
        
        sendInstanceTelemetry(instance,
                            Events.FAILED_TO_TERMINATE_STUCK_SHUTTING_DOWN_INSTANCE,
                            instance.getInstanceStateName().getStateName(),
                            exception.getMessage(),
                            failureMetadata,
                            "failure");
    }

    /**
     * Common method to send telemetry events for instance processing
     */
    private void sendInstanceTelemetry(InstanceV2Entity instance, 
                                     Events eventType,
                                     String instanceState,
                                     String errorMessage,
                                     Map<String, Object> specificMetadata,
                                     String telemetryType) {
        try {
            // Build base metadata that's common to both success and failure events
            Map<String, Object> baseMetadata = Map.of(
                    "originalState", SpotInstanceInternalState.SHUTTING_DOWN.getStateName(),
                    "configuredThresholdHours", icmsConfigurationProperties.getShuttingDownInstanceTerminationThresholdInHours(),
                    "instanceUpdateTime", instance.getInstanceUpdateTime() != null ? 
                            instance.getInstanceUpdateTime().toString() : "unknown",
                    "timeInShuttingDownState", instance.getInstanceUpdateTime() != null ? 
                            ChronoUnit.HOURS.between(instance.getInstanceUpdateTime(), Instant.now()) + "h" : "unknown"
            );

            // Combine base metadata with specific metadata
            Map<String, Object> combinedMetadata = new java.util.HashMap<>(baseMetadata);
            combinedMetadata.putAll(specificMetadata);

            // Build the metric with common fields
            GenericMetric metric = new GenericMetric()
                    .withEventName(eventType.toString())
                    .withCustomer(instance.getCustomer())
                    .withInstanceId(instance.getInstanceId())
                    .withZoneName(instance.getZone())
                    .withRequestId(instance.getRequestId())
                    .withInstanceType(instance.getInstanceType())
                    .withInstanceState(instanceState)
                    .withMetadata(combinedMetadata);

            // Add error message if provided (for failure events)
            if (errorMessage != null) {
                metric.withError(errorMessage);
            }

            // Add optional fields if available
            if (instance.getNcaId() != null) {
                metric.withNcaId(instance.getNcaId());
            }
            
            if (instance.getResourceProvider() != null) {
                metric.withResourceProvider(instance.getResourceProvider());
            }

            telemetryEventClient.triggerEvent(List.of(metric));
            
        } catch (Exception e) {
            // Don't fail the processing if telemetry fails
            log.warn("job: {} Failed to send {} telemetry for instance {}: {}",
                    SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME,
                    telemetryType, instance.getInstanceId(), e.getMessage());
        }
    }
} 