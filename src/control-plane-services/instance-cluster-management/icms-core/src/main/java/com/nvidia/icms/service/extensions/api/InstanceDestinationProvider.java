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

import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import jakarta.validation.constraints.NotNull;

import java.util.Set;

/**
 * Provides backend-specific destination logic for instance creation.
 *
 * <p>Responsibilities:
 * <ul>
 *   <li>Resolving backend-specific non-targeted destinations from authorized NCA accounts.</li>
 *   <li>Determining which cluster IDs a given NCA ID is explicitly authorized for.</li>
 *   <li>Filtering a destination set down to only those the NCA ID may use.</li>
 *   <li>Removing backend destinations that cannot satisfy a task's long {@code maxRuntimeDuration}.</li>
 * </ul>
 */
public interface InstanceDestinationProvider {

    /**
     * Returns all backend destinations available for the given non-targeted instance request.
     */
    @NotNull Set<RequestInstanceDestination> getNonTargetedDestinations(
            @NotNull SpotInstanceRequestSchema instanceRequest);

    /**
     * Returns the subset of cluster IDs in {@code readyClusters} that are explicitly
     * authorized for {@code ncaId} (i.e. the cluster's {@code authorizedNcaIds} contains the NCA).
     */
    @NotNull Set<String> getAuthorizedZones(
            @NotNull Set<ClusterByGroupIdAndIdEntity> readyClusters,
            @NotNull String ncaId);

    /**
     * Filters {@code destinations} so that only destinations the NCA ID may legally use are retained.
     * Always removes destinations whose cluster is specifically authorized for a <em>different</em> NCA ID.
     */
    @NotNull Set<RequestInstanceDestination> filterForAuthorizedZones(
            @NotNull Set<RequestInstanceDestination> destinations,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Set<String> nonByocAuthorizedZones);

    /**
     * Removes destinations from the set when the request is for a task whose
     * {@code maxRuntimeDuration} exceeds the backend-specific maximum.  If this removal leaves
     * the set empty, a {@link com.nvidia.icms.errors.IcmsBadRequestException} is thrown.
     *
     * @throws com.nvidia.icms.errors.IcmsBadRequestException if the request targets a task with
     *         a {@code maxRuntimeDuration} that exceeds the backend-specific maximum and no other
     *         destinations remain after filtering
     */
    @NotNull Set<RequestInstanceDestination> removeForTaskWithLongWait(
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Set<RequestInstanceDestination> destinations);
}
