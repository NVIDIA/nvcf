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

import static com.nvidia.nvct.util.NvctConstants.MAX_DESCRIPTION_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAGS_COUNT;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAG_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.NAME_REGEX;
import static java.lang.annotation.ElementType.FIELD;
import static java.lang.annotation.RetentionPolicy.RUNTIME;

import com.nvidia.nvct.service.telemetry.dto.TelemetriesDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Constraint;
import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;
import jakarta.validation.Payload;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.lang.annotation.Documented;
import java.lang.annotation.Retention;
import java.lang.annotation.Target;
import java.net.URI;
import java.time.Duration;
import java.util.List;
import java.util.Set;
import lombok.Builder;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.hibernate.validator.constraints.Length;

@Slf4j
@Builder(toBuilder = true)
@Schema(description = "Request payload to create a Task")
public record CreateTaskRequest(
        @Schema(description = "Task name must start with lowercase/uppercase/digit and can " +
                "only contain lowercase, uppercase, digit, hyphen, and underscore characters.")
        @Pattern(regexp = NAME_REGEX,
                message = "Invalid task name: Must conform to regex " + NAME_REGEX)
        @Size(min = 1, max = 128, message = "Invalid task name: must be 1 - 128 characters long")
        @NonNull @NotBlank String name,

        @Schema(description = "GPU, instance-type, and backend details for launching a Task.")
        @NotNull @Valid GpuSpecificationDto gpuSpecification,

        @Schema(description = "Task container image")
        @Nullable URI containerImage,

        @Schema(description = "Args to be passed when launching the container.")
        @Nullable String containerArgs,

        @Schema(description = "Environment settings for launching the container.")
        @Nullable List<@Valid ContainerEnvironmentEntryDto> containerEnvironment,

        @Schema(description = "Optional set of models")
        @Nullable Set<@Valid ArtifactDto> models,

        @Schema(description = "Optional set of resources")
        @Nullable Set<@Valid ArtifactDto> resources,

        @Schema(description = "Optional set of tags")
        @Nullable @Valid
        @Size(max = MAX_TAGS_COUNT, message = "Maximum number of tags of " + MAX_TAGS_COUNT +
                " is exceeded.")
        Set<@Length(max = MAX_TAG_LENGTH, message = "Maximum tag length of " + MAX_TAG_LENGTH +
                " is exceeded.")
        @Pattern(regexp = "[a-zA-Z0-9\\-_:=]+") String> tags,

        @Schema(description = "Optional Task description")
        @Nullable
        @Length(max = MAX_DESCRIPTION_LENGTH, message = "Maximum description length of " +
                MAX_DESCRIPTION_LENGTH + " is exceeded.")
        String description,

        @Schema(description = "Optional max duration for which the Task should run. " +
                "Must be specified when launching Task on 'GFN'. Must be less than PT8H when " +
                "launching Task on 'GFN'.",
                type = "string",
                format = "duration",
                example = "PT12H30M")
        @Nullable Duration maxRuntimeDuration,

        @Schema(description = "Optional max duration for which the Task should be queued.",
                defaultValue = "PT72H",
                type = "string",
                format = "duration",
                example = "PT4H30M45S")
        @Nullable Duration maxQueuedDuration,

        @Schema(description = "Optional grace period after which the Task should be terminated.",
                defaultValue = "PT1H",
                type = "string",
                format = "duration",
                example = "PT1H30M20S")
        @Nullable Duration terminationGracePeriodDuration,

        @Schema(description = "Optional Task result handling strategy",
                defaultValue = "UPLOAD")
        @Nullable ResultHandlingStrategyEnum resultHandlingStrategy,

        @Schema(description = "Optional result path in NGC Model Registry for the generated " +
                "results. Must be specified when resultHandlingStrategy is UPLOAD and the " +
                "format should be -- <org-name>/<optional-team-name>/<model-name>.")
        @Nullable String resultsLocation,

        @Schema(description = "Optional Helm Chart")
        @Nullable URI helmChart,

        @Schema(description = "Optional telemetry configuration for logs, metrics, and traces.")
        @Nullable @ValidTelemetries TelemetriesDto telemetries,

        @Schema(description = "Optional set of secrets. If resultHandlingStrategy is UPLOAD, " +
                "then user must specify NGC_API_KEY secret with write-privileges for NVCT to " +
                "upload results/checkpoints to the NGC Private Registry at a path specified " +
                "using resultPath property.")
        @Nullable Set<@Valid SecretDto> secrets) {

    @Documented
    @Target(FIELD)
    @Retention(RUNTIME)
    @Constraint(validatedBy = TelemetriesValidator.class)
    @interface ValidTelemetries {
        String message() default "Invalid request: Issues with the telemetries";

        Class<?>[] groups() default {};

        Class<? extends Payload>[] payload() default {};
    }

    private static class TelemetriesValidator
            implements ConstraintValidator<ValidTelemetries, TelemetriesDto> {
        private static final String MESG_INVALID_TELEMETRY_REQUEST =
                "Invalid request: telemetries object must have at least one UUID specified.";

        @Override
        public boolean isValid(
                TelemetriesDto telemetries,
                ConstraintValidatorContext constraintValidatorContext) {
            if (telemetries == null) {
                return true;
            }

            if (telemetries.logsTelemetryId() == null &&
                    telemetries.metricsTelemetryId() == null &&
                    telemetries.tracesTelemetryId() == null) {
                log.error(MESG_INVALID_TELEMETRY_REQUEST);
                return false;
            }
            return true;
        }
    }

}
