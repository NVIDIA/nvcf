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
import java.util.HashSet;
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
@Table(ClusterGroupByGroupIdEntity.TABLE_NAME)
public class ClusterGroupByGroupIdEntity {

    public static final String TABLE_NAME = "cluster_group_by_cluster_group_id";
    public static final String COLUMN_CLUSTER_GROUP_NAME = "cluster_group_name";
    public static final String COLUMN_CLUSTER_GROUP_ID = "cluster_group_id";
    public static final String COLUMN_CREATION_QUEUE_URL = "creation_queue_url";
    public static final String COLUMN_CREATION_QUEUE_TYPE = "creation_queue_type";
    public static final String COLUMN_GPUS = "gpus";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_AUTHORIZED_NCA_IDS = "authorized_nca_ids";

    @PrimaryKey
    @NotNull
    @Column(COLUMN_CLUSTER_GROUP_ID)
    private String clusterGroupId;

    @Column(COLUMN_CLUSTER_GROUP_NAME)
    private String clusterGroupName;

    @Column(COLUMN_CREATION_QUEUE_URL)
    private String creationQueueUrl;

    @Column(COLUMN_CREATION_QUEUE_TYPE)
    private String creationQueueType;

    @Column(COLUMN_GPUS)
    private Set<GpuUdt> gpus;

    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @Column(COLUMN_AUTHORIZED_NCA_IDS)
    private Set<String> authorizedNcaIds;

    public Set<GpuUdt> getGpus() {
        if (this.gpus == null) {
            this.gpus = new HashSet<>();
        }

        return this.gpus;
    }

    public Set<String> getAuthorizedNcaIds() {
        if (this.authorizedNcaIds == null) {
            this.authorizedNcaIds = new HashSet<>();
        }

        return this.authorizedNcaIds;
    }


}
