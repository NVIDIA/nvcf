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
package com.nvidia.icms.outbound.cassandra.tenantregistration.entity;

import java.time.Instant;
import java.util.Map;
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

/**
 * Entity for tenant registration details stored by registration_id.
 * registration_id: Unique registration ID (UUID) created by SIS.
 * tenant_registration_data: Data to store and propagate downstream (e.g. tenant application IDs).
 * nca_id: NVIDIA Cloud Account ID that owns this registration.
 * function_version_id / function_id: Set during the post registration call.
 */
@Builder(toBuilder = true)
@Data
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@NoArgsConstructor
@Table(TenantRegistrationEntity.TABLE_NAME)
public class TenantRegistrationEntity {

    public static final String TABLE_NAME = "tenant_registration_by_registration_id";
    public static final String COLUMN_REGISTRATION_ID = "registration_id";
    public static final String COLUMN_DEPLOYMENT_ID = "deployment_id";
    public static final String COLUMN_TENANT_REGISTRATION_DATA = "tenant_registration_data";
    public static final String COLUMN_TENANT = "tenant";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_FUNCTION_VERSION_ID = "function_version_id";
    public static final String COLUMN_FUNCTION_ID = "function_id";
    public static final String COLUMN_CREATE_TIME = "create_time";

    @NonNull
    @PrimaryKey
    @PrimaryKeyColumn(ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    @Column(COLUMN_REGISTRATION_ID)
    private UUID registrationId;

    @Nullable
    @Column(COLUMN_DEPLOYMENT_ID)
    private UUID deploymentId;

    @Nullable
    @Column(COLUMN_TENANT_REGISTRATION_DATA)
    private Map<String, String> tenantRegistrationData;

    @Nullable
    @Column(COLUMN_TENANT)
    private String tenant;

    @Nullable
    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @Nullable
    @Column(COLUMN_FUNCTION_VERSION_ID)
    private UUID functionVersionId;

    @Nullable
    @Column(COLUMN_FUNCTION_ID)
    private UUID functionId;

    @Nullable
    @Column(COLUMN_CREATE_TIME)
    private Instant createTime;
}
