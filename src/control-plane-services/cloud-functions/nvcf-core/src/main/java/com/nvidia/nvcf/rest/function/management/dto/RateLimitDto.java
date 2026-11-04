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
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;
import java.util.Map;
import java.util.Set;
import lombok.Builder;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@Builder
@RateLimitDto.ValidRateLimitValues(rateLimitString = "rateLimit", perNcaIdRateString = "perNcaIdRate", exemptedNcaIdsString = "exemptedNcaIds", perUserRateString = "perUserRate")
@Schema(types = {"object"}, description = "Data Transfer Object(DTO) representing Rate limit config")
public record RateLimitDto(
        @Nullable
        @Schema(description = "Rate")
        @Pattern(regexp = RATE_REGEX,
                message = "Invalid rate: Must conform to regex " + RATE_REGEX)
        String rateLimit,

        @Nullable
        @Size(max = 32, message = "Maximum number of exempted nca id of 32 is exceeded")
        @Schema(description = "NCA ID Exemptions")
        Set<String> exemptedNcaIds,

        @Nullable
        @Size(max = 32, message = "Maximum number of per nca id config of 32 is exceeded")
        @Schema(description = "Per NCA ID Rate")
        Map<String,
                @Pattern(regexp = RATE_REGEX,
                        message = "Invalid rate limit: Must conform to regex " + RATE_REGEX) String> perNcaIdRate,

        @Nullable
        @Schema(description = "Sync check. Defaults to false")
        Boolean syncCheck,

        @Nullable
        @Pattern(regexp = RATE_REGEX,
                message = "Invalid per-user rate: Must conform to regex " + RATE_REGEX)
        @Schema(description = "Per-user rate, intended for traffic authenticated with an invocation "
                + "delegation token (InvocationToken). When set, this single rate is enforced "
                + "individually against every unique caller identity, in addition to the NCA-tier limit.")
        String perUserRate) {

    private static final String RATE_REGEX = "^(?!.*-([SMHD]).*-\\1)[1-9]\\d*-[SMHD](,\\s*[1-9]\\d*-[SMHD])*$";


    @Constraint(validatedBy = ValidRateLimitValuesValidator.class)
    @Target({ElementType.TYPE})
    @Retention(RetentionPolicy.RUNTIME)
    public @interface ValidRateLimitValues {
        String message() default "Either global rateLimit, perNcaIdRate, or perUserRate must be non-null, " +
                "per-ncaid rates ncaids cannot be in exemptedNcaIds, " +
                "and exemptedNcaIds can only exist when global rateLimit is provided";

        Class<?>[] groups() default {};

        Class<? extends Payload>[] payload() default {};

        String rateLimitString();

        String perNcaIdRateString();

        String exemptedNcaIdsString();

        String perUserRateString();
    }

    public static class ValidRateLimitValuesValidator implements ConstraintValidator<ValidRateLimitValues, Object> {

        private String rateLimitString;
        private String perNcaIdRateString;
        private String exemptedNcaIdsString;
        private String perUserRateString;

        @Override
        public void initialize(ValidRateLimitValues constraintAnnotation) {
            this.rateLimitString = constraintAnnotation.rateLimitString();
            this.perNcaIdRateString = constraintAnnotation.perNcaIdRateString();
            this.exemptedNcaIdsString = constraintAnnotation.exemptedNcaIdsString();
            this.perUserRateString = constraintAnnotation.perUserRateString();
        }

        @Override
        @SneakyThrows
        @SuppressWarnings("unchecked")
        public boolean isValid(Object value, ConstraintValidatorContext context) {
            var rateLimit = value.getClass().getDeclaredField(rateLimitString).get(value);
            var perNcaIdRate = value.getClass().getDeclaredField(perNcaIdRateString).get(value);
            var exemptedNcaIds = value.getClass().getDeclaredField(exemptedNcaIdsString).get(value);
            var perUserRate = value.getClass().getDeclaredField(perUserRateString).get(value);

            // Check if at least one rate limit is provided
            if (rateLimit == null && perNcaIdRate == null && perUserRate == null) {
                return false;
            }

            // Check if exemptedNcaIds is provided without rateLimit
            if (exemptedNcaIds != null && rateLimit == null) {
                var exemptedNcaIdsSet = (Set<String>) exemptedNcaIds;
                if (!exemptedNcaIdsSet.isEmpty()) {
                    return false;
                }
            }

            // Check if any per-ncaid rate ncaid is also in exemptedNcaIds
            if (perNcaIdRate != null && exemptedNcaIds != null) {
                var perNcaIdRateMap = (Map<String, String>) perNcaIdRate;
                var exemptedNcaIdsSet = (Set<String>) exemptedNcaIds;
                
                for (String ncaId : perNcaIdRateMap.keySet()) {
                    if (exemptedNcaIdsSet.contains(ncaId)) {
                        return false;
                    }
                }
            }

            return true;
        }
    }
}
