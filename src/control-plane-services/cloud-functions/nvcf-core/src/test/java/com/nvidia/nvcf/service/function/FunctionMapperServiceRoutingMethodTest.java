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

import com.nvidia.nvcf.configuration.JacksonConfiguration;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import java.util.List;
import org.junit.jupiter.api.Test;

class FunctionMapperServiceRoutingMethodTest {

    private static final String MODEL = "meta/llama-3.1-8b-instruct";
    private static final List<String> URIS = List.of("/v1/chat/completions");

    private final FunctionMapperService mapper =
            new FunctionMapperService(new JacksonConfiguration().jsonMapper(), null);

    @Test
    void toModelSpecsStoresRoutingMethodWithoutOuterSpacesAndLeavesTheRequestAsReceived() {
        var model = FunctionModelDto.builder()
                .name(MODEL)
                .llmConfig(FunctionModelDto.LlmConfigDto.builder()
                        .uris(URIS)
                        .tokenizer("meta-llama-tokenizer")
                        .routingMethod("  pulsar;seed=x  ")
                        .build())
                .build();

        var specs = mapper.toModelSpecs(List.of(model));

        var stored = mapper.toFunctionModels(specs).getFirst().getLlmConfig();
        assertThat(stored.getRoutingMethod()).isEqualTo("pulsar;seed=x");
        assertThat(stored.getUris()).isEqualTo(URIS);
        assertThat(stored.getTokenizer()).isEqualTo("meta-llama-tokenizer");
        assertThat(model.getLlmConfig().getRoutingMethod()).isEqualTo("  pulsar;seed=x  ");
    }
}
