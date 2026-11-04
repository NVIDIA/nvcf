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

import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.UserDefinedType;

@Builder(toBuilder = true)
@Data
@AllArgsConstructor
@NoArgsConstructor
@UserDefinedType(EntityTypeUdt.USER_DEFINED_TYPE_NAME)
public class EntityTypeUdt {
    public static final String USER_DEFINED_TYPE_NAME = "entity_type";
    public static final String COLUMN_NAME = "name";
    public static final String COLUMN_DELETED_AT = "deleted_at";

    @NotNull
    @Column(COLUMN_NAME)
    private String name;


    @Column(COLUMN_DELETED_AT)
    @Nullable
    private Instant deletedAt;
}
