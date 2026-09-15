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

import com.nvidia.icms.service.extensions.api.InstanceDestinationProvider;

import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import java.util.Collections;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;

/**
 * No-op implementation of {@link InstanceDestinationProvider} that is registered only when
 * no other {@link InstanceDestinationProvider} bean is present in the application context.
 *
 * <p>Returns safe defaults so that destination-resolution logic does not fail when non-BYOC-specific
 * routing is not configured:
 * <ul>
 *   <li>Returns empty sets for destination and authorized-zone queries.</li>
 *   <li>Returns the input collection unchanged for filtering operations, so no destinations
 *       are inadvertently removed.</li>
 * </ul>
 */
@Slf4j
public class NoOpInstanceDestinationProvider implements InstanceDestinationProvider {

    @Override
    public Set<RequestInstanceDestination> getNonTargetedDestinations(
            SpotInstanceRequestSchema instanceRequest) {
        log.debug("NoOpInstanceDestinationProvider.getNonTargetedDestinations called — returning empty set");
        return Collections.emptySet();
    }

    @Override
    public Set<String> getAuthorizedZones(
            Set<ClusterByGroupIdAndIdEntity> readyClusters, String ncaId) {
        log.debug("NoOpInstanceDestinationProvider.getAuthorizedZones called — returning empty set");
        return Collections.emptySet();
    }

    @Override
    public Set<RequestInstanceDestination> filterForAuthorizedZones(
            Set<RequestInstanceDestination> destinations,
            SpotInstanceRequestSchema instanceRequest,
            Set<String> nonByocAuthorizedZones) {
        log.debug("NoOpInstanceDestinationProvider.filterForAuthorizedZones called — returning destinations unchanged");
        return destinations;
    }

    @Override
    public Set<RequestInstanceDestination> removeForTaskWithLongWait(
            SpotInstanceRequestSchema instanceRequest,
            Set<RequestInstanceDestination> destinations) {
        log.debug("NoOpInstanceDestinationProvider.removeForTaskWithLongWait called — returning destinations unchanged");
        return destinations;
    }
}
