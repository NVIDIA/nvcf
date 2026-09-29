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

import com.nvidia.nvcf.configuration.JacksonConfiguration;
import com.nvidia.nvcf.rest.function.management.dto.LlmInvocationConfigDto;
import com.nvidia.nvcf.rest.function.management.dto.PriorityDto;
import java.util.Map;
import org.junit.jupiter.api.Test;

class FunctionMapperServiceLlmInvocationConfigTest {

    private final FunctionMapperService mapper =
            new FunctionMapperService(new JacksonConfiguration().jsonMapper(), null);

    @Test
    void priorityWithOverridesSerializesAndDeserializesBack() {
        var original = new LlmInvocationConfigDto(new PriorityDto(7L, Map.of("nca-1", 3L)));
        var json = mapper.toLlmInvocationConfigJson(original);
        assertThat(json)
                .contains("version")
                .contains("priority")
                .contains("defaultPriority")
                .contains("perAccountPriority");
        assertThat(mapper.toLlmInvocationConfigDto(json)).isEqualTo(original);
    }

    @Test
    void defaultPriorityOnlySerializesAndDeserializesBack() {
        var original = new LlmInvocationConfigDto(new PriorityDto(7L, null));
        assertThat(mapper.toLlmInvocationConfigDto(mapper.toLlmInvocationConfigJson(original)))
                .isEqualTo(original);
    }

    @Test
    void nullConfigSerializesToNull() {
        assertThat(mapper.toLlmInvocationConfigJson(null)).isNull();
    }

    @Test
    void emptyConfigSerializesToVersionedJson() {
        // No empty-collapsing: a present-but-empty config still serializes, with the version stamp.
        assertThat(mapper.toLlmInvocationConfigJson(new LlmInvocationConfigDto(null))).contains("version");
        assertThat(mapper.toLlmInvocationConfigJson(new LlmInvocationConfigDto(new PriorityDto(null, null))))
                .contains("version");
    }

    @Test
    void blankJsonDeserializesToNull() {
        assertThat(mapper.toLlmInvocationConfigDto(null)).isNull();
        assertThat(mapper.toLlmInvocationConfigDto("")).isNull();
        assertThat(mapper.toLlmInvocationConfigDto("   ")).isNull();
    }

    @Test
    void emptyConfigJsonDeserializesToEmptyDto() {
        // Plain CRUD: an empty stored config (version stamp only) reads back as an empty DTO,
        // not null.
        assertThat(mapper.toLlmInvocationConfigDto("{\"version\":1}"))
                .isEqualTo(new LlmInvocationConfigDto(null));
        assertThat(mapper.toLlmInvocationConfigDto("{\"version\":1,\"priority\":{}}"))
                .isEqualTo(new LlmInvocationConfigDto(new PriorityDto(null, null)));
    }

    @Test
    void corruptJsonThrows() {
        // A corrupt stored blob surfaces as an error rather than being silently dropped.
        assertThatThrownBy(() -> mapper.toLlmInvocationConfigDto("{not valid json"))
                .isInstanceOf(IllegalStateException.class);
    }

    @Test
    void missingStorageVersionThrows() {
        // A blob without the storage version stamp is rejected rather than read as the current
        // version, so a future schema change cannot be silently misread.
        assertThatThrownBy(
                        () ->
                                mapper.toLlmInvocationConfigDto(
                                        "{\"priority\":{\"defaultPriority\":5}}"))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("version");
    }

    @Test
    void unsupportedStorageVersionThrows() {
        // A blob stamped with an unknown storage version is rejected rather than misread.
        assertThatThrownBy(
                        () ->
                                mapper.toLlmInvocationConfigDto(
                                        "{\"version\":2,\"priority\":{\"defaultPriority\":5}}"))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("Unsupported");
    }
}
