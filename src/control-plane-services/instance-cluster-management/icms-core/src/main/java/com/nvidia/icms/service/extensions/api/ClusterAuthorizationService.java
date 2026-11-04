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

import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;

import java.util.Set;

/**
 * Interface used by core code to obtain backend-authorized cluster views
 * without depending on backend-specific configuration, repositories, or filtering
 * logic.
 */
public interface ClusterAuthorizationService {

    /**
     * Build an authorized {@code ClusterGroups} response (or {@code null})
     * for the given requester using the detailed-targeting flow.
     *
     * <p>The implementation is expected to:
     * <ol>
     *   <li>Fetch all ready clusters under the cluster group.</li>
     *   <li>Apply NCA-id authorization filtering (specific authorization,
     *       public/wildcard authorization, plus the backend-specific
     *       authorization-filtering-enabled flag).</li>
     *   <li>Aggregate the filtered clusters into a single response,
     *       applying the supplied {@code instanceTypeUsageEnum} filter.</li>
     * </ol>
     *
     * @param requestingNcaId         the NCA id making the request
     * @param instanceTypeUsageEnum   instance-type usage filter to apply
     * @return the aggregated cluster group, or {@code null}
     */
    @Nullable
    ClusterGroups fetchAuthorizedClusterGroup(
            @NotNull String requestingNcaId,
            @NotNull InstanceTypeUsageEnum instanceTypeUsageEnum);

    /**
     * Returns whether the non-BYOC detailed-targeting flow (authorized cluster aggregation via
     * {@link #fetchAuthorizedClusterGroup}) is enabled for this deployment.
     *
     * <p>When {@code false}, callers should treat back to the legacy BART path. The default no-op
     * implementation returns {@code false} so that core code running without the internal
     * backend registered never enters the detailed-targeting branch.</p>
     */
    boolean isDetailedTargetingFlowEnabled();

    /**
     * Apply backend-specific authorization filtering to the supplied mutable set of
     * clusters in place. BYOC clusters in the set are left untouched.
     *
     * <p>Internal backend-specific implementation removes those not authorized
     * for the requester (according to the same specific/public/wildcard
     * rules as {@link #fetchAuthorizedClusterGroup}).
     *
     * <p>The default no-op implementation leaves the set unchanged.
     *
     * @param requestingNcaId  the NCA id making the request
     * @param allClusters      mutable set of clusters in scope; filtered in
     *                         place
     */
    void filterAuthorizedClusters(
            @NotNull String requestingNcaId,
            @NotNull Set<ClusterByGroupIdAndIdEntity> allClusters);
}
