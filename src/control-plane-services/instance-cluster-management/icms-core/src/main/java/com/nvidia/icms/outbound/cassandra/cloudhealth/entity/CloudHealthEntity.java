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
package com.nvidia.icms.outbound.cassandra.cloudhealth.entity;

import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import jakarta.validation.constraints.NotNull;
import java.util.HashMap;
import java.util.Map;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;


@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(CloudHealthEntity.TABLE_NAME)
public class CloudHealthEntity {

    public static final String TABLE_NAME = "cloud_health";
    public static final String COLUMN_CLOUD_PROVIDE = "cloud_provider";
    public static final String COLUMN_ZONE = "zone";
    public static final String COLUMN_STATUS = "status";
    public static final String COLUMN_CLUSTER_UPGRADE_STATUS = "cluster_upgrade_status";
    public static final String COLUMN_GPU_USAGE = "gpu_usage";

    @NonNull
    @PrimaryKey
    private CloudHealthKey key;

    @Column(COLUMN_STATUS)
    private CloudHealthStatus status;

    @Column(COLUMN_CLUSTER_UPGRADE_STATUS)
    private String clusterUpgradeStatus;

    @Column(COLUMN_GPU_USAGE)
    private Map<String, GpuCapacity> gpuUsage;

    /**
     * Copy constructor
     * @param other the CloudHealthEntity to copy from
     */
    public CloudHealthEntity(@NotNull CloudHealthEntity other) {
        this.key = CloudHealthKey.builder()
            .cloudProvider(other.key.getCloudProvider())
            .zone(other.key.getZone())
            .build();

        this.status = other.status;
        this.clusterUpgradeStatus = other.clusterUpgradeStatus;

        this.gpuUsage = other.gpuUsage != null ? new HashMap<>() : null;
        if (this.gpuUsage != null) {
            for (Map.Entry<String, GpuCapacity> entry : other.gpuUsage.entrySet()) {
                GpuCapacity original = entry.getValue();
                GpuCapacity copy = GpuCapacity.builder()
                    .capacity(original.getCapacity())
                    .allocated(original.getAllocated())
                    .available(original.getAvailable())
                    .build();
                this.gpuUsage.put(entry.getKey(), copy);
            }
        }
    }

    public String getKeyZoneValue() {
        return key.getZone();
    }

    public Map<String, GpuCapacity> getGpuUsage() {
        if (this.gpuUsage == null) {
            this.gpuUsage = new HashMap<>();
        }

        return this.gpuUsage;
     }

}
