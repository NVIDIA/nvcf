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
package com.nvidia.icms.outbound.cassandra.byoc.entity;

import java.util.Map;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(ClusterConfigurationByClusterIdEntity.TABLE_NAME)
public class ClusterConfigurationByClusterIdEntity {

    public static final String TABLE_NAME = "cluster_configuration_by_cluster_id";
    public static final String COLUMN_CLUSTER_ID = "cluster_id";
    public static final String COLUMN_CLUSTER_CONFIGURATIONS = "cluster_configurations";
    public static final String COLUMN_CLUSTER_CONFIGURATION_FILES = "cluster_configuration_files";

    @PrimaryKey
    @Column(COLUMN_CLUSTER_ID)
    private String clusterId;

    @Column(COLUMN_CLUSTER_CONFIGURATIONS)
    @Nullable
    private Map<String, String> clusterConfigurations;

    @Column(COLUMN_CLUSTER_CONFIGURATION_FILES)
    @Nullable
    private Map<String, String> clusterConfigurationFiles;
}


