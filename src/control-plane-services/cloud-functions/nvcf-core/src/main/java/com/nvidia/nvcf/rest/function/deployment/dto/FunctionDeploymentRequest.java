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

import static java.lang.annotation.ElementType.FIELD;
import static java.lang.annotation.RetentionPolicy.RUNTIME;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Constraint;
import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;
import jakarta.validation.Payload;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.lang.annotation.Documented;
import java.lang.annotation.Retention;
import java.lang.annotation.Target;
import java.util.List;
import java.util.Objects;
import lombok.Builder;
import lombok.extern.slf4j.Slf4j;
import org.springframework.util.CollectionUtils;

@Slf4j
@Builder
@Schema(description = "Request to deploy a function")
public record FunctionDeploymentRequest(
        @Schema(description = "GPU specs with GPU, instance-type, clusters etc. details")
        @NotEmpty @NotNull @ValidInstanceCount
        List<@Valid GpuSpecificationDto> deploymentSpecifications) {

    static final String MESG_MIN_GREATER_THAN_MAX =
            "Invalid request: 'minInstances' must be lesser than or equal to 'maxInstances'";
    static final String MESG_INVALID_GPU_BACKEND_COMBINATION =
            "Invalid request: GPU '%s' is not available in Backend '%s'";
    static final String MESG_MISSING_SPEC_FIELD =
            "Invalid request: Missing maxInstance or minInstance field in the gpu specification";
    static final String MESG_EMPTY_DEPLOYMENT_SPECS =
            "Invalid request: Empty or null gpu specs in the request payload";

    @Documented
    @Target(FIELD)
    @Retention(RUNTIME)
    @Constraint(validatedBy = InstanceCountValidator.class)
    @interface ValidInstanceCount {
        String message() default MESG_MIN_GREATER_THAN_MAX;

        Class<?>[] groups() default {};

        Class<? extends Payload>[] payload() default {};
    }

    // Validate minInstances is not greater than maxInstances.
    @Slf4j
    private static class InstanceCountValidator
            implements ConstraintValidator<ValidInstanceCount, List<GpuSpecificationDto>> {

        @Override
        public boolean isValid(
                List<GpuSpecificationDto> gpuSpecifications,
                ConstraintValidatorContext constraintValidatorContext) {
            if (CollectionUtils.isEmpty(gpuSpecifications)) {
                log.info(MESG_EMPTY_DEPLOYMENT_SPECS);
                return false;
            }

            var nullFieldsExist = gpuSpecifications.stream()
                    .anyMatch(InstanceCountValidator::nullFields);
            if (nullFieldsExist) {
                return false;
            }
            return gpuSpecifications.stream()
                    .filter(spec -> spec.minInstances() > spec.maxInstances())
                    .findFirst()
                    .isEmpty();
        }

        private static boolean nullFields(GpuSpecificationDto spec) {
            if (Objects.isNull(spec.maxInstances()) || Objects.isNull(spec.minInstances())) {
                log.info(MESG_MISSING_SPEC_FIELD);
                return true;
            }
            return false;
        }
    }
}
