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
package com.nvidia.icms.service.scheduled.instance;

import static com.nvidia.icms.service.InstanceServiceHelper.getCloudProvider;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.service.extensions.api.UnhealthyInstanceService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.byoc.ByocUnhealthyInstanceService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.audit.AuditOperation;
import com.nvidia.icms.util.audit.AuditState;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

/**
 * Instance will be considered healthy if it's cluster is healthy
 * We will validate cluster's health to identify the instance's heath
 * <p>
 * no cloud health status in DB = UNHEALTHY
 * UNHEALTHY cloud health status in DB = UNHEALTHY
 * HEALTHY cloud health status in DB = HEALTHY
 * <p>
 * If the cloud health status is reported as HEALTHY then it will be considered as healthy for 15 mins (cloud-health-ttl-in-sec)
 * If the cloud health status is reported as UNHEALTHY then it will be immediately considered as unhealthy
 */
@Service
@Slf4j
@AllArgsConstructor
public class ProcessUnhealthyInstance {

    public static final String PROCESS_UNHEALTHY_INSTANCE_TASK = "ProcessUnhealthyInstanceTask";

    private final UnhealthyInstanceService unhealthyInstanceService;
    private final ByocUnhealthyInstanceService byocUnhealthyInstanceService;
    private final TelemetryEventClient telemetryEventClient;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final ComputePlatformService computePlatformService;

    @Observed
    public void execute(
            @NotNull List<InstanceV2Entity> unhealthyInstances,
            @NotNull Set<String> skipHealthCheckStatusClusterIds) {
        try {
            sendProgressEvent("STARTED");
            terminateUnhealthyInstances(unhealthyInstances, skipHealthCheckStatusClusterIds);
            sendProgressEvent("COMPLETED");

        } catch (Exception exception) {
            log.error("ProcessUnhealthyInstance: instance health monitoring task failed, error: {}, exception: ",
                      exception.getMessage(), exception);
            sendTaskFailureException(exception.getMessage());
            throw exception;
        }
    }

    void terminateUnhealthyInstances(
            @NotNull List<InstanceV2Entity> unhealthyInstances,
            @NotNull Set<String> skipHealthCheckStatusClusterIds) {

        List<InstanceV2Entity> unhealthyNonByocInstances = new ArrayList<>();
        List<InstanceV2Entity> unhealthyByocInstances = new ArrayList<>();
        Set<String> unhealthyNonByocZones = new HashSet<>();

        for (InstanceV2Entity instanceEntity : unhealthyInstances) {
            try {
                // If cloud health check enabled for that resource provider and cluster is not skipped for health check processing,
                // then we will process unhealthy instance for termination
                Optional<ResourceProvider> resourceProvider =
                        processUnhealthyInstance(instanceEntity, skipHealthCheckStatusClusterIds);

                if (resourceProvider.isPresent()) {
                    if (computePlatformService.isComputePlatformProvider(resourceProvider.get())) {
                        unhealthyNonByocInstances.add(instanceEntity);
                        unhealthyNonByocZones.add(instanceEntity.getZone());
                    } else if (resourceProvider.get() == ResourceProvider.BYOC) {
                        unhealthyByocInstances.add(instanceEntity);
                    }
                } else {
                    log.info("ProcessUnhealthyInstance: Skipping processing of unhealthy instance, instanceId: {} ",
                             instanceEntity.getInstanceId());
                }

            } catch (Exception e) {
                // Suppressing the error to allow further instance processing
                log.error("ProcessUnhealthyInstance: Error in cloud health check for the instance {}, request Id {}, error: {}",
                          instanceEntity.getInstanceId(), instanceEntity.getRequestId(),
                          e.getMessage(), e);
                unhealthyInstanceProcessingFailed(instanceEntity, e.getMessage());
            }
        }

        // Send event for the unhealthy non-BYOC zones
        unhealthyInstanceService.sendTelemetryForUnhealthyCloud(unhealthyNonByocZones);

        unhealthyInstanceService.persistAndTerminate(unhealthyNonByocInstances);
        byocUnhealthyInstanceService.persistAndTerminate(unhealthyByocInstances);
    }

    private Optional<ResourceProvider> processUnhealthyInstance(
            InstanceV2Entity instanceEntity,
            Set<String> skipHealthCheckStatusClusterIds) {
        ResourceProvider resourceProvider = getResourceProvider(instanceEntity);

        if (!isHealthCheckEnabled(resourceProvider) ||
                skipHealthCheckStatusClusterIds.contains(instanceEntity.getZone())) {
            return Optional.empty();
        }
        return Optional.of(resourceProvider);
    }

    private ResourceProvider getResourceProvider(InstanceV2Entity instanceEntity) {
        ResourceProvider resourceProvider = instanceEntity.getResourceProvider();
        if (resourceProvider == null) {
            log.error("job: {} Failed to fetch resource provider", PROCESS_UNHEALTHY_INSTANCE_TASK);
        }
        return resourceProvider;
    }

    private boolean isHealthCheckEnabled(@Nullable ResourceProvider resourceProvider) {
        // resourceProvider null-guard avoids an Optional.of(null) downstream; the actual
        // on/off decision is the single cloud-failure-detection flag (provider-agnostic).
        return resourceProvider != null && icmsConfigurationProperties.isCloudFailureDetectionEnabled();
    }

    private void sendProgressEvent(String status) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withMetadata(Map.of("STATUS", status))
                .withEventName(Events.PROCESS_UNHEALTHY_INSTANCE_TASK.toString())));
    }

    private void sendTaskFailureException(String errMsg) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withError(errMsg)
                .withEventName(Events.PROCESS_UNHEALTHY_INSTANCE_TASK_FAILED.toString())));
    }

    private void unhealthyInstanceProcessingFailed(InstanceV2Entity instanceV2Entity, String errMsg) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withError(errMsg)
                .withInstanceId(instanceV2Entity.getInstanceId())
                .withRequestId(instanceV2Entity.getRequestId())
                .withClusterName(instanceV2Entity.getZone())
                .withCloudProvider(getCloudProvider(instanceV2Entity))
                .withFunctionId(instanceV2Entity.getNcaId())
                .withEventName(Events.PROCESS_UNHEALTHY_INSTANCE_TASK_FAILED.toString())));
    }
}
