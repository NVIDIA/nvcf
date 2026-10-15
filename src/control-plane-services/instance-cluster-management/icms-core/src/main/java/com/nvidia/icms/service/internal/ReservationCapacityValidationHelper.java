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
package com.nvidia.icms.service.internal;

import com.nvidia.icms.configuration.bean.InstanceTypeConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.reservation.ReservationRepository;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.ReservationProcessor;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;

import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import jakarta.annotation.Nullable;

import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import static com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository.isCloudHealthy;

@Slf4j
@Service
@AllArgsConstructor
public class ReservationCapacityValidationHelper {

    private final ReservationRepository reservationRepository;
    private final ReservationProcessor reservationProcessor;
    private final InstanceTypeConfigurationProperties instanceTypeConfigurationProperties;
    private final InstanceServiceHelper instanceServiceHelper;
    private final CloudHealthRepository cloudHealthRepository;

    public Optional<ReservationEntity> validateAndGetReservationEntity(@Nullable UUID reservationId) {

        // 0. Validate if reservationId provided
        if (reservationId == null) {
            return Optional.empty();
        }

        // 1. Validate if provided reservationId is valid
        Optional<ReservationEntity> reservationOpt = reservationRepository.findById(reservationId);
        if (reservationOpt.isEmpty()) {
            reportAndThrowError(String.format("Invalid reservationId %s provided", reservationId));
        }

        // 2. Validate if the reservation is active
        ReservationEntity reservationEntity = reservationOpt.get();
        if (!reservationEntity.isActive()) {
            reportAndThrowError(String.format("Reservation %s is not active", reservationId));
        }

        return Optional.of(reservationEntity);
    }

    @Observed
    public void validateReservationBackupCapacityForInstanceStateUpdate(@NotNull ReservationEntity reservationEntity,
                                                                        @NotNull InstanceRequestV2Entity instanceRequestEntity) {

        int incomingGpuCount = calculateGpuCountForInstance(instanceRequestEntity);
        validateReservedBackupCapacity(reservationEntity, instanceRequestEntity, incomingGpuCount);
    }

    @Observed
    public void validateReservationBackupCapacityForRequestStateUpdate(@NotNull ReservationEntity reservationEntity,
                                                                       @NotNull InstanceRequestV2Entity instanceRequestEntity,
                                                                       @NotNull Integer incomingInstanceCount) {

        // Calculate incoming GPU requirement and use unified validation
        Integer gpuCountPerInstance = calculateGpuCountForInstance(instanceRequestEntity);
        int incomingGpuCount = incomingInstanceCount * gpuCountPerInstance;

        validateReservedBackupCapacity(reservationEntity, instanceRequestEntity, incomingGpuCount);
    }

    /**
     * Calculates GPU count for an instance based on request data with fallback logic
     */
    public Integer calculateGpuCountForInstance(@NotNull InstanceRequestV2Entity instanceRequestEntity) {
        Integer gpuCount = instanceRequestEntity.getGpuCountPerInstance();
        if (gpuCount == null || gpuCount <= 0) {
            // Fallback: get GPU count from instance type
            ClientRequestDataModel.LaunchSpecification launchSpec = instanceServiceHelper.parseRequestInfo(instanceRequestEntity.getRequest()).getLaunchSpecification();
            gpuCount = instanceTypeConfigurationProperties.getGpuCountForInstanceType(launchSpec.getInstanceType());
        }
        return gpuCount;
    }


    private void validateReservedBackupCapacity(@NotNull ReservationEntity reservation,
                                                @NotNull InstanceRequestV2Entity instanceRequestEntity,
                                                @NotNull Integer incomingGpuCount) {
        /*
        1. Validate if the zone for which reservation backup will be provided is healthy
            a. This will help to fast fail the request as primary zone has become healthy so we don't need to serve RESERVATION_BACKUP
        2. Validate if incoming GPUs can be accepted for reservation
         */

        try {

            // 1. Validate if the zone for which reservation backup will be provided is healthy
            Map<String, CloudHealthEntity> cloudHealthByClusterId = cloudHealthRepository.findAllInMap();
            if (isCloudHealthy(cloudHealthByClusterId.get(reservation.getClusterId()))) {
                reportAndThrowError(String.format("%s primary zone for %s reservation has become healthy, rejecting RESERVATION_BACKUP support",
                        reservation.getClusterId(), reservation.getReservationId()));
            }

            // 2. Validate if incoming GPUs can be accepted for reservation
            Double availableCapacity = reservationProcessor.calculateAvailableCapacityForUnhealthyZone(reservation);

            // 2.a If reservation is fully utilized, reject the upcoming request
            if (availableCapacity.intValue() <= 0) {
                reportAndThrowError(String.format("Reservation %s is fully utilized", reservation.getReservationId()));
            }

            // 2.b If reservation is available but incoming GPUs are more than available capacity, reject the request
            if (availableCapacity.intValue() < incomingGpuCount) {
                reportAndThrowError(String.format("Insufficient reserved capacity. For %s reservation provided %d GPUs but only %d reserved GPUs available",
                        reservation.getReservationId(), incomingGpuCount, availableCapacity.intValue()));
            }

        } catch (PreConditionFailedException e) {
            throw e; // Re-throw validation failures

        } catch (Exception e) {
            log.error("Error during RESERVED_BACKUP validation for request: {}, reservationId: {}, error: {}.",
                    instanceRequestEntity.getRequestId(), reservation.getReservationId(), e.getMessage());
            throw e; // Don't ignore unexpected errors
        }
    }

    private void reportAndThrowError(@NotNull String errorMessage) {
        log.error(errorMessage);
        throw new PreConditionFailedException(errorMessage);
    }
}
