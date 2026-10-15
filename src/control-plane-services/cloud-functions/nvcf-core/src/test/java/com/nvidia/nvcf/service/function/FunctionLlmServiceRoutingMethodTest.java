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
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class FunctionLlmServiceRoutingMethodTest {

    private static final UUID FUNCTION_ID = UUID.randomUUID();
    private static final String MODEL = "meta/llama-3.1-8b-instruct";

    @Mock
    private FunctionLookupService functionLookupService;
    @Mock
    private FunctionMapperService functionMapperService;
    @InjectMocks
    private FunctionLlmService service;

    @Test
    void versionCreateWithOuterSpacesLeavesMatchingSiblingUnchanged() {
        var sibling = version();
        sibling.setModelSpecs(Map.of(MODEL, "{}"));
        when(functionMapperService.toFunctionModels(sibling.getModelSpecs()))
                .thenReturn(List.of(model("pulsar")));
        var request = CreateFunctionRequest.builder()
                .name("llm-function")
                .inferenceUrl(URI.create("/v1/chat/completions"))
                .models(List.of(model(" pulsar ")))
                .build();

        var siblingsToResave = service.reconcileForNewVersion(
                version(), request, List.of(sibling));

        assertThat(siblingsToResave).isEmpty();
        verify(functionMapperService, never()).toModelSpecs(any());
    }

    private static FunctionEntity version() {
        return FunctionEntity.builder()
                .functionId(FUNCTION_ID)
                .functionVersionId(UUID.randomUUID())
                .ncaId("nca-test")
                .functionName("llm-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v1/chat/completions")
                .apiBodyFormat(ApiBodyFormat.CUSTOM)
                .build();
    }

    private static FunctionModelDto model(String routingMethod) {
        return FunctionModelDto.builder()
                .name(MODEL)
                .llmConfig(FunctionModelDto.LlmConfigDto.builder()
                        .uris(List.of("/v1/chat/completions"))
                        .routingMethod(routingMethod)
                        .build())
                .build();
    }
}
