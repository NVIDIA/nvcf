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

package com.nvidia.boot.jwt.services.mapping.model;

import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverterTest;
import com.nvidia.boot.jwt.services.mapping.annotation.EncryptedFields;
import com.nvidia.boot.jwt.services.mapping.vo.KeyByOwnerAndServiceVo;
import com.nvidia.boot.jwt.services.mapping.vo.KeyOwnerStatus;
import com.nvidia.boot.jwt.services.mapping.vo.KeyOwnerType;
import com.nvidia.boot.jwt.services.mapping.vo.KeyStatus;
import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor
public class KeyByOwnerAndServiceModel {

    @NonNull
    private KeyOwnerType ownerType;

    @NonNull
    private String ownerID;

    @NonNull
    private String issuerServiceID;

    @NonNull
    private String keyId;

    private KeyOwnerStatus ownerStatus;

    private Instant updatedAt;

    private Instant expiresAt;

    private Instant deletesAt;

    private KeyStatus keyStatus;

    @EncryptedFields(encryptionKeyName = EncryptedModelConverterTest.TEST_KEY, valueObject = KeyByOwnerAndServiceVo.class)
    private String keyDetails;
}
