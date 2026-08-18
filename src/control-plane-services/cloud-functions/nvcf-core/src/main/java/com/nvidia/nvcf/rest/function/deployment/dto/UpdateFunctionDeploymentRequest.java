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

import static com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest.MESG_MIN_GREATER_THAN_MAX;
import static com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest.MESG_MISSING_SPEC_FIELD;
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
@Schema(description = "Request to update function deployment spec")
public record UpdateFunctionDeploymentRequest(
    @Schema(description = "Deployment specs with GPU, instance-type, etc. " +
            "details for update request")
    @NotEmpty @NotNull @ValidUpdateDeploymentInstanceCount
    List<@Valid UpdateGpuSpecificationDto> deploymentSpecifications) {

    @Documented
    @Target(FIELD)
    @Retention(RUNTIME)
    @Constraint(validatedBy = UpdateDeploymentInstanceCountValidator.class)
    @interface ValidUpdateDeploymentInstanceCount {
        String message() default MESG_MIN_GREATER_THAN_MAX;
        Class<?>[] groups() default {};
        Class<? extends Payload>[] payload() default {};
    }

    // Validate minInstances is not greater than maxInstances.
    @Slf4j
    private static class UpdateDeploymentInstanceCountValidator
            implements
            ConstraintValidator<ValidUpdateDeploymentInstanceCount, List<UpdateGpuSpecificationDto>> {
        static final String MESG_EMPTY_DEPLOYMENT_SPECS =
                "Invalid request: Empty or null deployment specs in the request payload";

        @Override
        public boolean isValid(
                List<UpdateGpuSpecificationDto> deploymentSpecifications,
                ConstraintValidatorContext constraintValidatorContext) {
            if (CollectionUtils.isEmpty(deploymentSpecifications)) {
                log.info(MESG_EMPTY_DEPLOYMENT_SPECS);
                return false;
            }

            var nullFieldsExist = deploymentSpecifications.stream()
                    .anyMatch(UpdateDeploymentInstanceCountValidator::nullFields);
            if (nullFieldsExist) {
                return false;
            }
            return deploymentSpecifications.stream()
                    .filter(spec -> spec.minInstances() > spec.maxInstances())
                    .findFirst()
                    .isEmpty();
        }

        private static boolean nullFields(UpdateGpuSpecificationDto spec) {
            if (Objects.isNull(spec.maxInstances()) || Objects.isNull(spec.minInstances())) {
                log.info(MESG_MISSING_SPEC_FIELD);
                return true;
            }
            return false;
        }
    }

}
