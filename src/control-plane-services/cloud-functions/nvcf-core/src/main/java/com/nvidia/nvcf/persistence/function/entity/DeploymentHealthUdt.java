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
package com.nvidia.nvcf.persistence.function.entity;

import jakarta.annotation.Nonnull;
import jakarta.validation.constraints.NotBlank;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.UserDefinedType;

@Data
@Builder
@AllArgsConstructor
@NoArgsConstructor
@UserDefinedType(DeploymentHealthUdt.USER_DEFINED_TYPE_NAME)
public class DeploymentHealthUdt {
    public static final String USER_DEFINED_TYPE_NAME = "deployment_health_udt";

    @Nonnull
    @Column("sis_request_id")
    private UUID icmsRequestId;

    @NotBlank
    @Column("gpu")
    private String gpu;

    @NotBlank
    @Column("backend")
    private String backend;

    @NotBlank
    @Column("instance_type")
    private String instanceType;

    @NotBlank
    @Column("error")
    private String error;

}
