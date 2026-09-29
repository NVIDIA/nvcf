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

import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto.ValidAutoscalingConfiguration;
import io.swagger.v3.oas.annotations.Hidden;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Positive;
import jakarta.validation.constraints.PositiveOrZero;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import lombok.Builder;
import tools.jackson.databind.node.ObjectNode;

@Builder(toBuilder = true)
@Schema(types = {"object"}, description = "Data Transfer Object(DTO) representing GPU specification.")
public record GpuSpecificationDto (
        @Schema(description = "GPU specification id")
        @Nullable UUID gpuSpecificationId,

        @Schema(description = "GPU name from the cluster")
        @NotBlank String gpu,

        @Schema(description = "Use clusters instead of this property. Legacy property typically" +
                " used when deploying a function to GFN.")
        @Nullable String backend,

        @Schema(description = "Maximum number of instances for the deployment")
        @NotNull @Positive Integer maxInstances,

        @Schema(description = "Minimum number of instances for the deployment")
        @NotNull @PositiveOrZero Integer minInstances,

        @Schema(description = "Instance type, based on GPU, assigned to a Worker")
        @NotBlank String instanceType,

        @Schema(description = "List of availability-zones(or clusters) in the cluster group")
        @Nullable List<String> availabilityZones,

        @Schema(description = "Max request concurrency between 1 (default) and "
                + MAX_REQUEST_CONCURRENCY + ".")
        @Nullable @Min(1) @Max(MAX_REQUEST_CONCURRENCY) Integer maxRequestConcurrency,

        @Schema(description = """
                Typically used when the function is based on Helm Charts to substitute
                 placeholders in values yaml.
                """,
                types = {"object"},
                implementation = Object.class,
                additionalProperties = Schema.AdditionalPropertiesValue.TRUE)
        @Nullable ObjectNode configuration,

        @Schema(description = """
                Specific clusters powered by the selected instance-type to deploy function.
                """)
        @Nullable Set<String> clusters,

        @Schema(description = """
                List of regions allowed to deploy. The instance or worker node will be in one of
                the specified geographical regions.
                """)
        @Nullable Set<String> regions,

        @Schema(description = """
                Specific cluster attributes/capabilities to deploy functions. For example,
                HIPPA Compliant, Confidential Compute Compliant, etc.
                """)
        @Nullable Set<String> attributes,

        @Schema(description = "Preferred order of deployment if there are several gpu specs.")
        @Hidden
        @Nullable @Min(1) @Max(99) Integer preferredOrder,

        @Schema(description = "CPU Architecture details")
        @Nullable String cpuArch,

        @Schema(description = "Operating System details")
        @Nullable String os,

        @Schema(description = "GPU driver version")
        @Nullable String driverVersion,

        @Schema(description = "Amount of available storage, e.g. 80G")
        @Nullable String storage,

        @Schema(description = "Amount of RAM")
        @Nullable String systemMemory,

        @Schema(description = "Amount of GPU memory")
        @Nullable String gpuMemory,

        @Schema(description = "Customizable autoscaling configuration")
        @Nullable
        @Valid
        @ValidAutoscalingConfiguration
        AutoscalingConfigurationDto autoscalingConfiguration,

        @Schema(description = "Helm validation policy")
        @Nullable @Valid HelmValidationPolicyDto helmValidationPolicy) {
}
