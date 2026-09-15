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
package com.nvidia.ess.encryption.persistence.models.naming;

import com.nvidia.ess.encryption.persistence.models.EncryptionKeyByTimestampModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import lombok.AllArgsConstructor;
import org.springframework.data.cassandra.core.mapping.CassandraPersistentEntity;
import org.springframework.data.cassandra.core.mapping.NamingStrategy;

@AllArgsConstructor
public class EncryptionKeyNamingStrategy implements NamingStrategy {
    private final String encryptionKeyTableName;
    private final String encryptionKeyByTimestampTableName;

    @Override
    public String getTableName(CassandraPersistentEntity<?> entity) {
        if (entity.getType().equals(EncryptionKeyModel.class)) {
            return encryptionKeyTableName;
        } else if (entity.getType().equals(EncryptionKeyByTimestampModel.class)) {
            return encryptionKeyByTimestampTableName;
        }
        return NamingStrategy.SNAKE_CASE.getTableName(entity);
    }
}
