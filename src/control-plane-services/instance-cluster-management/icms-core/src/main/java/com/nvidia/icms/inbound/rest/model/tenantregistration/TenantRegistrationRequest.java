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
package com.nvidia.icms.inbound.rest.model.tenantregistration;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@Builder
@NoArgsConstructor
@AllArgsConstructor
@Schema(description = "Request to register a tenant application (e.g. GDN) with SIS")
public class TenantRegistrationRequest {

    @NotNull(message = "tenantRegistrationData must not be null")
    @NotEmpty(message = "tenantRegistrationData must not be empty")
    @Schema(description = "Tenant-specific data to store and propagate (e.g. gdnAppId for GDN)", requiredMode = Schema.RequiredMode.REQUIRED)
    private Map<String, String> tenantRegistrationData;

    @NotNull(message = "functionVersionId must not be null")
    @Schema(description = "Associated NVCF function version ID", requiredMode = Schema.RequiredMode.REQUIRED)
    private UUID functionVersionId;

    @NotNull(message = "functionId must not be null")
    @Schema(description = "Associated NVCF function ID", requiredMode = Schema.RequiredMode.REQUIRED)
    private UUID functionId;
}
