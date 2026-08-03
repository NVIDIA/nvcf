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

package com.nvidia.apikeys.persistance.models;

import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.boot.jwt.services.mapping.annotation.EncryptedFields;
import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(KeyByOwnerAndServiceModel.TABLE_NAME)
public class KeyByOwnerAndServiceModel {

    public static final String TABLE_NAME = "keys_by_owner_and_service";
    public static final String COLUMN_OWNER_TYPE = "owner_type";
    public static final String COLUMN_OWNER_ID = "owner_id";
    public static final String COLUMN_ISSUER_SERVICE_ID = "issuer_service_id";
    public static final String COLUMN_KEY_ID = "key_id";
    public static final String COLUMN_OWNER_STATUS = "owner_status";
    public static final String COLUMN_OWNER_STATUS_UPDATED_AT = "owner_status_updated_at";
    public static final String COLUMN_EXPIRES_AT = "expires_at";
    public static final String COLUMN_DELETES_AT = "deletes_at";
    public static final String COLUMN_KEY_STATUS = "key_status";
    public static final String COLUMN_KEY_DETAILS = "key_details";

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_OWNER_TYPE, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private KeyOwnerType ownerType;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_OWNER_ID, ordinal = 1, type = PrimaryKeyType.PARTITIONED)
    private String ownerId;

    @PrimaryKeyColumn(name = COLUMN_ISSUER_SERVICE_ID, ordinal = 3, type = PrimaryKeyType.CLUSTERED)
    private String issuerServiceId;

    @PrimaryKeyColumn(name = COLUMN_KEY_ID, ordinal = 4, type = PrimaryKeyType.CLUSTERED)
    private String keyId;

    @Column(COLUMN_OWNER_STATUS)
    private KeyOwnerStatus ownerStatus;

    @Column(COLUMN_OWNER_STATUS_UPDATED_AT)
    private Instant ownerStatusUpdatedAt;

    @Column(COLUMN_EXPIRES_AT)
    private Instant expiresAt;

    @Column(COLUMN_DELETES_AT)
    private Instant deletesAt;

    @Column(COLUMN_KEY_STATUS)
    private KeyStatus keyStatus;

    @Column(COLUMN_KEY_DETAILS)
    @EncryptedFields(encryptionKeyName = "payload_jwe_kid", valueObject = KeyByOwnerAndServiceVo.class)
    private String keyDetails;
}
