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
package com.nvidia.icms.outbound.cassandra.instance.entity;

import com.datastax.oss.driver.api.mapper.annotations.ClusteringColumn;
import com.datastax.oss.driver.api.mapper.annotations.CqlName;
import com.datastax.oss.driver.api.mapper.annotations.Entity;
import com.datastax.oss.driver.api.mapper.annotations.PartitionKey;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.HashSet;
import java.util.Set;
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
@Table(InstanceByZoneEntity.TABLE_NAME)
public class InstanceByZoneEntity {

    public static final String TABLE_NAME = "instances_by_zone";
    public static final String COLUMN_CUSTOMER = "customer";
    public static final String COLUMN_TRUNCATED_TS = "truncated_ts";
    public static final String COLUMN_REQUEST_ID = "request_id";
    public static final String COLUMN_INSTANCE_ID = "instance_id";
    public static final String COLUMN_IMAGE_ID = "image_id";
    public static final String COLUMN_REQUEST_STATE = "request_state";
    public static final String COLUMN_TIMESTAMP = "timestamp";
    public static final String COLUMN_UPDATE_TIMESTAMP = "update_timestamp";
    public static final String COLUMN_ZONE = "zone";
    public static final String COLUMN_INSTANCE_STATE_CODE = "instance_state_code";
    public static final String COLUMN_INSTANCE_STATE_NAME = "instance_state_name";
    public static final String COLUMN_REQUEST_STATUS_CODE = "request_status_code";
    public static final String COLUMN_REQUEST_STATUS_MESSAGE = "request_status_message";
    public static final String COLUMN_REQUEST_STATUS_UPDATE_TIME = "request_status_update_time";
    public static final String COLUMN_RESOURCE_PROVIDER = "resource_provider";
    public static final String COLUMN_ERROR_LOG = "error_log";
    public static final String COLUMN_ERROR_SOURCE = "error_source";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_INSTANCE_TYPE = "instance_type";
    public static final String COLUMN_BACKEND = "backend";
    public static final String COLUMN_GPU = "gpu";
    public static final String COLUMN_INSTANCE_IPS = "instance_ips";
    public static final String COLUMN_REGION = "region";
    public static final String COLUMN_ATTRIBUTES = "attributes";
    public static final String COLUMN_CUSTOM_ATTRIBUTES = "custom_attributes";
    public static final String COLUMN_REQUEST = "request";


    @NotNull
    @PrimaryKey
    private InstanceByZoneKey key;

    @Column(COLUMN_REQUEST_ID)
    private String requestId;

    @Column(COLUMN_IMAGE_ID)
    private String imageId;

    @Column(COLUMN_REQUEST_STATE)
    private SpotInstanceRequestState requestState;

    @Column(COLUMN_UPDATE_TIMESTAMP)
    private Instant updateTimestamp;

    @Column(COLUMN_CUSTOMER)
    private String customer;

    @Column(COLUMN_INSTANCE_STATE_CODE)
    private Integer instanceStateCode;

    @Column(COLUMN_INSTANCE_STATE_NAME)
    private SpotInstanceInternalState instanceStateName;

    @Column(COLUMN_REQUEST_STATUS_CODE)
    private SpotInstanceStatus requestStatusCode;

    @Column(COLUMN_REQUEST_STATUS_MESSAGE)
    private String requestStatusMessage;

    @Column(COLUMN_REQUEST_STATUS_UPDATE_TIME)
    private Instant requestStatusUpdateTime;

    @Column(COLUMN_RESOURCE_PROVIDER)
    private ResourceProvider resourceProvider;

    @Column(COLUMN_ERROR_LOG)
    private String errorLog;

    @Column(COLUMN_ERROR_SOURCE)
    private String errorSource;

    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @Column(COLUMN_INSTANCE_TYPE)
    private String instanceType;

    @Column(COLUMN_BACKEND)
    private String backend;

    @Column(COLUMN_GPU)
    private String gpu;

    @Column(COLUMN_INSTANCE_IPS)
    private Set<String> instanceIps;

    @Column(COLUMN_REGION)
    private String region;

    @Column(COLUMN_ATTRIBUTES)
    private Set<String> attributes;

    @Column(COLUMN_CUSTOM_ATTRIBUTES)
    private Set<String> customAttributes;

    // This will store JSON String of ClientRequestDataModel
    @Column(COLUMN_REQUEST)
    private String request;

    public Set<String> getInstanceIps() {
        if (this.instanceIps == null) {
            this.instanceIps = new HashSet<>();
        }

        return this.instanceIps;
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

    public String getInstanceId() {
        return getKey().getInstanceId();
    }

}
