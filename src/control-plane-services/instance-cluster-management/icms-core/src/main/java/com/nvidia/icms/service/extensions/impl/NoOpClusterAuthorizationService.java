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

import com.nvidia.icms.service.extensions.api.ClusterAuthorizationService;

import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import jakarta.annotation.Nullable;
import lombok.extern.slf4j.Slf4j;

import java.util.Set;

/**
 * No-op default {@link ClusterAuthorizationService} registered when no
 * other {@code ClusterAuthorizationService} bean is present in the
 * application context.
 *
 * <p>Reports that no authorized non-BYOC cluster group exists and leaves any
 * supplied cluster set unchanged.
 */
@Slf4j
public class NoOpClusterAuthorizationService implements ClusterAuthorizationService {

    @Override
    @Nullable
    public ClusterGroups fetchAuthorizedClusterGroup(
            String requestingNcaId,
            InstanceTypeUsageEnum instanceTypeUsageEnum) {
        return null;
    }

    @Override
    public void filterAuthorizedClusters(
            String requestingNcaId,
            Set<ClusterByGroupIdAndIdEntity> allClusters) {
        // No-op: non-BYOC module is not active so there are no non-BYOC clusters to filter.
    }

    @Override
    public boolean isDetailedTargetingFlowEnabled() {
        // No-op: non-BYOC module is not active so the detailed-targeting flow is never enabled.
        return false;
    }
}
