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

import static org.assertj.core.api.Assertions.assertThat;

import jakarta.validation.Validation;
import jakarta.validation.Validator;
import jakarta.validation.ValidatorFactory;
import java.util.HashMap;
import java.util.Map;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class LlmInvocationConfigDtoValidationTest {

    private ValidatorFactory factory;
    private Validator validator;

    @BeforeAll
    void setUp() {
        factory = Validation.buildDefaultValidatorFactory();
        validator = factory.getValidator();
    }

    @AfterAll
    void tearDown() {
        factory.close();
    }

    @Test
    void defaultOnlyIsValid() {
        var priority = new PriorityDto(7L, null);
        assertThat(validator.validate(priority)).isEmpty();
    }

    @Test
    void defaultWithOverridesIsValid() {
        var priority = new PriorityDto(7L, Map.of("nca-1", 3L));
        assertThat(validator.validate(priority)).isEmpty();
    }

    @Test
    void emptyPriorityIsValid() {
        var priority = new PriorityDto(null, null);
        assertThat(validator.validate(priority)).isEmpty();
    }

    @Test
    void overridesWithoutDefaultIsRejected() {
        var priority = new PriorityDto(null, Map.of("nca-1", 3L));
        var violations = validator.validate(priority);
        assertThat(violations).isNotEmpty();
        assertThat(violations).anyMatch(v -> v.getMessage().contains("defaultPriority"));
    }

    @Test
    void negativeDefaultIsRejected() {
        var violations = validator.validate(new PriorityDto(-1L, null));
        assertThat(violations).isNotEmpty();
        assertThat(violations)
                .anyMatch(v -> v.getPropertyPath().toString().contains("defaultPriority"));
    }

    @Test
    void defaultAboveU32MaxIsRejected() {
        var violations = validator.validate(new PriorityDto(PriorityDto.MAX_PRIORITY + 1, null));
        assertThat(violations).isNotEmpty();
        assertThat(violations)
                .anyMatch(v -> v.getPropertyPath().toString().contains("defaultPriority"));
    }

    @Test
    void u32MaxDefaultIsValid() {
        assertThat(validator.validate(new PriorityDto(PriorityDto.MAX_PRIORITY, null))).isEmpty();
    }

    @Test
    void negativeOverrideValueIsRejected() {
        var priority = new PriorityDto(0L, Map.of("nca-1", -5L));
        var violations = validator.validate(priority);
        assertThat(violations).isNotEmpty();
        assertThat(violations)
                .anyMatch(v -> v.getPropertyPath().toString().contains("perAccountPriority"));
    }

    @Test
    void tooManyOverrideEntriesIsRejected() {
        var overrides = new HashMap<String, Long>();
        for (int i = 0; i <= PriorityDto.MAX_PER_ACCOUNT_ENTRIES; i++) {
            // One past the cap, so the map exceeds MAX_PER_ACCOUNT_ENTRIES.
            overrides.put("nca-" + i, 1L);
        }
        var violations = validator.validate(new PriorityDto(0L, overrides));
        assertThat(violations).isNotEmpty();
        assertThat(violations)
                .anyMatch(v -> v.getPropertyPath().toString().contains("perAccountPriority"));
    }

    @Test
    void wrapperCascadesToPriorityValidation() {
        var llmConfig = new LlmInvocationConfigDto(new PriorityDto(null, Map.of("nca-1", 3L)));
        var violations = validator.validate(llmConfig);
        assertThat(violations).isNotEmpty();
        // The cascade reaches the nested class-level @ValidPriority rule.
        assertThat(violations).anyMatch(v -> v.getMessage().contains("defaultPriority"));
    }

    @Test
    void wrapperCascadesToMapValueRange() {
        var llmConfig = new LlmInvocationConfigDto(new PriorityDto(0L, Map.of("nca-1", -5L)));
        var violations = validator.validate(llmConfig);
        assertThat(violations).isNotEmpty();
        // The cascade reaches the nested map-value @Min constraint.
        assertThat(violations)
                .anyMatch(v -> v.getPropertyPath().toString().contains("perAccountPriority"));
    }
}
