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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.factory.RandomFactory;
import com.nvidia.icms.factory.UpdateEntity;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.util.TimeUtils;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import jakarta.annotation.Nullable;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.springframework.beans.factory.annotation.Autowired;

import static org.mockito.Mockito.when;

public class ReservationRepositoryTest extends IntegrationTest {

    @Autowired
    private ReservationRepo reservationRepo;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    private ReservationRepository reservationRepository;


    @BeforeEach
    void init() {
        reservationRepository = new ReservationRepository(reservationRepo,
                                                          icmsConfigurationProperties);
    }



    @Test
    void insertAndFind() {
        ReservationEntity reservationEntity = createDefaultActiveReservation( r -> r.setLastUpdatedTime(TimeUtils.getPreviousDate(1)));

        //Act
        ReservationEntity inserted = reservationRepository.insert(reservationEntity, 300L);
        Optional<ReservationEntity> inDb = reservationRepository.findById(reservationEntity.getReservationId());

        //assert
        assertTrue(compareReservationEntities(reservationEntity, inserted, false));
        assertNotNull(inserted.getLastUpdatedTime());
        assertTrue(inDb.isPresent());
        assertTrue(compareReservationEntities(inDb.get(), inserted, true));
    }

    @Test
    void delete() {
        ReservationEntity reservationEntity = createDefaultActiveReservation(null);

        //Act
        reservationRepository.delete(reservationEntity);
        Optional<ReservationEntity> inDb = reservationRepository.findById(reservationEntity.getReservationId());

        //assert
        assertFalse(inDb.isPresent());
    }


    @Test
    void update() {
        ReservationEntity reservationEntity = createDefaultActiveReservation(null);
        ReservationEntity reservationEntity2 = createDefaultActiveReservation(null);
        reservationEntity2.setReservationId(reservationEntity.getReservationId());

        reservationRepository.insert(reservationEntity, 300L);

        //Act
        reservationRepository.update(reservationEntity2);
        Optional<ReservationEntity> inDb = reservationRepository.findById(reservationEntity.getReservationId());

        //assert
        assertTrue(inDb.isPresent());
        assertTrue(compareReservationEntities(inDb.get(), reservationEntity2, false));
    }


    @Test
    void findAll() {
        //Arrange
        List<ReservationEntity> reservations = insertDefaultActiveReservations(3,null);

        //Act

        List<ReservationEntity> reservationsInDb = reservationRepository.findAll();

        //Assert
        assertEquals(3, reservationsInDb.size());

        reservations.forEach(r -> {
            ReservationEntity inDb = findById(r.getReservationId(), reservationsInDb);
            assertTrue(compareReservationEntities(inDb, r, true));
        });

    }

    @Test
    void findByAllByNcaId() {
        //Arrange
        List<ReservationEntity> reservations = insertDefaultActiveReservations(3,null);

        //Act

        List<ReservationEntity> reservationsInDb = reservationRepository.findByAllByNcaId(reservations.getFirst().getNcaId());

        //Assert
        assertEquals(1, reservationsInDb.size());
        assertTrue(compareReservationEntities(reservations.getFirst(), reservationsInDb.getFirst(), true));
    }

    @Test
    void findByAllByClusterId() {
        //Arrange
        List<ReservationEntity> reservations = insertDefaultActiveReservations(3,null);

        //Act

        List<ReservationEntity> reservationsInDb = reservationRepository.findByAllByClusterId(reservations.getFirst().getClusterId());

        //Assert
        assertEquals(1, reservationsInDb.size());
        assertTrue(compareReservationEntities(reservations.getFirst(), reservationsInDb.getFirst(), true));
    }

