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
package com.nvidia.nvcf.rest.function.management.dto;

import static com.fasterxml.jackson.annotation.JsonFormat.Shape.STRING;

import com.fasterxml.jackson.annotation.JsonFormat;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Positive;
import java.net.URI;
import java.time.Duration;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;

@Builder
@Data
@NoArgsConstructor
@AllArgsConstructor
@Schema(types = {"object"},
        description = "Data Transfer Object(DTO) representing a function's health configuration")
public class HealthDto {

    @NonNull
    @NotNull
    @Schema(description = "HTTP/gPRC protocol type for health endpoint")
    private ProtocolEnum protocol;

    @NonNull
    @NotNull
    @Schema(description = "Health endpoint for the container or the helmChart")
    private URI uri;

    @NonNull
    @NotNull
    @Schema(description = "Port number where the health listener is running")
    @Positive
    private Integer port;

    @NonNull
    @NotNull
    @Schema(description = "ISO 8601 duration string in PnDTnHnMn.nS format",
            type = "string",
            format = "duration",
            example = "PT10S")
    @JsonFormat(shape = STRING)
    private Duration timeout;

    @NonNull
    @NotNull
    @Schema(description = "Expected return status code considered as successful.")
    private Integer expectedStatusCode;
}
