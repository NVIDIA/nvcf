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

import com.nvidia.icms.service.extensions.api.InstanceTerminationService;

import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import java.util.ArrayList;
import java.util.Map;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class NoOpInstanceTerminationService implements InstanceTerminationService {

    @Override
    public void publishInstanceTerminationMessage(
            String customer, String zone, String requestId, String instanceId) {
        log.debug("NoOpInstanceTerminationService.publishInstanceTerminationMessage called — no-op");
    }

    @Override
    public TerminateInstancesResponse terminateInstances(
            String customer,
            Set<InstanceV2Entity> instanceEntities,
            Map<String, Object> auditProps) {
        log.debug("NoOpInstanceTerminationService.terminateInstances called — returning empty response");
        return new TerminateInstancesResponse(new ArrayList<>());
    }

    @Override
    public TerminateInstancesResponse terminateInstanceRequests(
            String customer,
            Map<String, InstanceV2Entity> runningInstancesEntityMap,
            Map<String, Object> auditProps) {
        log.debug("NoOpInstanceTerminationService.terminateInstanceRequests called — returning empty response");
        return new TerminateInstancesResponse(new ArrayList<>());
    }

    @Override
    public void sendSnsTerminationMessage(
            String requestId, String customer, String zone, Set<String> instanceIds) {
        log.debug("NoOpInstanceTerminationService.sendSnsTerminationMessageForNonByoc called — no-op");
    }
}
