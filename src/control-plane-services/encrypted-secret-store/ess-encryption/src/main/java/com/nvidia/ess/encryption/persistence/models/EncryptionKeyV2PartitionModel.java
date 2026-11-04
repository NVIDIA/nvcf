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
package com.nvidia.ess.encryption.persistence.models;

import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import lombok.experimental.SuperBuilder;
import org.springframework.data.cassandra.core.cql.Ordering;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

@Data
@AllArgsConstructor
@NoArgsConstructor
@SuperBuilder
@Table(EncryptionKeyV2PartitionModel.TABLE)
public class EncryptionKeyV2PartitionModel {

    // add to EncryptionKeyNamingStrategy later if needed
    public static final String TABLE = "encryption_keys_by_kid_and_encrypted_at";
    public static final String COLUMN_NAMESPACE = "namespace";
    public static final String COLUMN_KID = "kid";
    public static final String COLUMN_ENCRYPTED_AT = "encrypted_at";
    public static final String COLUMN_CURRENT_KID = "current_kid";

    @NonNull
    @PrimaryKeyColumn(value = COLUMN_NAMESPACE, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String namespace;

    @NonNull
    @PrimaryKeyColumn(value = COLUMN_KID, ordinal = 1, type = PrimaryKeyType.CLUSTERED,
            ordering = Ordering.ASCENDING)
    private String kid;

    @NonNull
    @PrimaryKeyColumn(value = COLUMN_ENCRYPTED_AT, ordinal = 2, type = PrimaryKeyType.CLUSTERED,
            ordering = Ordering.DESCENDING)
    private Instant encryptedAt;

    @NonNull
    @Column(value = COLUMN_CURRENT_KID, isStatic = true)
    private String currentKid;
}
