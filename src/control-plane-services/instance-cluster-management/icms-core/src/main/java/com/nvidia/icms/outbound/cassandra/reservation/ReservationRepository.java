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
package com.nvidia.icms.outbound.cassandra.reservation;



import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.util.TimeUtils;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;

import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.data.domain.Limit;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class ReservationRepository {
    final ReservationRepo reservationRepo;

    private final IcmsConfigurationProperties icmsConfigurationProperties;


    @Observed
    public ReservationEntity insert(@NotNull ReservationEntity entity,
                                    @NotNull long ttl) {

        try {
            entity.setLastUpdatedTime(TimeUtils.getNowTruncatedToMs());
            reservationRepo.insertWithTtl(entity, Duration.ofSeconds(ttl), false);
            return entity;
        } catch (Exception exception) {
            log.error(
                    "class:ReservationRepository function: insert, failed to insert entry {} : {}",
                    entity.getReservationId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", "Failed to insert Reservation", exception.getMessage()), exception);
        }
    }

    @Observed
    public void delete(@NotNull ReservationEntity entity) {
        try {
            reservationRepo.deleteById(entity.getReservationId());
        } catch (Exception exception) {
            log.error(
                    "class:ReservationRepository function: delete, failed to delete entry {} : {}",
                    entity.getReservationId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", "Failed to delete Reservation", exception.getMessage()), exception);
        }
    }

    @Observed
    public ReservationEntity update(@NotNull ReservationEntity entity) {
        try {
            entity.setLastUpdatedTime(TimeUtils.getNowTruncatedToMs());
            reservationRepo.update(entity);
            return entity;
        } catch (Exception exception) {
            log.error(
                    "class:ReservationRepository function: delete, failed to delete entry {} : {}",
                    entity.getReservationId(), exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", "Failed to update Reservation", exception.getMessage()), exception);
        }
    }

    public Optional<ReservationEntity> findById(@NotNull UUID reservationId) {
        try {
            return reservationRepo.findByReservationId(reservationId);
        } catch (Exception exception) {
            log.error(
                    "class:ReservationRepository function: findById, failed to find entry {} : {}",
                    reservationId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", "Failed to find Reservation", exception.getMessage()), exception);
        }
    }

    public List<ReservationEntity> findAll() {
        try {
            return reservationRepo.findAllBy(Limit.unlimited());
        } catch (Exception exception) {
            log.error(
                    "class:ReservationRepository function: findByAll, failed to find all entries: {}",
                    exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", "Failed to find all reservations", exception.getMessage()), exception);
        }
    }

    public List<ReservationEntity> findByAllByNcaId(String ncaId) {
        try {
            return reservationRepo.findAllByNcaId(ncaId);
        } catch (Exception exception) {
            log.error(
                    "class:ReservationRepository function: findByAllByNcaId, failed to find  reservation for ncaId {}: {}",
                    ncaId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", "Failed to find reservations per ncaId", exception.getMessage()), exception);
        }
    }

    public List<ReservationEntity> findByAllByClusterId(String clusterId) {
        try {
            return reservationRepo.findAllByClusterId(clusterId);
        } catch (Exception exception) {
            log.error(
                    "class:ReservationRepository function: findByAllByClusterId, failed to find  reservation for clusterId {}: {}",
                    clusterId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", "Failed to find reservations per clusterId", exception.getMessage()), exception);
        }
    }

    public List<ReservationEntity> findByAllByNcaIdAndClusterId(String ncaId, String clusterId) {
        try {
            return reservationRepo.findAllByNcaIdAndClusterId(ncaId, clusterId);
        } catch (Exception exception) {
            log.error(
                    "class:ReservationRepository function: findByAllByNcaIdAndClusterId, failed to find reservation for ncaId {} and clusterId {}: {}",
                    ncaId, clusterId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(String.format("%s , error: %s", "Failed to find reservations per ncaId and clusterId", exception.getMessage()), exception);
        }
    }
}
