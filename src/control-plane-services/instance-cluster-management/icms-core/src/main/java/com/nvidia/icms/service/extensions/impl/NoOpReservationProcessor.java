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

import com.nvidia.icms.service.extensions.api.ReservationProcessor;

import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import jakarta.validation.constraints.NotNull;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class NoOpReservationProcessor implements ReservationProcessor {

    @Override
    public Set<RequestInstanceDestination> filterDestinationBasedOnReservation(
            Set<RequestInstanceDestination> destinations,
            SpotInstanceRequestSchema instanceRequest,
            Map<String, CloudHealthEntity> cloudHealthByClusterId) {
        log.debug("NoOpReservationProcessor.filterDestinationBasedOnReservation called — returning destinations unchanged");
        return destinations;
    }

    @Override
    public Double calculateAvailableCapacityForUnhealthyZone(ReservationEntity reservation) {
        log.debug("NoOpReservationProcessor.calculateAvailableCapacityForUnhealthyZone called — returning 0.0");
        return 0.0;
    }

    @Override
    public Double calculateAvailableCapacityForHealthyZone(ReservationEntity reservation) {
        log.debug("NoOpReservationProcessor.calculateAvailableCapacityForHealthyZone called — returning 0.0");
        return 0.0;
    }

    @Override
    public List<ReservationEntity> getActiveReservationsForNcaId(@NotNull String ncaId) {
        log.debug("NoOpReservationProcessor.getActiveReservationsForNcaId called for ncaId {} — returning empty list",
                  ncaId);
        return new ArrayList<>();
    }
}
