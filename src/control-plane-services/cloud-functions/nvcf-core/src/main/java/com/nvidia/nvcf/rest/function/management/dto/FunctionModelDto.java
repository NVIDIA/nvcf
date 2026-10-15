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

import com.fasterxml.jackson.annotation.JsonIgnore;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.net.URI;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;

@Builder
@Data
@NoArgsConstructor
@AllArgsConstructor
@Schema(description = "Model associated with a function, including optional LLM routing metadata")
public class FunctionModelDto {

    @Schema(description = "Model name")
    @NonNull
    @NotNull
    private String name;

    @Schema(description = "Model version. Optional for LLM-type functions.")
    @Nullable
    private String version;

    @Schema(description = "Model URI. Optional for LLM-type functions.")
    @Nullable
    private URI uri;

    @Schema(description = "LLM-specific configuration for this model. " +
            "Only relevant for LLM-type functions.")
    @Nullable
    private LlmConfigDto llmConfig;

    @Builder
    @Data
    @NoArgsConstructor
    @AllArgsConstructor
    @Schema(types = {"object"}, description = "LLM-specific configuration for a model")
    public static class LlmConfigDto {

        @Schema(description = "OpenAI-compatible URIs supported by this model " +
                "(e.g. /v1/chat/completions). Required for LLM models.")
        @NotEmpty
        private List<String> uris;

        @Schema(description = "Token-level rate limit for this model.")
        @Nullable
        private String tokenRateLimit;

        @Schema(description = "Tokenizer identifier for this model.")
        @Nullable
        private String tokenizer;

        @Schema(description = "Routing method for this model.")
        @Nullable
        private String routingMethod;

        @JsonIgnore
        public boolean hasContent() {
            return (uris != null && !uris.isEmpty())
                    || tokenRateLimit != null
                    || tokenizer != null
                    || routingMethod != null;
        }
    }
}
