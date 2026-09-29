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

import static com.nvidia.nvcf.util.NvcfConstants.MAX_TAGS_COUNT;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_TAG_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.TAG_REGEX;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Constraint;
import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;
import jakarta.validation.Payload;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import lombok.Builder;
import org.hibernate.validator.constraints.Length;

@Builder
@UpdateFunctionRequest.ValidUpdateFunctionRequest
@Schema(description = "Request payload to update mutable function configuration fields.")
public record UpdateFunctionRequest(
        @Nullable
        @Schema(description = "Optional set of function tags. When omitted, the existing value is preserved.")
        @Valid
        @Size(max = MAX_TAGS_COUNT,
                message = "Maximum number of tags of " + MAX_TAGS_COUNT + " is exceeded.")
        Set<@Length(max = MAX_TAG_LENGTH,
                    message = "Maximum tag length of " + MAX_TAG_LENGTH + " is exceeded.")
            @Pattern(regexp = TAG_REGEX) String> tags,

        @Nullable
        @Valid
        @Schema(description = "Optional full replacement for the function rate limit configuration. " +
                "When omitted, the existing value is preserved. Use the delete rate-limit endpoint " +
                "to clear it.")
        RateLimitDto rateLimit,

        @Nullable
        @Valid
        @Schema(description = "Optional list of model-specific updates.")
        List<ModelUpdateDto> modelUpdates,

        @Nullable
        @Valid
        @Schema(description = "Optional function-level LLM invocation configuration. When omitted, "
                + "the existing value is preserved.")
        LlmInvocationConfigDto llmInvocationConfig) {

    private static final String MESG_MODEL_CONFIG_UPDATE_REQUIRED =
            "Invalid request: at least one of 'tokenRateLimit' or 'routingMethod' must be specified";
    private static final String MESG_DUPLICATE_MODEL_UPDATES =
            "Invalid request: duplicate model names are not allowed in 'modelUpdates'";
    private static final String MESG_FUNCTION_UPDATE_REQUIRED =
            "Invalid request: at least one of 'tags', 'rateLimit', 'modelUpdates', or "
                    + "'llmInvocationConfig' must be specified";

    @Builder
    @Schema(types = {"object"}, description = "Model-specific update request.")
    public record ModelUpdateDto(
            @Schema(description = "Name of the model to update.")
            @NotBlank
            String modelName,

            @Nullable
            @Valid
            @Schema(description = "Optional updates to this model's LLM config fields.")
            LlmConfigUpdateDto llmConfig) {
    }

    @Builder
    @Schema(types = {"object"},
            description = "Partial update payload for LLM-specific model configuration fields.")
    public record LlmConfigUpdateDto(
            @Nullable
            @Schema(description = "Updated token-level rate limit for the model. " +
                    "When omitted, the existing value is preserved.")
            String tokenRateLimit,

            @Nullable
            @Schema(description = "Updated routing method for the model. " +
                    "When omitted, the existing value is preserved.")
            String routingMethod) {
    }

    @Constraint(validatedBy = ValidUpdateFunctionRequestValidator.class)
    @Target(ElementType.TYPE)
    @Retention(RetentionPolicy.RUNTIME)
    public @interface ValidUpdateFunctionRequest {
        String message() default "Invalid update function request";
        Class<?>[] groups() default {};
        Class<? extends Payload>[] payload() default {};
    }

    public static class ValidUpdateFunctionRequestValidator
            implements ConstraintValidator<ValidUpdateFunctionRequest, UpdateFunctionRequest> {

        @Override
        public boolean isValid(
                UpdateFunctionRequest value,
                ConstraintValidatorContext context) {
            if (value == null) {
                return true;
            }

            if (value.tags() == null && value.rateLimit() == null
                    && (value.modelUpdates() == null || value.modelUpdates().isEmpty())
                    && value.llmInvocationConfig() == null) {
                return violation(context, MESG_FUNCTION_UPDATE_REQUIRED);
            }

            var modelUpdates = value.modelUpdates();
            if (modelUpdates == null || modelUpdates.isEmpty()) {
                return true;
            }

            var uniqueNames = new HashSet<String>();
            for (var modelUpdate : modelUpdates) {
                if (!uniqueNames.add(modelUpdate.modelName())) {
                    return violation(context, MESG_DUPLICATE_MODEL_UPDATES);
                }
            }

            for (var modelUpdate : modelUpdates) {
                var llmConfig = modelUpdate.llmConfig();
                if (llmConfig == null
                        || (llmConfig.tokenRateLimit() == null && llmConfig.routingMethod() == null)) {
                    return violation(context, MESG_MODEL_CONFIG_UPDATE_REQUIRED);
                }
            }

            return true;
        }

        private boolean violation(ConstraintValidatorContext context, String message) {
            context.disableDefaultConstraintViolation();
            context.buildConstraintViolationWithTemplate(message)
                    .addConstraintViolation();
            return false;
        }
    }
}