    @Test
    void findByAllByNcaIdAndClusterId() {
        //Arrange
        UUID ncaId = UUID.randomUUID();
        List<ReservationEntity> reservations = insertDefaultActiveReservations(3,r -> r.setNcaId(ncaId.toString())); //  all of them for the same ncaID

        //Act

        List<ReservationEntity> reservationsInDb = reservationRepository.findByAllByNcaIdAndClusterId(reservations.getFirst().getNcaId(), reservations.getFirst().getClusterId());

        //Assert
        assertEquals(1, reservationsInDb.size());
        assertTrue(compareReservationEntities(reservations.getFirst(), reservationsInDb.getFirst(), true));
    }

    private ReservationEntity createReservationWithDuration(long durationSeconds) {
        Instant endTime = Instant.now().plus(Duration.ofSeconds(durationSeconds));
        
        return ReservationEntity.builder()
                .reservationId(UUID.randomUUID())
                .ncaId(UUID.randomUUID().toString())
                .clusterId(UUID.randomUUID().toString())
                .gpuType(RandomFactory.getRandomStringWithPrefix("GPU", 5))
                .reservedGpuCount(0)
                .availableGpuCount(4.0)
                .startTime(Instant.now().minus(Duration.ofHours(1))) // Started 1 hour ago
                .endTime(endTime)
                .name(RandomFactory.getRandomStringWithPrefix("Reservation", 5))
                .build();
    }

    private ReservationEntity createDefaultActiveReservation( @Nullable UpdateEntity<ReservationEntity> action) {
        return ReservationEntity.builder()
                .reservationId(UUID.randomUUID())
                .ncaId(UUID.randomUUID().toString())
                .clusterId(UUID.randomUUID().toString())
                .gpuType(RandomFactory.getRandomStringWithPrefix("GPU", 5))
                .reservedGpuCount(0)
                .availableGpuCount(0.0)
                .startTime(TimeUtils.getPreviousDate(1))
                .endTime(TimeUtils.getPreviousDate(-1))
                .name(RandomFactory.getRandomStringWithPrefix("Reservation", 5))
                .build();
    }


    private List<ReservationEntity> insertDefaultActiveReservations(int count, @Nullable UpdateEntity<ReservationEntity> action) {
        List<ReservationEntity> result = new ArrayList<>();
        for(int i = 0; i < count; i++) {
            ReservationEntity  reservation = createDefaultActiveReservation(action);
            reservationRepository.insert(reservation, 300L);
            result.add(reservation);
        }

        return result;
    }

    private ReservationEntity findById(@NotNull UUID reservationId, @NotNull List<ReservationEntity> source) {
        return source.stream().filter(r -> r.getReservationId().equals(reservationId)).findFirst().orElse(null);
    }

    /**
     * Compares two ReservationEntity objects for equality by comparing all their fields.
     * 
     * @param entity1 The first ReservationEntity to compare
     * @param entity2 The second ReservationEntity to compare
     * @return true if all fields are equal, false otherwise
     */
    private boolean compareReservationEntities(ReservationEntity entity1, ReservationEntity entity2, boolean compareLastUpdatedTime) {
        if (entity1 == entity2) {
            return true;
        }
        if (entity1 == null || entity2 == null) {
            return false;
        }

        return Objects.equals(entity1.getReservationId(), entity2.getReservationId()) &&
               Objects.equals(entity1.getNcaId(), entity2.getNcaId()) &&
               Objects.equals(entity1.getClusterId(), entity2.getClusterId()) &&
               Objects.equals(entity1.getGpuType(), entity2.getGpuType()) &&
               Objects.equals(entity1.getReservedGpuCount(), entity2.getReservedGpuCount()) &&
               Objects.equals(entity1.getAvailableGpuCount(), entity2.getAvailableGpuCount()) &&
               Objects.equals(entity1.getStartTime(), entity2.getStartTime()) &&
               Objects.equals(entity1.getEndTime(), entity2.getEndTime()) &&
               Objects.equals(entity1.getName(), entity2.getName()) &&
               (!compareLastUpdatedTime || Objects.equals(entity1.getLastUpdatedTime(), entity2.getLastUpdatedTime()));
    }

}
