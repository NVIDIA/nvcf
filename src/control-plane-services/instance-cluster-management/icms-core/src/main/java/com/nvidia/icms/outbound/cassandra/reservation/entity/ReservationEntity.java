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

import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;


@Builder(toBuilder = true)
@Data
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@NoArgsConstructor
@Table(ReservationEntity.TABLE_NAME)
public class ReservationEntity {

    public static final String TABLE_NAME = "reservations";
    public static final String COLUMN_RESERVATION_ID = "reservation_id";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_CLUSTER_ID = "cluster_id";
    public static final String COLUMN_GPU_TYPE = "gpu_type";
    public static final String COLUMN_RESERVED_GPU_COUNT = "reserved_gpu_count";
    public static final String COLUMN_AVAILABLE_GPU_COUNT = "available_gpu_count";
    public static final String COLUMN_START_TIME = "start_time";
    public static final String COLUMN_END_TIME = "end_time";
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_DESCRIPTION = "description";
    public static final String COLUMN_LAST_UPDATED_TIME = "last_updated_time";
    public static final String COLUMN_RESERVATION_BACKUP_DISABLED = "reservation_backup_disabled";

    @NonNull
    @PrimaryKey
    @PrimaryKeyColumn(ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    @Column(COLUMN_RESERVATION_ID)
    private UUID reservationId;

    @NonNull
    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @NonNull
    @Column(COLUMN_CLUSTER_ID)
    private String clusterId;

    @NotNull
    @Column(COLUMN_GPU_TYPE)
    private String gpuType;

    @NotNull
    @Column(COLUMN_RESERVED_GPU_COUNT)
    private Integer reservedGpuCount;

    @NotNull
    @Column(COLUMN_AVAILABLE_GPU_COUNT)
    private Double availableGpuCount;

    @NotNull
    @Column(COLUMN_START_TIME)
    private Instant startTime;

    @NotNull
    @Column(COLUMN_END_TIME)
    private Instant endTime;

    @Column(COLUMN_NAME)
    private String name;

    @Column(COLUMN_LAST_UPDATED_TIME)
    private Instant lastUpdatedTime;

    /**
     * When true, this reservation never falls back to another zone if its primary zone is
     * unhealthy. Null for rows written before the column existed, and for reporters that do
     * not send the field, both of which mean backup is allowed.
     */
    @Column(COLUMN_RESERVATION_BACKUP_DISABLED)
    private Boolean reservationBackUpDisabled;

    public String toString() {
        return "ReservationEntity{" +
                "reservationId=" + reservationId +
                ", ncaId=" + ncaId +
                ", clusterId=" + clusterId +
                ", gpuType='" + gpuType + '\'' +
                ", reservedGpuCount=" + reservedGpuCount +
                ", availableGpuCount=" + availableGpuCount +
                ", startTime=" + startTime +
                ", endTime=" + endTime +
                ", name='" + name + '\'' +
                ", lastUpdatedTime=" + lastUpdatedTime +
                ", reservationBackUpDisabled=" + reservationBackUpDisabled +
                '}';
    }

    public boolean isBackupDisabled() {
        return Boolean.TRUE.equals(reservationBackUpDisabled);
    }

    public boolean isActive() {
        Instant nowUtc =  Instant.now();
        return startTime != null &&
                endTime != null &&
                startTime.isBefore(nowUtc) &&
                endTime.isAfter(nowUtc);
    }
}
