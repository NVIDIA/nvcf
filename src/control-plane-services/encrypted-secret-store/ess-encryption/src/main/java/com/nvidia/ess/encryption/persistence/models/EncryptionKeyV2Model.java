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
import java.util.UUID;
import java.util.function.UnaryOperator;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.EqualsAndHashCode;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import lombok.ToString;
import lombok.experimental.SuperBuilder;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.Table;

@EqualsAndHashCode(callSuper = true)
@ToString(callSuper = true)
@Data
@AllArgsConstructor
@NoArgsConstructor
@SuperBuilder
@Table(EncryptionKeyV2Model.TABLE)
public class EncryptionKeyV2Model extends EncryptionKeyV2PartitionModel {

    // add to EncryptionKeyNamingStrategy later if needed
    public static final String TABLE = "encryption_keys_by_kid_and_encrypted_at";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_ENCRYPTED_KEY = "encrypted_key";
    public static final String COLUMN_STATUS = "status";
    public static final String COLUMN_ENCRYPTED_BY_KID = "encrypted_by_kid";

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

    @NonNull
    @Column(COLUMN_STATUS)
    private String status;

    public EncryptionKeyModel toEncryptionKeyByKidModel() {
        return EncryptionKeyModel.builder()
                .namespace(this.getNamespace())
                .kid(this.getKid())
                .createdAt(createdAt)
                .encryptedKey(encryptedKey)
                .encryptedByKid(encryptedByKid)
                .encryptedAt(this.getEncryptedAt())
                .build();
    }

    public EncryptionKeyByTimestampModel toEncryptionKeyByTimestampModel() {
        return EncryptionKeyByTimestampModel.builder()
                .namespace(this.getNamespace())
                .kid(this.getKid())
                .createdAt(createdAt)
                .encryptedKey(encryptedKey)
                .encryptedByKid(encryptedByKid)
                .encryptedAt(this.getEncryptedAt())
                .build();
    }

    public UnaryOperator<String> logMessageFormatter() {
        String currentStatus = this.status;
        return errMsg -> String.format(
                "%s | namespace: %s, KID: %s, created_at: %s, encrypted_at: %s, status: %s", errMsg,
                getNamespace(), maskKid(getKid()), getCreatedAt(), getEncryptedAt(), currentStatus
        );
    }
}
