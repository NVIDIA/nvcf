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
package com.nvidia.icms.outbound.cassandra.reservation.entity;

import org.junit.jupiter.api.Test;
import java.time.Instant;
import java.util.UUID;
import java.util.function.Consumer;
import static org.junit.jupiter.api.Assertions.*;

class ReservationEntityTest {

    private ReservationEntity createReservation(Consumer<ReservationEntity.ReservationEntityBuilder> customizer) {
        ReservationEntity.ReservationEntityBuilder builder = ReservationEntity.builder()
                .reservationId(UUID.randomUUID())
                .ncaId("test-nca")
                .clusterId("test-cluster")
                .gpuType("test-gpu")
                .reservedGpuCount(1)
                .availableGpuCount(1.0);

        if (customizer != null) {
            customizer.accept(builder);
        }

        return builder.build();
    }

    @Test
    void isActive_WhenReservationIsActive_ReturnsTrue() {
        // Arrange
        Instant now = Instant.now();
        ReservationEntity reservation = createReservation(builder -> 
            builder.startTime(now.minusSeconds(3600))  // 1 hour ago
                  .endTime(now.plusSeconds(3600))      // 1 hour from now
        );

        // Act & Assert
        assertTrue(reservation.isActive());
    }

    @Test
    void isActive_WhenReservationHasNotStarted_ReturnsFalse() {
        // Arrange
        Instant now = Instant.now();
        ReservationEntity reservation = createReservation(builder -> 
            builder.startTime(now.plusSeconds(3600))   // 1 hour from now
                  .endTime(now.plusSeconds(7200))      // 2 hours from now
        );

        // Act & Assert
        assertFalse(reservation.isActive());
    }

    @Test
    void isActive_WhenReservationHasEnded_ReturnsFalse() {
        // Arrange
        Instant now = Instant.now();
        ReservationEntity reservation = createReservation(builder -> 
            builder.startTime(now.minusSeconds(7200))  // 2 hours ago
                  .endTime(now.minusSeconds(3600))     // 1 hour ago
        );

        // Act & Assert
        assertFalse(reservation.isActive());
    }

    @Test
    void isActive_WhenStartTimeIsNull_ReturnsFalse() {
        // Arrange
        Instant now = Instant.now();
        ReservationEntity reservation = createReservation(builder -> 
            builder.startTime(null)
                  .endTime(now.plusSeconds(3600))
        );

        // Act & Assert
        assertFalse(reservation.isActive());
    }

    @Test
    void isActive_WhenEndTimeIsNull_ReturnsFalse() {
        // Arrange
        Instant now = Instant.now();
        ReservationEntity reservation = createReservation(builder -> 
            builder.startTime(now.minusSeconds(3600))
                  .endTime(null)
        );

        // Act & Assert
        assertFalse(reservation.isActive());
    }
} 
