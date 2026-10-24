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

import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
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
@Table(ClustersByAccountEntity.TABLE_NAME)
public class ClustersByAccountEntity {

    public static final String TABLE_NAME = "clusters_by_account";
    public static final String COLUMN_REGISTRATION_TIME = "registration_time";
    public static final String COLUMN_CLUSTER_NAME = "cluster_name";
    public static final String COLUMN_CLUSTER_ID = "cluster_id";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_TERMINATION_QUEUE_URL = "termination_queue_url";
    public static final String COLUMN_TERMINATION_QUEUE_TYPE = "termination_queue_type";
    public static final String COLUMN_CLUSTER_DESCRIPTION = "cluster_description";
    public static final String COLUMN_CLUSTER_PROVIDER = "cluster_provider";
    public static final String COLUMN_CLUSTER_STATUS = "cluster_status";
    public static final String COLUMN_CLUSTER_SOURCE = "cluster_source";
    public static final String COLUMN_K8S_VERSION = "k8s_version";
    public static final String COLUMN_CLUSTER_GROUP_NAME = "cluster_group_name";
    public static final String COLUMN_CLUSTER_GROUP_ID = "cluster_group_id";
    public static final String COLUMN_CREATION_QUEUE_URL = "creation_queue_url";
    public static final String COLUMN_CREATION_QUEUE_TYPE = "creation_queue_type";
    public static final String COLUMN_GPUS = "gpus";
    public static final String COLUMN_AUTHORIZED_NCA_IDS = "authorized_nca_ids";
    public static final String COLUMN_CAPABILITIES = "capabilities";
    public static final String COLUMN_ATTRIBUTES = "attributes";
    public static final String COLUMN_GPUS_V2 = "gpus_v2";
    public static final String COLUMN_GPUS_V4 = "gpus_v4";
    public static final String COLUMN_GPUS_V5 = "gpus_v5";
    public static final String COLUMN_NVCA_VERSION = "nvca_version";
    public static final String COLUMN_AUTH_CLIENT_ID = "auth_client_id";
    public static final String COLUMN_REGION = "region";
    public static final String COLUMN_NVCA_LAST_CONNECTED = "nvca_last_connected";
    public static final String COLUMN_CREATION_QUEUES = "creation_queues";
    public static final String COLUMN_CLUSTER_CREATION_QUEUES = "cluster_creation_queues";
    public static final String COLUMN_CLUSTER_CREATION_QUEUES_FOR_TASKS = "cluster_creation_queues_for_tasks";
    public static final String COLUMN_REQUEST_DUMP = "request_dump";
    public static final String COLUMN_CUSTOM_ATTRIBUTES = "custom_attributes";
    public static final String COLUMN_ALLOW_CLUSTER_TARGETING = "allow_cluster_targeting";
    public static final String COLUMN_ALLOW_TASK_CLUSTER_CREATION_QUEUES = "allow_task_cluster_creation_queues";
    public static final String COLUMN_CLUSTER_KEY_ID = "cluster_key_id";

    @PrimaryKey
    @NotNull
    private ClustersByAccountKey key;

    @Column(COLUMN_CLUSTER_ID)
    private String clusterId;

    @Column(COLUMN_REGISTRATION_TIME)
    private Instant registrationTime;

    // common fields with ClusterByGroupIdAndIdEntity
    @Column(COLUMN_CLUSTER_GROUP_ID)
    private String clusterGroupId;

    @Column(COLUMN_TERMINATION_QUEUE_URL)
    private String terminationQueueUrl;

    @Column(COLUMN_TERMINATION_QUEUE_TYPE)
    private String terminationQueueType;

    @Column(COLUMN_CLUSTER_DESCRIPTION)
    private String clusterDescription;

    @Column(COLUMN_CLUSTER_PROVIDER)
    private ClusterProviderEnum clusterProvider;

    @Column(COLUMN_CLUSTER_STATUS)
    private ClusterStatusEnum clusterStatus;

    @Column(COLUMN_CLUSTER_SOURCE)
    private String clusterSource;

    @Column(COLUMN_K8S_VERSION)
    private String k8sVersion;

    @Column(COLUMN_CLUSTER_GROUP_NAME)
    private String clusterGroupName;

    @Column(COLUMN_CREATION_QUEUE_URL)
    private String creationQueueUrl;

    @Column(COLUMN_CREATION_QUEUE_TYPE)
    private String creationQueueType;

    @Column(COLUMN_GPUS)
    private Set<GpuUdt> gpus;

    @Column(COLUMN_AUTHORIZED_NCA_IDS)
    private Set<String> authorizedNcaIds;

    @Column(COLUMN_REQUEST_DUMP)
    private String requestDump;

    @Column(COLUMN_CAPABILITIES)
    private Set<String> capabilities;

    @Column(COLUMN_ATTRIBUTES)
    private Set<String> attributes;

    @Column(COLUMN_GPUS_V4)
    private Set<GpuV4Udt> gpusV4;

    @Column(COLUMN_GPUS_V5)
    private Set<GpuV5Udt> gpusV5;

    @Column(COLUMN_NVCA_VERSION)
    private String nvcaVersion;

    @Column(COLUMN_AUTH_CLIENT_ID)
    private String authClientId;

    @Column(COLUMN_REGION)
    private String region;

    @Column(COLUMN_NVCA_LAST_CONNECTED)
    private Instant nvcaLastConnected;

    @Column(COLUMN_CREATION_QUEUES)
    private Map<String, CreationQueueUdt> creationQueues;

    @Column(COLUMN_CLUSTER_CREATION_QUEUES)
    private Map<String, CreationQueueUdt> clusterCreationQueues;

    @Column(COLUMN_CLUSTER_CREATION_QUEUES_FOR_TASKS)
    private Map<String, CreationQueueUdt> clusterCreationQueuesForTasks;

    @Column(COLUMN_CUSTOM_ATTRIBUTES)
    private Set<String> customAttributes;

    @Column(COLUMN_ALLOW_CLUSTER_TARGETING)
    private Boolean allowClusterTargeting;

    @Column(COLUMN_ALLOW_TASK_CLUSTER_CREATION_QUEUES)
    private Boolean allowTaskClusterCreationQueues;

    @Column(COLUMN_CLUSTER_KEY_ID)
    private String clusterKeyId;

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

    public Set<String> getCapabilities() {
        if (this.capabilities == null) {
            this.capabilities = new HashSet<>();
        }

        return this.capabilities;
    }

    public Set<String> getAttributes() {
        if (this.attributes == null) {
            this.attributes = new HashSet<>();
        }

        return this.attributes;
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

    public Set<String> getCustomAttributes() {
        if (this.customAttributes == null) {
            this.customAttributes = new HashSet<>();
        }

        return this.customAttributes;
    }

    public Set<GpuV5Udt> getGpusV5() {
        if (this.gpusV5 == null) {
            this.gpusV5 = new HashSet<>();
        }
        return this.gpusV5;
    }
}
