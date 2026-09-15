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

import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.jwt.services.mapping.annotation.EncryptedFields;
import com.nvidia.boot.observability.tracing.redaction.DoNotTraceValue;
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
@Table(KeyModel.TABLE_NAME)
public class KeyModel {

    public static final String TABLE_NAME = "keys";
    public static final String COLUMN_API_KEY_HASH = "api_key_hash";
    public static final String COLUMN_STATUS = "status";
    public static final String COLUMN_EXPIRES_AT = "expires_at";
    public static final String COLUMN_DELETES_AT = "deletes_at";
    public static final String COLUMN_KEY_DETAILS = "key_details";

    @DoNotTraceValue
    @NonNull
    @PrimaryKeyColumn(name = COLUMN_API_KEY_HASH, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String keyHash;

    @NonNull
    @Column(COLUMN_STATUS)
    private KeyStatus keyStatus;

    @Column(COLUMN_EXPIRES_AT)
    private Instant expiresAt;

    @Column(COLUMN_DELETES_AT)
    private Instant deletesAt;

    @EncryptedFields(encryptionKeyName = "payload_jwe_kid", valueObject = KeyVo.class)
    @Column(COLUMN_KEY_DETAILS)
    private String keyDetails;
}
