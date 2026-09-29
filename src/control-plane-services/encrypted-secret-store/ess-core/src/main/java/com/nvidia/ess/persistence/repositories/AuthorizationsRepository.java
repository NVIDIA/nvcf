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
package com.nvidia.ess.persistence.repositories;

import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_NAMESPACE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_NOTARY_AUTHORIZATIONS;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_OAUTH_AUTHORIZATIONS;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_AUTHORIZATIONS_VERSION;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.TABLE_NAME;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.ess.persistence.models.AuthorizationUdt;
import com.nvidia.ess.persistence.models.NamespaceModel;
import org.springframework.data.cassandra.repository.Query;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Mono;

@Repository
public interface AuthorizationsRepository extends ReactiveCassandraRepository<NamespaceModel, String> {

    /**
     * Adds a tenant (non-notary) authorization to the {@code oauth_authorizations} column and bumps
     * {@code authorizations_version} in one atomic single-partition {@code UPDATE}.
     */
    @Query("UPDATE " + TABLE_NAME + " SET "
            + COLUMN_OAUTH_AUTHORIZATIONS + "[ :key ] = :value, "
            + COLUMN_AUTHORIZATIONS_VERSION + " = now() "
            + "WHERE "
            + COLUMN_NAMESPACE + " = :namespace")
    Mono<Void> addNonNotaryAuthorization(String namespace, String key, AuthorizationUdt value);

    // Element-delete via map[key] = null (instead of DELETE) so the same UPDATE can bump the version.
    @Query("UPDATE " + TABLE_NAME + " SET "
            + COLUMN_OAUTH_AUTHORIZATIONS + "[ :key ] = null, "
            + COLUMN_AUTHORIZATIONS_VERSION + " = now() "
            + "WHERE "
            + COLUMN_NAMESPACE + " = :namespace")
    Mono<Void> removeNonNotaryAuthorization(String namespace, String key);

    /**
     * Test-only: overwrite a non-notary tenant authorization's {@code oauth_authorizations} entry with
     * an explicit, non-null {@code authorization.type} column value. The mapped {@link AuthorizationUdt}
     * model no longer carries {@code type}, so this writes a fully-specified UDT literal to prove auth is
     * agnostic to the DB value of {@code type}.
     */
    @VisibleForTesting
    @Query("UPDATE " + TABLE_NAME + " SET " + COLUMN_OAUTH_AUTHORIZATIONS + "[ :key ] = "
            + "{" + AuthorizationUdt.COLUMN_ID + ": :id, "
            + AuthorizationUdt.COLUMN_NAME + ": :name, "
            + AuthorizationUdt.COLUMN_JWKS_URL + ": :jwksUrl, "
            + AuthorizationUdt.COLUMN_ISSUER + ": :issuer, "
            + "type: :type} "
            + "WHERE " + COLUMN_NAMESPACE + " = :namespace")
    Mono<Void> overwriteNonNotaryAuthorizationWithType(String namespace, String key, String id,
            String name, String jwksUrl, String issuer, String type);

    @Query("UPDATE " + TABLE_NAME + " SET "
            + COLUMN_NOTARY_AUTHORIZATIONS + "[ :key ] = :value, "
            + COLUMN_AUTHORIZATIONS_VERSION + " = now() "
            + "WHERE "
            + COLUMN_NAMESPACE + " = :namespace")
    Mono<Void> addNotaryAuthorization(String namespace, String key, AuthorizationUdt value);

    // Element-delete via map[key] = null (instead of DELETE) so the same UPDATE can bump the version.
    @Query("UPDATE " + TABLE_NAME + " SET "
            + COLUMN_NOTARY_AUTHORIZATIONS + "[ :key ] = null, "
            + COLUMN_AUTHORIZATIONS_VERSION + " = now() "
            + "WHERE "
            + COLUMN_NAMESPACE + " = :namespace")
    Mono<Void> removeNotaryAuthorization(String namespace, String key);
}
