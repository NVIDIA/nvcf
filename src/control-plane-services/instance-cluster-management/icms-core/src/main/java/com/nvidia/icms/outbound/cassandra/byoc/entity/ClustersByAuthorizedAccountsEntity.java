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

import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
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
@Table(ClustersByAuthorizedAccountsEntity.TABLE_NAME)
public class ClustersByAuthorizedAccountsEntity {

    public static final String TABLE_NAME = "clusters_by_authorized_accounts";
    public static final String COLUMN_NCA_ID_KEY = "nca_id_key";
    public static final String COLUMN_CLUSTER_NAME = "cluster_name";
    public static final String COLUMN_CLUSTER_ID = "cluster_id";
    public static final String COLUMN_CLUSTER_GROUP_NAME = "cluster_group_name";
    public static final String COLUMN_CLUSTER_GROUP_ID = "cluster_group_id";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_CREATION_QUEUES = "creation_queues";
    public static final String COLUMN_CLUSTER_CREATION_QUEUES = "cluster_creation_queues";
    public static final String COLUMN_CLUSTER_CREATION_QUEUES_FOR_TASKS = "cluster_creation_queues_for_tasks";
    public static final String COLUMN_GPUS_V2 = "gpus_v2";
    public static final String COLUMN_GPUS_V4 = "gpus_v4";
    public static final String COLUMN_GPUS_V5 = "gpus_v5";
    public static final String COLUMN_NVCA_LAST_CONNECTED = "nvca_last_connected";
    public static final String COLUMN_AUTHORIZED_NCA_IDS = "authorized_nca_ids";
    public static final String COLUMN_CLUSTER_KEY_ID = "cluster_key_id";

    @PrimaryKey
    @NotNull
    ClustersByAuthorizedAccountsKey key;

    @Column(COLUMN_CLUSTER_NAME)
    private String clusterName;

    @Column(COLUMN_CLUSTER_GROUP_NAME)
    private String clusterGroupName;

    @Column(COLUMN_CLUSTER_GROUP_ID)
    private String clusterGroupId;

    @Column(COLUMN_AUTHORIZED_NCA_IDS)
    private Set<String> authorizedNcaIds;

    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @Column(COLUMN_CREATION_QUEUES)
    private Map<String, CreationQueueUdt> creationQueues;

    @Column(COLUMN_GPUS_V4)
    private Set<GpuV4Udt> gpusV4;

    @Column(COLUMN_GPUS_V5)
    private Set<GpuV5Udt> gpusV5;

    @Column(COLUMN_NVCA_LAST_CONNECTED)
    private Instant nvcaLastConnected;

    @Column(COLUMN_CLUSTER_CREATION_QUEUES)
    private Map<String, CreationQueueUdt> clusterCreationQueues;

    @Column(COLUMN_CLUSTER_CREATION_QUEUES_FOR_TASKS)
    private Map<String, CreationQueueUdt> clusterCreationQueuesForTasks;

    @Column(COLUMN_CLUSTER_KEY_ID)
    private String clusterKeyId;

    public Set<String> getAuthorizedNcaIds() {
        if (this.authorizedNcaIds == null) {
            this.authorizedNcaIds = new HashSet<>();
        }

        return this.authorizedNcaIds;
    }

    public Set<GpuV4Udt> getGpusV4() {
        if (this.gpusV4 == null) {
            this.gpusV4 = new HashSet<>();
        }

        return this.gpusV4;
    }

    public Map<String, CreationQueueUdt> getCreationQueues() {
        if (this.creationQueues == null) {
            this.creationQueues = new HashMap<>();
        }

        return this.creationQueues;
    }

    public Map<String, CreationQueueUdt> getClusterCreationQueues() {
        if (this.clusterCreationQueues == null) {
            this.clusterCreationQueues = new HashMap<>();
        }

        return this.clusterCreationQueues;
    }

    public Map<String, CreationQueueUdt> getClusterCreationQueueForTasks() {
        if (this.clusterCreationQueuesForTasks == null) {
            this.clusterCreationQueuesForTasks = new HashMap<>();
        }

        return this.clusterCreationQueuesForTasks;
    }

    public Set<GpuV5Udt> getGpusV5() {
        if (this.gpusV5 == null) {
            this.gpusV5 = new HashSet<>();
        }
        return this.gpusV5;
    }
}
