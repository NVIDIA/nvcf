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

package com.nvidia.apikeys.vo;

import com.nvidia.apikeys.persistance.models.KeyByAccountAndOwnerAndServiceModel;
import com.nvidia.boot.jwt.services.mapping.annotation.ValueObject;
import java.time.Instant;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor
@ValueObject(model = KeyByAccountAndOwnerAndServiceModel.class)
public class KeyByAccountAndOwnerAndServiceVo {

    private String ncaId;
    private KeyOwnerType ownerType;
    private String ownerId;

    private String issuerServiceId;
    private String keyId;
    private Instant createdAt;
    private Instant expiresAt;
    private Instant deletesAt;
    private KeyStatus keyStatus;

    private String keyHash;
    private String apiKeySuffix;
    private String description;
    private Set<String> audienceServiceIds;

    public static KeyByAccountAndOwnerAndServiceVo from(KeyVo key) {
        // authorizations stay only on the keys table
        return KeyByAccountAndOwnerAndServiceVo.builder()
                .ncaId(key.getNcaId())
                .ownerType(key.getOwnerType())
                .ownerId(key.getOwnerId())
                .issuerServiceId(key.getIssuerServiceId())
                .keyId(key.getKeyId())
                .createdAt(key.getCreatedAt())
                .expiresAt(key.getExpiresAt())
                .deletesAt(key.getDeletesAt())
                .keyStatus(key.getKeyStatus())
                .keyHash(key.getKeyHash())
                .apiKeySuffix(key.getApiKeySuffix())
                .description(key.getDescription())
                .audienceServiceIds(key.getAudienceServiceIds())
                .build();
    }
}
