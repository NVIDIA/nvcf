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
package com.nvidia.icms.outbound.cassandra.request.entity;

import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import java.time.Instant;
import java.util.HashSet;
import java.util.Set;
import java.util.UUID;
import jakarta.annotation.Nullable;
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
@Table(InstanceRequestV2Entity.TABLE_NAME)
public class InstanceRequestV2Entity {

    public static final String TABLE_NAME = "requests";
    public static final String COLUMN_REQUEST_ID = "request_id";
    public static final String COLUMN_CREATE_TIME_UUID = "create_timeuuid";
    public static final String COLUMN_CUSTOMER = "customer";
    public static final String COLUMN_ACTION = "action";
    public static final String COLUMN_REQUEST = "request";
    public static final String COLUMN_STATE = "state";
    public static final String COLUMN_STATUS_CODE = "status_code";
    public static final String COLUMN_STATUS_MESSAGE = "status_message";
    public static final String COLUMN_STATUS_UPDATE_TIME = "status_update_time";
    public static final String COLUMN_RESOURCE_PROVIDER = "resource_provider";
    public static final String COLUMN_CHECK_BATCHWISE_INFO= "check_batchwise_info";
    public static final String COLUMN_CLUSTERS = "clusters";
    public static final String COLUMN_REGIONS = "regions";
    public static final String COLUMN_ATTRIBUTES = "attributes";
    public static final String COLUMN_CUSTOM_ATTRIBUTES = "custom_attributes";
    public static final String COLUMN_INSTANCE_COUNT = "instance_count";
    public static final String COLUMN_TASK_ID = "task_id";
    public static final String COLUMN_MAX_QUEUE_DURATION = "max_queued_duration";
    public static final String COLUMN_ACCOUNT_NAME = "account_name";
    public static final String COLUMN_FUNCTION_ID = "function_id";
    public static final String COLUMN_FUNCTION_VERSION_ID = "function_version_id";
    public static final String COLUMN_DEPLOYMENT_ID = "deployment_id";
    public static final String COLUMN_GPU_SPECIFICATION_ID = "gpu_specification_id";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_GPU_COUNT_PER_INSTANCE = "gpu_count_per_instance";

    @NonNull
    @PrimaryKey
    @PrimaryKeyColumn(ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    @Column(COLUMN_REQUEST_ID)
    private String requestId;

    @Column(COLUMN_CREATE_TIME_UUID)
    private UUID createTimeuuid;

    @Column(COLUMN_CUSTOMER)
    private String customer;

    @Column(COLUMN_ACTION)
    private SpotInstanceRequestAction action;

    @Column(COLUMN_REQUEST)
    private String request;

    @Column(COLUMN_STATE)
    private SpotInstanceRequestState state;

    @Column(COLUMN_STATUS_CODE)
    private String statusCode;

    @Column(COLUMN_STATUS_MESSAGE)
    private String statusMessage;

    @Column(COLUMN_STATUS_UPDATE_TIME)
    private Instant statusUpdateTime;

    @Column(COLUMN_RESOURCE_PROVIDER)
    private ResourceProvider resourceProvider;

    @Column(COLUMN_CHECK_BATCHWISE_INFO)
    private Boolean checkBatchwiseInfo;

    @Column(COLUMN_CLUSTERS)
    private Set<String> clusters;

    @Column(COLUMN_REGIONS)
    private Set<String> regions;

    @Column(COLUMN_ATTRIBUTES)
    private Set<String> attributes;

    @Column(COLUMN_CUSTOM_ATTRIBUTES)
    private Set<String> customAttributes;

    @Column(COLUMN_INSTANCE_COUNT)
    private int instanceCount;

    @Column(COLUMN_TASK_ID)
    private UUID taskId;

    @Column(COLUMN_MAX_QUEUE_DURATION)
    private String maxQueuedDuration;

    @Column(COLUMN_ACCOUNT_NAME)
    private String accountName;

    @Column(COLUMN_FUNCTION_ID)
    private UUID functionId;

    @Column(COLUMN_FUNCTION_VERSION_ID)
    private UUID functionVersionId;

    @Column(COLUMN_DEPLOYMENT_ID)
    private UUID deploymentId;

    @Column(COLUMN_GPU_SPECIFICATION_ID)
    private UUID gpuSpecificationId;

    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @Column(COLUMN_GPU_COUNT_PER_INSTANCE)
    private Integer gpuCountPerInstance;

    public Set<String> getClusters() {
        if (this.clusters == null) {
            this.clusters = new HashSet<>();
        }

        return this.clusters;
    }

    public Set<String> getRegions() {
        if (this.regions == null) {
            this.regions = new HashSet<>();
        }

        return this.regions;
    }

    public Set<String> getAttributes() {
        if (this.attributes == null) {
            this.attributes = new HashSet<>();
        }

        return this.attributes;
    }

    public Set<String> getCustomAttributes() {
        if (this.customAttributes == null) {
            this.customAttributes = new HashSet<>();
        }

        return this.customAttributes;
    }
}
