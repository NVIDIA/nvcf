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

import static com.fasterxml.jackson.annotation.JsonFormat.Shape.STRING;
import static java.lang.annotation.ElementType.FIELD;
import static java.lang.annotation.RetentionPolicy.RUNTIME;

import com.fasterxml.jackson.annotation.JsonFormat;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Constraint;
import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;
import jakarta.validation.Payload;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Positive;
import jakarta.validation.constraints.PositiveOrZero;
import java.lang.annotation.Documented;
import java.lang.annotation.Retention;
import java.lang.annotation.Target;
import java.time.Duration;
import java.util.Objects;
import lombok.Builder;
import lombok.extern.slf4j.Slf4j;

@Builder
@Schema(types = {"object"},
        description = "Data Transfer Object(DTO) representing autoscaling configuration")
public record AutoscalingConfigurationDto(

        @Schema(description = "Configuration for scaling up")
        @Nullable @Valid ScalingDetails scaleUpDetails,

        @Schema(description = "Configuration for scaling down")
        @Nullable @Valid ScalingDetails scaleDownDetails) {

    @Builder
    @Schema(types = {"object"})
    public record ScalingDetails(
            @Schema(description = "Scaling metric")
            @Nullable String metric,

            @Schema(description = "Scaling factor must be greater-than 1.0 for scale up and " +
                    "less-than 1.0 for scale down. This factor is used to multiply the current" +
                    "instance count when specified threshold is met.")
            @NotNull @Positive Float factor,

            @Schema(description = "Scaling threshold (0-100) as a percentage of utilization" +
                    "upon which the number of current instances are multiplied with the" +
                    "specified factor.")
            @NotNull @PositiveOrZero Integer threshold,

            @Schema(description = "Stickiness window configuration")
            @Nullable @Valid StickinessWindow stickiness
    ) {
    }

    @Builder
    @Schema(types = {"object"})
    public record StickinessWindow(
            @Schema(description = "Window size must be less than or equal to PT1H and will be" +
                    "rounded to the nearest minute",
                    type = "string",
                    format = "duration",
                    example = "PT30M")
            @JsonFormat(shape = STRING)
            @NotNull Duration size,

            @Schema(description = "Window threshold must be less than size",
                    type = "string",
                    format = "duration",
                    example = "PT5M")
            @JsonFormat(shape = STRING)
            @NotNull Duration threshold
    ) {
    }

    /**
     * Reusable constraint annotation for validating AutoscalingConfigurationDto fields.
     */
    @Documented
    @Target(FIELD)
    @Retention(RUNTIME)
    @Constraint(validatedBy = AutoscalingConfigurationValidator.class)
    public @interface ValidAutoscalingConfiguration {
        String message() default "Invalid request: Autoscaling configuration is invalid";

        Class<?>[] groups() default {};

        Class<? extends Payload>[] payload() default {};
    }

    @Slf4j
    static class AutoscalingConfigurationValidator
            implements ConstraintValidator<ValidAutoscalingConfiguration,
                                            AutoscalingConfigurationDto> {
        private static final String MESG_INVALID_SCALE_UP_DETAILS =
                "Invalid request: Missing or invalid factor/threshold in scaleUpDetails. " +
                        "Factor must be greater than 1.0.";
        private static final String MESG_INVALID_SCALE_DOWN_DETAILS =
                "Invalid request: Missing or invalid factor/threshold in scaleDownDetails. " +
                        "Factor must be less than 1.0.";
        private static final String MESG_INVALID_STICKINESS_WINDOW =
                "Invalid request: Invalid stickiness window - size and threshold required, " +
                        "size must be less than or equal to one hour and threshold must " +
                        "be less than size.";

        @Override
        public boolean isValid(AutoscalingConfigurationDto value,
                               ConstraintValidatorContext context) {
            if (value == null) {
                return true;
            }

            if (Objects.nonNull(value.scaleUpDetails())) {
                var details = value.scaleUpDetails();
                if (Objects.isNull(details.factor()) || Objects.isNull(details.threshold())) {
                    log.info(MESG_INVALID_SCALE_UP_DETAILS);
                    return false;
                }
                // scaleUpDetails factor must be greater than 1.0.
                if (details.factor() <= 1.0f) {
                    log.info(MESG_INVALID_SCALE_UP_DETAILS);
                    return false;
                }
                if (!isValidStickinessWindow(details.stickiness())) {
                    return false;
                }
            }

            if (Objects.nonNull(value.scaleDownDetails())) {
                var details = value.scaleDownDetails();
                if (Objects.isNull(details.factor()) || Objects.isNull(details.threshold())) {
                    log.info(MESG_INVALID_SCALE_DOWN_DETAILS);
                    return false;
                }
                // scaleDownDetails factor must be less than 1.0.
                if (details.factor() >= 1.0f) {
                    log.info(MESG_INVALID_SCALE_DOWN_DETAILS);
                    return false;
                }
                if (!isValidStickinessWindow(details.stickiness())) {
                    return false;
                }
            }

            return true;
        }

        private static final Duration MAX_STICKINESS_SIZE = Duration.ofHours(1);

        private boolean isValidStickinessWindow(StickinessWindow stickiness) {
            if (Objects.isNull(stickiness)) {
                return true;
            }
            if (Objects.isNull(stickiness.size()) || Objects.isNull(stickiness.threshold())) {
                log.info(MESG_INVALID_STICKINESS_WINDOW);
                return false;
            }
            // Validate that size is less than or equal to PT1H (1 hour).
            if (stickiness.size().compareTo(MAX_STICKINESS_SIZE) > 0) {
                log.info(MESG_INVALID_STICKINESS_WINDOW);
                return false;
            }
            // Validate that threshold is less than size.
            if (stickiness.threshold().compareTo(stickiness.size()) >= 0) {
                log.info(MESG_INVALID_STICKINESS_WINDOW);
                return false;
            }
            return true;
        }
    }
}
