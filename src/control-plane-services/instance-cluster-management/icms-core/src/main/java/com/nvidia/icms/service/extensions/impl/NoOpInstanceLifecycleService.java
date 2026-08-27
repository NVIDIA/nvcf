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

import com.nvidia.icms.service.extensions.api.InstanceLifecycleService;

import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.cluster.InstancesRequestInfoResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import java.util.ArrayList;
import java.util.Map;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;

/**
 * No-op implementation of {@link InstanceLifecycleService} that is registered only when no other
 * {@link InstanceLifecycleService} bean is present in the application context.
 *
 * <p>All methods perform no action and return empty responses without side effects, ensuring
 * that instance lifecycle operations do not fail when no managed integration is configured.
 */
@Slf4j
public class NoOpInstanceLifecycleService implements InstanceLifecycleService {

    @Override
    public TerminateInstancesResponse terminateInstances(
            String customer,
            Set<InstanceV2Entity> instanceEntities,
            Map<String, Object> auditProps) {
        log.debug("NoOpInstanceLifecycleService.terminateInstances called — returning empty response");
        return new TerminateInstancesResponse(new ArrayList<>());
    }

    @Override
    public TerminateInstancesResponse terminateInstanceRequests(
            String customer,
            Map<String, InstanceV2Entity> runningInstancesEntityMap,
            Map<String, Object> auditProps) {
        log.debug("NoOpInstanceLifecycleService.terminateInstanceRequests called — returning empty response");
        return new TerminateInstancesResponse(new ArrayList<>());
    }

    @Override
    public CreateSpotInstancesResponse requestNonByocInstances(
            String customer,
            SpotInstanceRequestSchema instanceRequestSchema,
            Map<String, Object> auditProps,
            Set<RequestInstanceDestination> destinations) {
        log.debug("NoOpInstanceLifecycleService.requestNonByocInstances called — returning empty response");
        return new CreateSpotInstancesResponse();
    }

    @Override
    public InstancesRequestInfoResponse getInstanceRequestInfo(String requestId) {
        log.debug("NoOpInstanceLifecycleService.getInstanceRequestInfo called — returning empty response");
        return new InstancesRequestInfoResponse();
    }
}
