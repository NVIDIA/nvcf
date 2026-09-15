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
package com.nvidia.nvcf.rest.function.invocation.dto;

import com.nvidia.nvcf.rest.function.management.dto.BasicFunctionDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Size;
import java.util.List;
import lombok.Builder;

@Builder
@Schema(description = "Request body for issuing a function invocation token")
public record MultiFunctionsInvocationTokenRequest(

        /*
        Function version id is optional
        When a version id is omitted/null for a function id, that means all versions can be invoked
        Otherwise, only specific function versions of that function is allowed
         */
        @Schema(description = "Functions to invoke")
        @Size(max = 4, message = "Maximum number of functions to invoke 4 is exceeded")
        @NotNull
        List<BasicFunctionDto> functions,

        @Schema(description = "ClientId for client to pass in to track who will eventually use the token")
        @NotBlank String clientId
) {

}
