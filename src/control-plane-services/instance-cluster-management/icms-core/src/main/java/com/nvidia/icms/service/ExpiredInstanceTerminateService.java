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

import com.nvidia.icms.service.extensions.api.ExpiredInstanceProcessor;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.byoc.ByocExpiredInstanceTerminateService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import java.util.ArrayList;
import java.util.List;

@AllArgsConstructor
@Service
@Slf4j
public class ExpiredInstanceTerminateService {

    private final ExpiredInstanceProcessor expiredInstanceProcessor;
    private final ByocExpiredInstanceTerminateService byocExpiredInstanceTerminateService;
    private final ComputePlatformService computePlatformService;

    /**
     * Routes expired instances to the appropriate provider-specific service for termination.
     *
     * @param instanceEntities list of expired instances to process
     * @return the subset of instances that were eligible for termination (not already terminated or shutting down)
     */
    public List<InstanceV2Entity> terminateExpiredInstances(
            @NotNull List<InstanceV2Entity> instanceEntities) {

        List<InstanceV2Entity> instances = new ArrayList<>();
        List<InstanceV2Entity> nonByocInstances = new ArrayList<>();
        List<InstanceV2Entity> byocInstances = new ArrayList<>();

        for (InstanceV2Entity entity : instanceEntities) {
            if (entity.getInstanceStateName() == SpotInstanceInternalState.TERMINATED ||
                    entity.getInstanceStateName() == SpotInstanceInternalState.SHUTTING_DOWN) {
                continue;
            }

            try {
                log.trace("Attempt to delete expired instance id {} from {} resource provider",
                          entity.getInstanceId(), entity.getResourceProvider());
                ResourceProvider resourceProvider = entity.getResourceProvider();
                if (computePlatformService.isComputePlatformProvider(resourceProvider)) {
                    nonByocInstances.add(entity);
                } else if (resourceProvider == ResourceProvider.BYOC) {
                    byocInstances.add(entity);
                } else {
                    throw new IllegalStateException(
                            "Unrecognised resource provider '" + entity.getResourceProvider()
                            + "' for instance " + entity.getInstanceId());
                }
            } catch (Exception e) {
                log.error("Error in terminating expired instance Id {}, request Id {} error: {}",
                          entity.getInstanceId(), entity.getRequestId(), e.getMessage(), e);
                continue;
            }
            instances.add(entity);
        }

        expiredInstanceProcessor.persistAndTerminate(nonByocInstances);
        byocExpiredInstanceTerminateService.persistAndTerminate(byocInstances);
        return instances;
    }
}
