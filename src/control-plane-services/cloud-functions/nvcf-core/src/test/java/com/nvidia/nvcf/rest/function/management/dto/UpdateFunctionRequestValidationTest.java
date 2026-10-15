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
import java.util.List;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class UpdateFunctionRequestValidationTest {

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
    void urisInModelUpdateIsRejectedNamingTheField() {
        var request = request(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                .uris(List.of("/v1/responses"))
                .routingMethod("round-robin")
                .build());
        assertThat(validator.validate(request))
                .anyMatch(v -> v.getMessage().contains("llmConfig.uris"));
    }

    @Test
    void tokenizerInModelUpdateIsRejectedNamingTheField() {
        var request = request(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                .tokenizer("tok")
                .build());
        assertThat(validator.validate(request))
                .anyMatch(v -> v.getMessage().contains("llmConfig.tokenizer"));
    }

    @Test
    void routingMethodAndTokenRateLimitAreAccepted() {
        var request = request(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                .routingMethod("round-robin")
                .tokenRateLimit("1-M")
                .build());
        assertThat(validator.validate(request)).isEmpty();
    }

    @Test
    void emptyLlmConfigIsRejected() {
        var request = request(UpdateFunctionRequest.LlmConfigUpdateDto.builder().build());
        assertThat(validator.validate(request)).isNotEmpty();
    }

    private static UpdateFunctionRequest request(
            UpdateFunctionRequest.LlmConfigUpdateDto llmConfig) {
        return UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName("m")
                        .llmConfig(llmConfig)
                        .build()))
                .build();
    }
}
