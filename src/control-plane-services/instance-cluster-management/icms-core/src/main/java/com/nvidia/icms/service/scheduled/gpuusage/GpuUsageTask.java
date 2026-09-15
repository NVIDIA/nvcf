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
package com.nvidia.icms.service.scheduled.gpuusage;

import static com.nvidia.icms.scheduled.GpuUsageTaskController.GPU_USAGE_EVENT_NAME;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.LatestInstanceStateEventService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class GpuUsageTask {

    private static final EnumSet<SpotInstanceInternalState> ACTIVE_INSTANCE_STATES = EnumSet.of(
            SpotInstanceInternalState.STARTING,
            SpotInstanceInternalState.RUNNING
    );

    private final InstanceV2Repository instanceV2Repository;
    private final GpuUsageEventService gpuUsageEventService;
    private final CloudHealthRepository cloudHealthRepository;
    private final TelemetryEventClient telemetryEventClient;
    private final LatestInstanceStateEventService latestInstanceStateEventService;

    @Observed
    public void execute() {
        Instant now = Instant.now();
        Instant currentJobExecutionTime = now.truncatedTo(ChronoUnit.HOURS);

        // TODO(NVCFSPOT-1477): Use previous job execution time from DB
        Instant previousJobExecutionTime = now.truncatedTo(ChronoUnit.HOURS).minus(1, ChronoUnit.HOURS);
        Map<String, ClusterProviderEnum> clusterProviderEnumCache = new HashMap<>();
        Set<String> healthyClusters = cloudHealthRepository.finalAllHealthyZones();

        instanceV2Repository.findAllInstancesAndApplyAction(
                r -> processInstanceV2Entity(r, currentJobExecutionTime,
                                                 previousJobExecutionTime,
                                                 clusterProviderEnumCache,
                                                 healthyClusters), 5);
    }

    private void processInstanceV2Entity(
            InstanceV2Entity entity,
            Instant currentJobExecutionTime,
            Instant previousJobExecutionTime,
            Map<String, ClusterProviderEnum> clusterProviderEnumCache,
            Set<String> healthyClusters) {

        SpotInstanceInternalState instanceState = entity.getInstanceStateName();
        String instanceId = entity.getInstanceId();

        /*
        While considering GPU usage we will consider only starting and running instances as active instances.
        We won't consider shutting-down instances as active, because NVCA/NGRAM will be reporting them as terminated soon after moving to shutting-down state
        But we will send telemetry event for shutting-down instances to track if NVCA/NGRAM not reporting shutting-down instances as terminated to SIS
         */

        // Process instances based on their state
        if (instanceState == null) {
            log.warn("Skipping instance with null state: {}", instanceId);
            return;
        }

        // Special handling for shutting down instances - send telemetry but don't process further
        if (SpotInstanceInternalState.SHUTTING_DOWN.equals(instanceState)) {
            sendShuttingDownInstanceEvent(entity);
            latestInstanceStateEventService.sendLatestInstanceStateEvent(entity);
            return;
        }

        // Skip non-active instances
        if (!ACTIVE_INSTANCE_STATES.contains(instanceState)) {
            return;
        }

        // Skip instances from unhealthy clusters
        if (!healthyClusters.contains(entity.getZone())) {
            log.warn("Event: {} skipping GPU usage from unhealthy cluster, instanceId: {} instanceState: {} zoneName: {}",
                    GPU_USAGE_EVENT_NAME, instanceId,
                    instanceState.getStateName(),
                    entity.getZone());
            return;
        }

        // Process active instances from healthy clusters
        gpuUsageEventService.sendGpuUsageEventForRunningInstance(entity, currentJobExecutionTime,
                                                                 previousJobExecutionTime,
                                                                 clusterProviderEnumCache);
    }

    private void sendShuttingDownInstanceEvent(InstanceV2Entity entity) {
        GenericMetric metric = new GenericMetric()
                .withEventName(Events.SHUTTING_DOWN_INSTANCES_WITHOUT_TERMINATED_STATE_UPDATE.toString())
                .withInstanceId(entity.getInstanceId())
                .withRequestId(entity.getRequestId())
                .withResourceProvider(entity.getResourceProvider())
                .withInstanceType(entity.getInstanceType())
                .withNcaId(entity.getNcaId())
                .withRequestState(entity.getRequestState().toString())
                .withGpuName(entity.getGpu())
                .withInstanceState(entity.getInstanceStateName().getStateName());
                
        telemetryEventClient.triggerEvent(List.of(metric));
    }
}
