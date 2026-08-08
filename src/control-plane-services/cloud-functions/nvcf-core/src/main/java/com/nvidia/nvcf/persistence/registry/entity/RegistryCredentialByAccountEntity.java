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
package com.nvidia.nvcf.persistence.registry.entity;

import jakarta.annotation.Nullable;
import java.time.Instant;
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
@Table(RegistryCredentialByAccountEntity.TABLE_NAME)
public class RegistryCredentialByAccountEntity {

    public static final String TABLE_NAME = "registry_credentials_by_account";
    public static final String COLUMN_REGISTRY_NAME = "registry_name";
    public static final String COLUMN_REGISTRY_HOSTNAME = "registry_hostname";
    public static final String COLUMN_REGISTRY_CREDENTIAL_NAME = "registry_credential_name";
    public static final String COLUMN_ARTIFACT_TYPES = "artifact_types";
    public static final String COLUMN_TAGS = "tags";
    public static final String COLUMN_DESCRIPTION = "description";
    public static final String COLUMN_PROVISIONED_BY = "provisioned_by";
    public static final String COLUMN_LAST_UPDATED_AT = "last_updated_at";
    public static final String COLUMN_CREATED_AT = "created_at";

    @PrimaryKey
    private RegistryCredentialByAccountKey key;

    @NonNull
    @Column(COLUMN_REGISTRY_NAME)
    private String registryName;

    @NonNull
    @Column(COLUMN_REGISTRY_HOSTNAME)
    private String registryHostname;

    @NonNull
    @Column(COLUMN_REGISTRY_CREDENTIAL_NAME)
    private String registryCredentialName;

    @NonNull
    @Column(COLUMN_ARTIFACT_TYPES)
    private Set<ArtifactType> artifactTypes;

    @Nullable
    @Column(COLUMN_TAGS)
    private Set<String> tags;

    @Nullable
    @Column(COLUMN_DESCRIPTION)
    private String description;

    @NonNull
    @Column(COLUMN_PROVISIONED_BY)
    private ProvisionedBy provisionedBy;

    @Builder.Default
    @Column(COLUMN_LAST_UPDATED_AT)
    private Instant lastUpdatedAt = Instant.now();

    @Builder.Default
    @Column(COLUMN_CREATED_AT)
    private Instant createdAt = Instant.now();

}
