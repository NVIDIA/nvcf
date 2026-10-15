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
package com.nvidia.icms.service.heartbeats;

import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.Map;

public interface HeartbeatService<T, C, R> {

    void sendHeartbeatEvent(
            @NotNull String clusterId,
            @NotNull CloudProvider cloudProvider,
            String eventName,
            @NotNull Map<String, Object> metadata);

    R recordClusterHeartbeat(@NotNull String clusterId, @NotNull T request);

    void logHeartbeatReceived(@NotNull T heartbeatRequest, @NotNull  String cluster, @NotNull ResourceProvider resourceProvider);

    @Nullable
    String getGpuUsageAsString(@NotNull T heartbeatRequest);

    @Nullable
    String getHeartbeatRequestAsString(@NotNull T heartbeatRequest);

    @NotNull
    Map<String, Object> createMetadataForEvent(@NotNull T heartbeatRequest);

    @NotNull
    Map<String, GpuCapacity> toGpuCapacityMap(@NotNull T heartbeatRequest);

    @NotNull
    Map<String, C> getCapacityStats(@NotNull T heartbeatRequest);

    void recordHeartbeat(@NotNull String clusterId,
                         @NotNull T heartbeatRequest,
                         @NotNull ResourceProvider resourceProvider,
                         @NotNull CloudHealthStatus cloudHealthStatus,
                         @Nullable String upgradeStatus,
                         @NotNull String heartbeatEvent,
                         int ttl,
                         @NotNull ClusterEntity clusterEntity);

    void recordLastHealthyHeartbeatReportTime(@NotNull ClusterEntity clusterEntity,
                                              @NotNull CloudHealthStatus cloudHealthStatus);

    ClusterEntity getClusterInfo(String clusterId);
}
