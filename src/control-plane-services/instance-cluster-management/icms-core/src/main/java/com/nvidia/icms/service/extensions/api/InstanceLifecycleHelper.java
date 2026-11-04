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

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.sqs.model.GdnLaunchSpecification;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import jakarta.validation.constraints.NotNull;

import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

public interface InstanceLifecycleHelper {

    void sendInstanceRequestSqsMessageForNonByoc(@NotNull String customer,
                                              @NotNull Set<RequestInstanceDestination> nonByocDestinations,
                                              @NotNull SpotInstanceRequestSchema instanceRequest,
                                              @NotNull UUID requestId,
                                              @NotNull Map<String, String> envVars,
                                              @Nullable GdnLaunchSpecification gdnLaunchSpecification);

    @NotNull String getGlobalCreationQueueUrlForNonByoc(@NotNull String gpuName, boolean isRequestForTasks);

    long getReservationTtlInSeconds(@NotNull Instant reservationEndTime, @NotNull Instant currentUtcTime);

    @NotNull Instant getReservationTtl(@NotNull Instant reservationEndTime);

    boolean useSpotCapacityPostReservedExhausted(String ncaId);

    /**
     * Looks up the cluster by {@code clusterId}, checks it is in READY status, and returns its
     * {@link CloudProvider}. Returns {@code null} if the cluster is not READY (e.g. unhealthy).
     *
     * @param clusterId the cluster to validate
     * @return the cloud provider, or {@code null} if the cluster is not ready
     */
    @Nullable CloudProvider validateClusterStatusAndGetCloudProvider(@NotNull String clusterId);

    /**
     * Returns {@code true} if the request has a {@code maxRuntimeDuration} that is within the
     * backend specific limit defined in configuration.
     *
     * @param instanceRequest the instance request to check
     * @return {@code true} if the duration is present and within the configured backend limit
     */
    boolean isMaxRuntimeDurationValid(@NotNull SpotInstanceRequestSchema instanceRequest);
}
