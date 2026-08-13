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
package com.nvidia.nvcf.rest.function.deployment.dto;

import static com.nvidia.nvcf.util.NvcfConstants.MAX_REQUEST_CONCURRENCY;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Positive;
import jakarta.validation.constraints.PositiveOrZero;
import java.util.Set;
import java.util.UUID;
import lombok.Builder;

@Builder
@Schema(description = "Data Transfer Object (DTO) representing GPU specification " +
        "for Deployment Update case.")
@Deprecated
public record UpdateGpuSpecificationDto(
        @Schema(description = "GPU specification id")
        @Nullable UUID gpuSpecificationId,

        @Schema(description = "GPU name from the cluster. Deprecated: this is read-only field, " +
                "should not be specified in update request.")
        @Nullable String gpu,

        @Schema(description = """
                Backend/CSP where the GPU powered instance has been launched. The original value
                would not be updated and it should be the same as original. The field is required
                only for validation.
                """)
        @Nullable String backend,

        @Schema(description = """
                Specific clusters within instance or worker node powered by the selected
                instance-type to deploy function. The original value would not be updated and it
                should be the same as original. The field is required only for validation.
                """)
        @Nullable Set<String> clusters,

        @Schema(description = "Maximum number of instances for the deployment")
        @NotNull @Positive Integer maxInstances,

        @Schema(description = "Minimum number of instances for the deployment")
        @NotNull @PositiveOrZero Integer minInstances,

        @Schema(description = "Instance type, based on GPU, assigned to a Worker. Deprecated: " +
                "this is read-only field, should not be specified in update request.")
        @Nullable String instanceType,

        @Schema(description = "Max request concurrency between 1 (default) and "
                + MAX_REQUEST_CONCURRENCY + ".")
        @Nullable @Min(1) @Max(MAX_REQUEST_CONCURRENCY) Integer maxRequestConcurrency) {

}
