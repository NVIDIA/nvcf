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

import java.time.Instant;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor
@Table(TimeIndexedEncryptionKeyModel.TABLE_NAME)
public class TimeIndexedEncryptionKeyModel {
  public static final String TABLE_NAME = "encryption_keys_by_timestamp";
  public static final String COLUMN_NAMESPACE = "namespace";
  public static final String COLUMN_KID = "kid";
  public static final String COLUMN_CREATED_AT = "created_at";
  public static final String COLUMN_ENCRYPTED_AT = "encrypted_at";
  public static final String COLUMN_ENCRYPTED_BY_KID = "encrypted_by_kid";
  public static final String COLUMN_ENCRYPTED_KEY = "encrypted_key";

  @NonNull
  @PrimaryKeyColumn(name = COLUMN_NAMESPACE, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
  private String namespace;

  @NonNull
  @Column(COLUMN_KID)
  private String kid;

  @NonNull
  @PrimaryKeyColumn(name = COLUMN_CREATED_AT, ordinal = 1, type = PrimaryKeyType.CLUSTERED)
  private UUID createdAt;

  @NonNull
  @Column(COLUMN_ENCRYPTED_AT)
  private Instant encryptedAt;

  @NonNull
  @Column(COLUMN_ENCRYPTED_BY_KID)
  private String encryptedByKid;

  @NonNull
  @Column(COLUMN_ENCRYPTED_KEY)
  private String encryptedKey;
}
