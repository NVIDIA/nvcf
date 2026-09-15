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
package com.nvidia.nvcf.persistence.client.entity;

import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.Id;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor
@Table(ClientEntity.TABLE_NAME)
public class ClientEntity {

    public static final String TABLE_NAME = "clients";

    public static final String COLUMN_CLIENT_ID = "client_id";  // Customer's OAuth2 Client Id
    public static final String COLUMN_NCA_ID = "nca_id";        // Customer's NV CLoud Account Id
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_CREATED_AT = "created_at";

    @NonNull
    @Id
    @PrimaryKey(COLUMN_CLIENT_ID)
    private String clientId;

    @NonNull
    @Column(COLUMN_NCA_ID)
    private String ncaId;

    @NonNull
    @Column(COLUMN_NAME)
    private String name;

    @Column(COLUMN_CREATED_AT)
    @Builder.Default
    private Instant createdAt = Instant.now();

}
