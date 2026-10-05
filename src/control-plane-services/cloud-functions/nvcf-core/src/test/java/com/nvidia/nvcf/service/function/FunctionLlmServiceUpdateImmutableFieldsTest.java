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
package com.nvidia.nvcf.service.function;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class FunctionLlmServiceUpdateImmutableFieldsTest {

    private static final UUID FUNCTION_ID = UUID.randomUUID();
    private static final String MODEL = "meta/llama-3.1-8b-instruct";
    private static final List<String> URIS = List.of("/v1/chat/completions");

    @Mock
    private FunctionLookupService functionLookupService;
    @Mock
    private FunctionMapperService functionMapperService;
    @InjectMocks
    private FunctionLlmService service;

    private FunctionEntity function;

    @BeforeEach
    void setUp() {
        function = FunctionEntity.builder()
                .functionId(FUNCTION_ID)
                .functionVersionId(UUID.randomUUID())
                .ncaId("nca-test")
                .functionName("llm-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .functionType(FunctionType.LLM)
                .inferenceUrl("/v1/chat/completions")
                .apiBodyFormat(ApiBodyFormat.CUSTOM)
                .modelSpecs(Map.of(MODEL, "{}"))
                .build();
        when(functionLookupService.lookupUsingFunctionId(FUNCTION_ID))
                .thenReturn(List.of(function));
        when(functionMapperService.toFunctionModels(function.getModelSpecs()))
                .thenReturn(List.of(storedModel()));
    }

    @Test
    void rejectsUrisChangeNamingTheField() {
        var request = update(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                .uris(List.of("/v1/chat/completions", "/v1/responses"))
                .build());

        assertThatThrownBy(() -> service.applyLlmUpdates(function, request))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("llmConfig.uris")
                .hasMessageContaining(MODEL);
    }

    @Test
    void rejectsTokenizerChangeNamingTheField() {
        var request = update(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                .tokenizer("other-tokenizer")
                .build());

        assertThatThrownBy(() -> service.applyLlmUpdates(function, request))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("llmConfig.tokenizer")
                .hasMessageContaining(MODEL);
    }

    @Test
    void acceptsResubmittedUnchangedUrisAndAppliesRoutingMethod() {
        var request = update(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                .uris(URIS)
                .tokenizer("meta-llama-tokenizer")
                .routingMethod("round-robin")
                .build());

        var siblingsToResave = service.applyLlmUpdates(function, request);

        assertThat(siblingsToResave).isEmpty();
    }

    private static UpdateFunctionRequest update(
            UpdateFunctionRequest.LlmConfigUpdateDto llmConfig) {
        return UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName(MODEL)
                        .llmConfig(llmConfig)
                        .build()))
                .build();
    }

    private static FunctionModelDto storedModel() {
        return FunctionModelDto.builder()
                .name(MODEL)
                .llmConfig(FunctionModelDto.LlmConfigDto.builder()
                        .uris(URIS)
                        .tokenizer("meta-llama-tokenizer")
                        .routingMethod("power-of-two")
                        .build())
                .build();
    }
}
