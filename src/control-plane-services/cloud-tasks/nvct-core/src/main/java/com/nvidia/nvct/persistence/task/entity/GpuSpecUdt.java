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

package com.nvidia.nvct.persistence.task.entity;

import jakarta.annotation.Nullable;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.UserDefinedType;

@Data
@Builder
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@NoArgsConstructor
@UserDefinedType(GpuSpecUdt.USER_DEFINED_TYPE_NAME)
public class GpuSpecUdt {

    public static final String USER_DEFINED_TYPE_NAME = "gpu_spec_udt";

    @NonNull
    @Column("instance_type")
    private String instanceType;

    @NonNull
    @Column("gpu")
    private String gpu;

    @Nullable
    @Column("backend")
    private String backend;

    @Nullable
    @Column("max_request_concurrency")
    private Integer maxRequestConcurrency;

    @Nullable
    @Column("configuration")
    private String configuration;

    @Nullable
    @Column("clusters")
    private Set<String> clusters;

    @Nullable
    @Column("regions")
    private Set<String> regions;

    @Nullable
    @Column("attributes")
    private Set<String> attributes;

    /**
     * Serialized {@link com.nvidia.nvct.rest.task.dto.HelmValidationPolicyDto} JSON.
     * Null for tasks created before helm validation policy support was added.
     */
    @Nullable
    @Column("helm_validation_policy")
    private String helmValidationPolicy;

}
