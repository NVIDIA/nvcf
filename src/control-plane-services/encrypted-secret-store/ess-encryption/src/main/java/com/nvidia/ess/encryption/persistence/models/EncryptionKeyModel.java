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

import static com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry.maskKid;

import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.UUID;
import java.util.function.UnaryOperator;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Builder(toBuilder = true)
public class EncryptionKeyModel {
    public static final String TABLE = "encryption_keys_by_kid";
    public static final String COLUMN_NAMESPACE = "namespace";
    public static final String COLUMN_KID = "kid";
    public static final String COLUMN_ENCRYPTED_KEY = "encrypted_key";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_ENCRYPTED_AT = "encrypted_at";
    public static final String COLUMN_ENCRYPTED_BY_KID = "encrypted_by_kid";

    @NonNull
    @Column(COLUMN_NAMESPACE)
    @PrimaryKeyColumn(value = COLUMN_NAMESPACE, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String namespace;

    @NonNull
    @PrimaryKeyColumn(value = COLUMN_KID, ordinal = 1, type = PrimaryKeyType.CLUSTERED)
    private String kid;

    @NonNull
    @Column(COLUMN_CREATED_AT)
    private UUID createdAt;

    @NonNull
    @Column(COLUMN_ENCRYPTED_KEY)
    private String encryptedKey;

    // Nullable for backwards compatibility in future
    @Nullable
    @Column(COLUMN_ENCRYPTED_BY_KID)
    private String encryptedByKid;

    @Nullable
    @Column(COLUMN_ENCRYPTED_AT)
    private Instant encryptedAt;

    public EncryptionKeyByTimestampModel toEncryptionKeyByTimestampModel() {
        return EncryptionKeyByTimestampModel.builder()
                .namespace(namespace)
                .kid(kid)
                .createdAt(createdAt)
                .encryptedKey(encryptedKey)
                .encryptedByKid(encryptedByKid)
                .encryptedAt(encryptedAt)
                .build();
    }

    public UnaryOperator<String> logMessageFormatter() {
        return errMsg -> String.format(
                "%s | namespace: %s, KID: %s, created_at: %s, encrypted_at: %s", errMsg,
                getNamespace(), maskKid(getKid()), getCreatedAt(), getEncryptedAt()
        );
    }
}
