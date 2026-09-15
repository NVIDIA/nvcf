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
package com.nvidia.nvct.rest.task.dto;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Pattern;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;

@Builder
@Data
@NoArgsConstructor
@AllArgsConstructor
@Schema(description = "Data Transfer Object(DTO) representing a container environment entry")
public class ContainerEnvironmentEntryDto {
    private static final String KEY_REGEX = "^[a-z0-9A-Z][a-z0-9A-Z\\_]*$";


    @Schema(description = "Container environment key")
    @Pattern(regexp = KEY_REGEX,
            message = "Invalid environment key: Must conform to regex " + KEY_REGEX)
    @NotBlank
    @NonNull
    private String key;

    @Schema(description = "Container environment value")
    @NotBlank
    @NonNull
    private String value;

}
