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

import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import jakarta.validation.constraints.NotNull;

import java.util.List;
import java.util.Map;
import java.util.Set;

public interface ReservationProcessor {

    Set<RequestInstanceDestination> filterDestinationBasedOnReservation(
            @NotNull Set<RequestInstanceDestination> destinations,
            @NotNull SpotInstanceRequestSchema instanceRequest,
            @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId);

    Double calculateAvailableCapacityForUnhealthyZone(ReservationEntity reservation);

    Double calculateAvailableCapacityForHealthyZone(ReservationEntity reservation);

    /**
     * Returns the list of currently active reservations owned by the given NCA, or an empty list
     * if backend support for reservations is disabled / ncaId is blank / no active reservations
     * exist.
     *
     * <p>Moved from {@code ClusterGpuInfoHelper.getActiveReservationsPerNcaId} so that
     * {@code ClusterGpuInfoHelper} (core code) no longer needs a direct injection of
     * {@code ReservationRepository}; the backend module owns reservation lookup.</p>
     */
    List<ReservationEntity> getActiveReservationsForNcaId(@NotNull String ncaId);
}
