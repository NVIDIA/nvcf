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

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Constraint;
import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;
import jakarta.validation.Payload;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.Size;
import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;
import java.util.Map;
import org.springframework.util.CollectionUtils;
import tools.jackson.core.JsonParser;
import tools.jackson.core.JsonToken;
import tools.jackson.databind.DeserializationContext;
import tools.jackson.databind.annotation.JsonDeserialize;
import tools.jackson.databind.deser.std.StdScalarDeserializer;
import tools.jackson.databind.type.LogicalType;

@PriorityDto.ValidPriority
@Schema(types = {"object"},
        description = "Function-level request priority. Lower value is higher priority, 0 is highest.")
public record PriorityDto(
        @Nullable
        @Min(value = 0, message = "defaultPriority must be >= 0")
        @Max(value = MAX_PRIORITY, message = "defaultPriority must be <= " + MAX_PRIORITY)
        @Schema(description = "Default priority.")
        @JsonDeserialize(using = PriorityValueDeserializer.class)
        Long defaultPriority,

        @Nullable
        @Size(max = MAX_PER_ACCOUNT_ENTRIES,
                message = "Maximum number of perAccountPriority entries of " + MAX_PER_ACCOUNT_ENTRIES
                        + " is exceeded.")
        @Schema(description = "Per-account priority overrides, keyed by account ID.")
        @JsonDeserialize(contentUsing = PriorityValueDeserializer.class)
        Map<String,
                @Min(value = 0, message = "priority must be >= 0")
                @Max(value = MAX_PRIORITY, message = "priority must be <= " + MAX_PRIORITY)
                Long> perAccountPriority) {

    // u32 max. Package-private so the test can reuse these bounds.
    static final long MAX_PRIORITY = 4294967295L;
    static final int MAX_PER_ACCOUNT_ENTRIES = 64;
    private static final String MESG_DEFAULT_REQUIRED_WITH_OVERRIDES =
            "Invalid priority: 'defaultPriority' is required when 'perAccountPriority' has entries";

    @Constraint(validatedBy = ValidPriorityValidator.class)
    @Target(ElementType.TYPE)
    @Retention(RetentionPolicy.RUNTIME)
    public @interface ValidPriority {
        String message() default "Invalid priority configuration";

        Class<?>[] groups() default {};

        Class<? extends Payload>[] payload() default {};
    }

    public static class ValidPriorityValidator
            implements ConstraintValidator<ValidPriority, PriorityDto> {

        @Override
        public boolean isValid(PriorityDto value, ConstraintValidatorContext context) {
            if (value == null) {
                return true;
            }
            // Default priority is required if priority configuration is specified.
            if (!CollectionUtils.isEmpty(value.perAccountPriority())
                    && value.defaultPriority() == null) {
                context.disableDefaultConstraintViolation();
                context.buildConstraintViolationWithTemplate(MESG_DEFAULT_REQUIRED_WITH_OVERRIDES)
                        .addConstraintViolation();
                return false;
            }
            return true;
        }
    }

    /** Deserializes priority values while rejecting floating-point JSON numbers. */
    public static class PriorityValueDeserializer extends StdScalarDeserializer<Long> {
        public PriorityValueDeserializer() {
            super(Long.class);
        }

        @Override
        public LogicalType logicalType() {
            return LogicalType.Integer;
        }

        @Override
        public Long deserialize(JsonParser parser, DeserializationContext context) {
            if (parser.hasToken(JsonToken.VALUE_NUMBER_FLOAT)) {
                return context.reportInputMismatch(Long.class, "priority must be an integer");
            }
            return _parseLong(parser, context, Long.class);
        }
    }
}
