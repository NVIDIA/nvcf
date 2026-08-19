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
import java.time.Instant;
import java.util.Set;
import java.util.UUID;
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
@Table(FunctionDeploymentEntity.TABLE_NAME)
public class FunctionDeploymentEntity {

    public static final String TABLE_NAME = "functions_deployment_v2";

    public static final String COLUMN_FUNCTION_VERSION_ID = "function_version_id";
    public static final String COLUMN_DEPLOYMENT_ID = "deployment_id";
    public static final String COLUMN_FUNCTION_ID = "function_id";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_HEALTH_INFO = "health_info";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_LAST_UPDATED_AT = "last_updated_at";

    @NonNull
    @PrimaryKey
    private FunctionDeploymentKey key;

    @NonNull
    @Column(COLUMN_DEPLOYMENT_ID)
    private UUID deploymentId;

    @NonNull
    @Column(COLUMN_FUNCTION_ID)
    private UUID functionId;

    @NonNull
    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @Nullable
    @Column(COLUMN_HEALTH_INFO)
    private Set<DeploymentHealthUdt> healthInfo;

    @Column(COLUMN_CREATED_AT)
    @Builder.Default
    private Instant createdAt = Instant.now();

    @Column(COLUMN_LAST_UPDATED_AT)
    @Builder.Default
    private Instant lastUpdatedAt = Instant.now();
}
