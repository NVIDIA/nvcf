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
package com.nvidia.ess.persistence.models;

import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.Map;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import lombok.experimental.SuperBuilder;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;

/**
 * Lightweight namespace model without the entity_types map.
 * Used for operations that only need authorization info (oauth/Notary) but not entity types,
 * avoiding deserialization overhead when namespaces have many (tombstoned) entity types.
 *
 * Can be extended by EntityTypeInNamespaceModel which adds a single entity type field.
 */
@SuperBuilder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor
@Table(NamespaceWithoutEntityTypesModel.TABLE_NAME)
public class NamespaceWithoutEntityTypesModel {

    public static final String TABLE_NAME = "namespaces";
    public static final String COLUMN_NAMESPACE = "namespace";
    public static final String COLUMN_NOTARY_AUTHORIZATIONS = "notary_authorizations";
    public static final String COLUMN_OAUTH_AUTHORIZATIONS = "oauth_authorizations";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_UPDATED_AT = "updated_at";
    public static final String COLUMN_ENTITY_HASH_SIZE = "entity_hash_size";
    public static final String COLUMN_PREVIOUS_ENTITY_HASH_SIZE = "previous_entity_hash_size";
    public static final String COLUMN_DELETED_AT = "deleted_at";
    public static final String COLUMN_REQUIRE_LWT_FOR_SECRET_WRITES = "require_lwt_for_secret_version_writes";
    public static final String COLUMN_AUTHORIZATIONS_VERSION = "authorizations_version";

    @NonNull
    @PrimaryKey
    private String namespace;

    @Nullable
    @Column(COLUMN_NOTARY_AUTHORIZATIONS)
    private Map<String, AuthorizationUdt> notaryAuthorizations;

    /** Column for tenant (non-notary) authorizations. */
    @Nullable
    @Column(COLUMN_OAUTH_AUTHORIZATIONS)
    private Map<String, AuthorizationUdt> oauthAuthorizations;

    @NonNull
    @Builder.Default
    @Column(COLUMN_CREATED_AT)
    private Instant createdAt = Instant.now();

    @NonNull
    @Builder.Default
    @Column(COLUMN_UPDATED_AT)
    private Instant updatedAt = Instant.now();

    @NonNull
    @Column(COLUMN_ENTITY_HASH_SIZE)
    private Integer entityHashSize;

    @Nullable
    @Column(COLUMN_PREVIOUS_ENTITY_HASH_SIZE)
    private Integer previousEntityHashSize;

    @Column(COLUMN_DELETED_AT)
    @Nullable
    private Instant deletedAt;

    @Column(COLUMN_REQUIRE_LWT_FOR_SECRET_WRITES)
    @Nullable
    private Boolean requireLWTForSecretVersionWrites;

    /** Monotonic version (timeuuid) bumped on every write to an authorizations column. */
    @Column(COLUMN_AUTHORIZATIONS_VERSION)
    @Nullable
    private UUID authorizationsVersion;
}
