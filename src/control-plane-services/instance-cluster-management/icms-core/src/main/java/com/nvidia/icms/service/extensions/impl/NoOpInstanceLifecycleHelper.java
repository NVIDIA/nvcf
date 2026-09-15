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

import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.sqs.model.GdnLaunchSpecification;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class NoOpInstanceLifecycleHelper implements InstanceLifecycleHelper {

    @Override
    public void sendInstanceRequestSqsMessageForNonByoc(
            String customer,
            Set<RequestInstanceDestination> nonByocDestinations,
            SpotInstanceRequestSchema instanceRequest,
            UUID requestId,
            Map<String, String> envVars,
            @Nullable GdnLaunchSpecification gdnLaunchSpecification) {
        log.debug("NoOpInstanceLifecycleHelper.sendInstanceRequestSqsMessageForNonByoc called — no-op");
    }

    @Override
    public String getGlobalCreationQueueUrlForNonByoc(String gpuName, boolean isRequestForTasks) {
        log.debug("NoOpInstanceLifecycleHelper.getGlobalCreationQueueUrlForNonByoc called — returning empty string");
        return "";
    }

    @Override
    public long getReservationTtlInSeconds(Instant reservationEndTime, Instant currentUtcTime) {
        log.debug("NoOpInstanceLifecycleHelper.getReservationTtlInSeconds called — returning 0");
        return 0L;
    }

    @Override
    public Instant getReservationTtl(Instant reservationEndTime) {
        log.debug("NoOpInstanceLifecycleHelper.getReservationTtl called — returning Instant.EPOCH");
        return Instant.EPOCH;
    }

    @Override
    public boolean useSpotCapacityPostReservedExhausted(String ncaId) {
        log.debug("NoOpInstanceLifecycleHelper.useSpotCapacityPostReservedExhausted called — returning false");
        return false;
    }

    @Override
    public @Nullable CloudProvider validateClusterStatusAndGetCloudProvider(String clusterId) {
        log.debug("NoOpInstanceLifecycleHelper.validateClusterStatusAndGetCloudProvider called — returning null");
        return null;
    }

    @Override
    public boolean isMaxRuntimeDurationValid(SpotInstanceRequestSchema instanceRequest) {
        log.debug("NoOpInstanceLifecycleHelper.isMaxRuntimeDurationValid called — returning true");
        return true;
    }
}
