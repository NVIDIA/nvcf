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

import com.nvidia.nvcf.rest.function.management.dto.LlmInvocationConfigDto;
import com.nvidia.nvcf.rest.function.management.dto.PriorityDto;
import java.util.HashMap;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class FunctionLlmServicePriorityTest {

    private static final String CALLER_NCA = "nca-caller";
    private static final String OTHER_NCA  = "nca-other";

    @Mock
    private FunctionLookupService functionLookupService;
    @Mock
    private FunctionMapperService functionMapperService;
    @InjectMocks
    private FunctionLlmService service;

    @Test
    void callerInOverrideMapReturnsOverride() {
        var config = new LlmInvocationConfigDto(
                new PriorityDto(10L, Map.of(CALLER_NCA, 2L)));
        assertThat(service.resolveInvocationPriority(CALLER_NCA, config))
                .hasValue(2L);
    }

    @Test
    void callerNotInMapFallsBackToDefault() {
        var config = new LlmInvocationConfigDto(
                new PriorityDto(10L, Map.of(OTHER_NCA, 2L)));
        assertThat(service.resolveInvocationPriority(CALLER_NCA, config))
                .hasValue(10L);
    }

    @Test
    void defaultOnlyReturnsDefault() {
        var config = new LlmInvocationConfigDto(new PriorityDto(5L, null));
        assertThat(service.resolveInvocationPriority(CALLER_NCA, config))
                .hasValue(5L);
    }

    @Test
    void nullDefaultAndNoMatchingOverrideReturnsEmpty() {
        var config = new LlmInvocationConfigDto(
                new PriorityDto(null, Map.of(OTHER_NCA, 3L)));
        assertThat(service.resolveInvocationPriority(CALLER_NCA, config))
                .isEmpty();
    }

    @Test
    void zeroDefaultReturnsZeroNotEmpty() {
        var config = new LlmInvocationConfigDto(new PriorityDto(0L, null));
        assertThat(service.resolveInvocationPriority(CALLER_NCA, config))
                .hasValue(0L);
    }

    @Test
    void zeroOverrideShadowsNonzeroDefault() {
        var config = new LlmInvocationConfigDto(
                new PriorityDto(10L, Map.of(CALLER_NCA, 0L)));
        assertThat(service.resolveInvocationPriority(CALLER_NCA, config))
                .hasValue(0L);
    }

    @Test
    void nullOverrideValueFallsBackToDefault() {
        var overrides = new HashMap<String, Long>();
        overrides.put(CALLER_NCA, null);
        var config = new LlmInvocationConfigDto(new PriorityDto(10L, overrides));
        assertThat(service.resolveInvocationPriority(CALLER_NCA, config))
                .hasValue(10L);
    }

    @Test
    void nullConfigReturnsEmpty() {
        assertThat(service.resolveInvocationPriority(CALLER_NCA, null))
                .isEmpty();
    }

    @Test
    void configWithNullPriorityReturnsEmpty() {
        var config = new LlmInvocationConfigDto(null);
        assertThat(service.resolveInvocationPriority(CALLER_NCA, config))
                .isEmpty();
    }
}
