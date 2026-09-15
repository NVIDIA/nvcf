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
package com.nvidia.icms.service.extensions.api;

import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.cluster.InstancesRequestInfoResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import jakarta.validation.constraints.NotNull;

import java.util.Map;
import java.util.Set;

public interface InstanceLifecycleService {

    TerminateInstancesResponse terminateInstances(
            String customer,
            Set<InstanceV2Entity> instanceEntities,
            Map<String, Object> auditProps);

    TerminateInstancesResponse terminateInstanceRequests(
            String customer,
            Map<String, InstanceV2Entity> runningInstancesEntityMap,
            Map<String, Object> auditProps);

    CreateSpotInstancesResponse requestNonByocInstances(
            @NotNull String customer,
            @NotNull SpotInstanceRequestSchema instanceRequestSchema,
            @NotNull Map<String, Object> auditProps,
            @NotNull Set<RequestInstanceDestination> destinations);

    InstancesRequestInfoResponse getInstanceRequestInfo(String requestId);
}
