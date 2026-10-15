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
package com.nvidia.nvcf.persistence.account.entity;

import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.Id;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor
@Table(AccountEntity.TABLE_NAME)
public class AccountEntity {

    public static final String TABLE_NAME = "accounts";

    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_CLIENT_IDS = "client_ids";
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_MAX_FUNCTIONS_ALLOWED = "max_functions_allowed";
    public static final String COLUMN_MAX_TASKS_ALLOWED = "max_tasks_allowed";
    public static final String COLUMN_MAX_TELEMETRIES_ALLOWED = "max_telemetries_allowed";
    public static final String COLUMN_MAX_REGISTRY_CREDENTIALS_ALLOWED = "max_registry_credentials_allowed";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_LAST_UPDATED_AT = "last_updated_at";

    @NonNull
    @Id
    @PrimaryKey(COLUMN_NCA_ID)
    private String ncaId;

    @Nullable
    @Column(COLUMN_CLIENT_IDS)
    private Set<String> clientIds;

    @NonNull
    @Column(COLUMN_NAME)
    private String name;

    @NonNull
    @Column(COLUMN_MAX_FUNCTIONS_ALLOWED)
    private Integer maxFunctionsAllowed;

    @NonNull
    @Column(COLUMN_MAX_TASKS_ALLOWED)
    private Integer maxTasksAllowed;

    @NonNull
    @Column(COLUMN_MAX_TELEMETRIES_ALLOWED)
    private Integer maxTelemetriesAllowed;

    @NonNull
    @Column(COLUMN_MAX_REGISTRY_CREDENTIALS_ALLOWED)
    private Integer maxRegistryCredentialsAllowed;

    @Column(COLUMN_CREATED_AT)
    @Builder.Default
    private Instant createdAt = Instant.now();

    @Column(COLUMN_LAST_UPDATED_AT)
    @Builder.Default
    private Instant lastUpdatedAt = Instant.now();
}
