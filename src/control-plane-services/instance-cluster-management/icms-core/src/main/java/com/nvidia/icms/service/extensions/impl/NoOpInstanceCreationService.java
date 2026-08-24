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

import com.nvidia.icms.service.extensions.api.InstanceCreationService;

import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.sqs.model.GdnLaunchSpecification;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import jakarta.annotation.Nullable;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;

/**
 * No-op implementation of {@link InstanceCreationService} that is registered only when no other
 * {@link InstanceCreationService} bean is present in the application context.
 *
 * <p>All methods perform no action, ensuring that instance creation does not fail
 * when non-BYOC-specific SQS messaging and telemetry are not configured.
 */
@Slf4j
public class NoOpInstanceCreationService implements InstanceCreationService {

    /**
     * In normal implementations, enqueues one SQS creation message per non-BYOC destination.
     * This no-op implementation performs no action.
     */
    @Override
    public void sendSqsMessages(
            String customer,
            Set<RequestInstanceDestination> nonByocDestinations,
            SpotInstanceRequestSchema instanceRequest,
            UUID requestId,
            Map<String, String> envVars,
            @Nullable GdnLaunchSpecification gdnLaunchSpecification) {
        log.debug("NoOpInstanceCreationService.sendSqsMessages called — no-op");
    }

    /**
     * In normal implementations, emits a telemetry event for each non-BYOC destination in the request.
     * This no-op implementation performs no action.
     */
    @Override
    public void sendTelemetry(
            Set<RequestInstanceDestination> nonByocDestinations,
            SpotInstanceRequestSchema instanceRequest,
            InstanceRequestV2Entity instanceRequestEntity,
            Map<String, String> envVars,
            @Nullable GdnLaunchSpecification gdnLaunchSpecification) {
        log.debug("NoOpInstanceCreationService.sendTelemetry called — no-op");
    }
}
