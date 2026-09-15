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

import static com.nvidia.nvct.util.NvctConstants.MAX_SECRET_NAME_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.MAX_SECRET_VALUE_LENGTH;
import static java.lang.annotation.ElementType.FIELD;
import static java.lang.annotation.RetentionPolicy.RUNTIME;

import tools.jackson.databind.JsonNode;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Constraint;
import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;
import jakarta.validation.Payload;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.lang.annotation.Documented;
import java.lang.annotation.Retention;
import java.lang.annotation.Target;
import lombok.Builder;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@Builder
@Schema(description = "Data Transfer Object(DTO) representing secret name/value pair")
public record SecretDto(
        @Schema(description = "Secret name",
                requiredMode = Schema.RequiredMode.REQUIRED)
        @Pattern(regexp = SECRET_NAME_REGEX,
                message = "Invalid secret name: Must conform to regex " + SECRET_NAME_REGEX)
        @Size(min = 1, max = MAX_SECRET_NAME_LENGTH,
                message = "Invalid secret name: must be 1 - " + MAX_SECRET_NAME_LENGTH + " chars long")
        @NonNull @NotBlank String name,

        @Schema(description = "Secret value must be a string or JSON object and 1 - "
                + MAX_SECRET_VALUE_LENGTH + " chars long",
                types = {"string", "object"},
                implementation = Object.class,
                requiredMode = Schema.RequiredMode.REQUIRED)
        @NonNull @ValidSecretValueLength JsonNode value) {

        private static final String MESG_INVALID_SECRET_VALUE =
                "Invalid secret value specified";
        private static final String MESG_INVALID_SECRET_VALUE_LENGTH =
                "Secret value's length must be 1 - " + MAX_SECRET_VALUE_LENGTH + " chars long";

        private static final String SECRET_NAME_REGEX = "^[a-z0-9A-Z][a-z0-9A-Z\\_\\.\\-]*$";

        @Documented
        @Target(FIELD)
        @Retention(RUNTIME)
        @Constraint(validatedBy = SecretValueLengthValidator.class)
        @interface ValidSecretValueLength {
                String message() default MESG_INVALID_SECRET_VALUE;

                Class<?>[] groups() default {};

                Class<? extends Payload>[] payload() default {};
        }

        private static class SecretValueLengthValidator
                implements
                ConstraintValidator<ValidSecretValueLength, JsonNode> {

                @Override
                public boolean isValid(
                        JsonNode jsonNode,
                        ConstraintValidatorContext constraintValidatorContext) {
                        var value = jsonNode.isString() ? jsonNode.asString() : jsonNode.toString();
                        var length = value != null ? value.trim().length() : 0;
                        if (length == 0 || length > MAX_SECRET_VALUE_LENGTH) {
                                log.error(MESG_INVALID_SECRET_VALUE_LENGTH);
                                return false;
                        }

                        return true;
                }
        }

}
