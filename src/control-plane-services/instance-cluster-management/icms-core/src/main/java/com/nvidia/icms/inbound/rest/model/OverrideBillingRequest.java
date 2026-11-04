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
package com.nvidia.icms.inbound.rest.model;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotNull;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Billing NCA ID override request")
@Builder
public class OverrideBillingRequest {

    @NotNull(message = "Function ID must not be null")
    @Schema(description = "Function ID of the NVCF function to override billing NCA ID")
    private UUID functionId;

    @NotNull(message = "Function version ID must not be null")
    @Schema(description = "Function Version ID of the NVCF function to override billing NCA ID")
    private UUID functionVersionId;

    @NotNull(message = "Owner NCA ID of the function cannot be null")
    @Schema(description = "Owner NCA ID of the NVCF function")
    private String ownerNcaId;

    @NotNull(message = "Billing NCA ID of the function cannot be null")
    @Schema(description = "Billing NCA ID of the NVCF function")
    private String billingNcaId;
}
