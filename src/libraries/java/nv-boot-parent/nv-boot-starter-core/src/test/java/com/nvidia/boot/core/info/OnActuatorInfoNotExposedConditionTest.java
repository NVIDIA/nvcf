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

package com.nvidia.boot.core.info;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import org.junit.jupiter.api.Test;
import org.springframework.context.annotation.ConditionContext;
import org.springframework.core.env.Environment;
import org.springframework.core.type.AnnotatedTypeMetadata;

class OnActuatorInfoNotExposedConditionTest {

    private final OnActuatorInfoNotExposedCondition condition = new OnActuatorInfoNotExposedCondition();
    private final AnnotatedTypeMetadata metadata = mock(AnnotatedTypeMetadata.class);

    @Test
    void matchesWhenExposurePropertyIsAbsent() {
        assertThat(condition.getMatchOutcome(contextWithExposure(null), metadata).isMatch()).isTrue();
    }

    @Test
    void matchesWhenExposureDoesNotIncludeInfo() {
        assertThat(condition.getMatchOutcome(contextWithExposure("health"), metadata).isMatch()).isTrue();
    }

    @Test
    void noMatchWhenExposureIncludesInfo() {
        assertThat(condition.getMatchOutcome(contextWithExposure("health,info"), metadata).isMatch()).isFalse();
    }

    @Test
    void noMatchWhenExposureIsWildcard() {
        assertThat(condition.getMatchOutcome(contextWithExposure("*"), metadata).isMatch()).isFalse();
    }

    private ConditionContext contextWithExposure(String exposureValue) {
        var environment = mock(Environment.class);
        when(environment.getProperty("management.endpoints.web.exposure.include", ""))
                .thenReturn(exposureValue == null ? "" : exposureValue);

        var context = mock(ConditionContext.class);
        when(context.getEnvironment()).thenReturn(environment);
        return context;
    }
}
