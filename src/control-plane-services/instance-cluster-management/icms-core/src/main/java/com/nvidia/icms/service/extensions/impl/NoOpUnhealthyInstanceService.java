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
package com.nvidia.icms.service.extensions.impl;

import com.nvidia.icms.service.extensions.api.UnhealthyInstanceService;

import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.util.List;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;

/**
 * No-op implementation of {@link UnhealthyInstanceService} that is registered only when no
 * other {@link UnhealthyInstanceService} bean is present in the application context.
 *
 * <p>All methods perform no action and return safe neutral values, ensuring that unhealthy
 * instance processing does not fail when non-BYOC-specific handling is not configured.
 */
@Slf4j
public class NoOpUnhealthyInstanceService implements UnhealthyInstanceService {

    /**
     * In normal implementations, persists the terminal state of each unhealthy instance and
     * dispatches the corresponding termination messages to the non-BYOC provider.
     * This no-op implementation performs no action.
     */
    @Override
    public void persistAndTerminate(List<InstanceV2Entity> unhealthyInstances) {
        log.debug("NoOpUnhealthyInstanceService.persistAndTerminate called — no-op");
    }

    /**
     * In normal implementations, emits telemetry events for each unhealthy non-BYOC cloud zone.
     * This no-op implementation performs no action.
     */
    @Override
    public void sendTelemetryForUnhealthyCloud(Set<String> unhealthyZones) {
        log.debug("NoOpUnhealthyInstanceService.sendTelemetryForUnhealthyCloud called — no-op");
    }

    /**
     * In normal implementations, builds a telemetry metric representing a non-BYOC cloud-offline event
     * for the given zone. This no-op implementation returns an empty {@link GenericMetric} with
     * no fields populated.
     *
     * @return a new, empty {@link GenericMetric}
     */
    @Override
    public GenericMetric getMetricForCloudOffline(String zone) {
        log.debug("NoOpUnhealthyInstanceService.getMetricForCloudOffline called — returning empty metric");
        return new GenericMetric();
    }
}
