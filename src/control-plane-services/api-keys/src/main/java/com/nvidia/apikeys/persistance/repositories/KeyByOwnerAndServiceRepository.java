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

package com.nvidia.apikeys.persistance.repositories;

import static com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel.COLUMN_OWNER_ID;
import static com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel.COLUMN_OWNER_STATUS;
import static com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel.COLUMN_OWNER_STATUS_UPDATED_AT;
import static com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel.COLUMN_OWNER_TYPE;

import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.nvidia.apikeys.persistance.models.KeyByOwnerAndServiceModel;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import org.springframework.data.cassandra.repository.MapIdCassandraRepository;
import org.springframework.data.cassandra.repository.Query;
import org.springframework.data.repository.query.Param;
import org.springframework.stereotype.Repository;

@Repository
public interface KeyByOwnerAndServiceRepository extends
        MapIdCassandraRepository<KeyByOwnerAndServiceModel> {

    List<KeyByOwnerAndServiceModel> findByOwnerTypeAndOwnerIdAndIssuerServiceId(
            KeyOwnerType ownerType, String ownerId, String issuerServiceId);

    Optional<KeyByOwnerAndServiceModel> findByOwnerTypeAndOwnerIdAndIssuerServiceIdAndKeyId(
            KeyOwnerType ownerType, String ownerId, String issuerServiceId, String keyId);

    Optional<KeyByOwnerAndServiceModel> findFirstByOwnerTypeAndOwnerId(
            KeyOwnerType type, String id);

    List<KeyByOwnerAndServiceModel> findByOwnerTypeAndOwnerId(
            KeyOwnerType ownerType, String ownerId);

    @Query("INSERT INTO " + KeyByOwnerAndServiceModel.TABLE_NAME
            + "(" + COLUMN_OWNER_TYPE
            + "," + COLUMN_OWNER_ID
            + "," + COLUMN_OWNER_STATUS
            + "," + COLUMN_OWNER_STATUS_UPDATED_AT + ") "
            + " VALUES "
            + "(:" + COLUMN_OWNER_TYPE
            + ",:" + COLUMN_OWNER_ID
            + ",:" + COLUMN_OWNER_STATUS
            + ",:" + COLUMN_OWNER_STATUS_UPDATED_AT + ") ")
    ResultSet updateKeyOwner(
            @Param(COLUMN_OWNER_TYPE) KeyOwnerType ownerType,
            @Param(COLUMN_OWNER_ID) String ownerId,
            @Param(COLUMN_OWNER_STATUS) KeyOwnerStatus ownerStatus,
            @Param(COLUMN_OWNER_STATUS_UPDATED_AT) Instant ownerStatusUpdatedAt);

}
