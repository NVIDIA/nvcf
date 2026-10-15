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
package com.nvidia.icms.inbound.rest.model.nvca;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotBlank;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Request to update cluster JWKS for cert rotation")
public class UpdateJwksRequest {

    @NotBlank(message = "jwks is required")
    @Schema(description = "JWKS (JSON Web Key Set) JSON string")
    private String jwks;

    @Schema(description = "OIDC issuer URL. Optional: when omitted or blank, the existing "
            + "persisted issuer is kept. CLI rotate sends this so SIS reflects the issuer the "
            + "client just probed rather than silently retaining a stale value from initial "
            + "registration.")
    private String oidcIssuer;
}
