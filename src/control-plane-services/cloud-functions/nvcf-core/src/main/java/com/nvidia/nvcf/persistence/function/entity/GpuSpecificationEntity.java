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
package com.nvidia.nvcf.persistence.function.entity;

import jakarta.annotation.Nullable;
import java.util.Set;
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
@Table(GpuSpecificationEntity.TABLE_NAME)
public class GpuSpecificationEntity {

    public static final String TABLE_NAME = "gpu_specifications";

    public static final String COLUMN_BACKEND = "backend";
    public static final String COLUMN_GPU = "gpu";
    public static final String COLUMN_MIN_INSTANCES = "min_instances";
    public static final String COLUMN_MAX_INSTANCES = "max_instances";
    public static final String COLUMN_INSTANCE_TYPE = "instance_type";
    public static final String COLUMN_MAX_REQUEST_CONCURRENCY = "max_request_concurrency";
    public static final String COLUMN_CONFIGURATION = "configuration";
    public static final String COLUMN_AVAILABILITY_ZONES = "availability_zones";
    public static final String COLUMN_CLUSTERS = "clusters";
    public static final String COLUMN_REGIONS = "regions";
    public static final String COLUMN_ATTRIBUTES = "attributes";
    public static final String COLUMN_PREFERRED_ORDER = "preferred_order";
    public static final String COLUMN_AUTOSCALING_CONFIG = "autoscaling_configuration";
    public static final String COLUMN_HELM_VALIDATION_POLICY = "helm_validation_policy";

    @PrimaryKey
    private GpuSpecificationKey key;

    @Nullable
    @Column(COLUMN_BACKEND)
    private String backend;

    @NonNull
    @Column(COLUMN_GPU)
    private String gpu;

    @NonNull
    @Column(COLUMN_MIN_INSTANCES)
    private Integer minInstances;

    @NonNull
    @Column(COLUMN_MAX_INSTANCES)
    private Integer maxInstances;

    @Nullable
    @Column(COLUMN_INSTANCE_TYPE)
    private String instanceType;

    @Nullable
    @Column(COLUMN_MAX_REQUEST_CONCURRENCY)
    private Integer maxRequestConcurrency;

    @Nullable
    @Column(COLUMN_CONFIGURATION)
    private String configuration;

    @Nullable
    @Column(COLUMN_AVAILABILITY_ZONES)
    private Set<String> availabilityZones;

    @Nullable
    @Column(COLUMN_CLUSTERS)
    private Set<String> clusters;

    @Nullable
    @Column(COLUMN_REGIONS)
    private Set<String> regions;

    @Nullable
    @Column(COLUMN_ATTRIBUTES)
    private Set<String> attributes;

    @Nullable
    @Column(COLUMN_PREFERRED_ORDER)
    private Integer preferredOrder;

    @Nullable
    @Column(COLUMN_AUTOSCALING_CONFIG)
    private String autoscalingConfig;

    @Nullable
    @Column(COLUMN_HELM_VALIDATION_POLICY)
    private String helmValidationPolicy;
}
