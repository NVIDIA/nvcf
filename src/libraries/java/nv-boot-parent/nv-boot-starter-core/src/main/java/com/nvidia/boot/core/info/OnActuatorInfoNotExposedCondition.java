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

import java.util.Arrays;
import java.util.Locale;
import org.springframework.boot.autoconfigure.condition.ConditionMessage;
import org.springframework.boot.autoconfigure.condition.ConditionOutcome;
import org.springframework.boot.autoconfigure.condition.SpringBootCondition;
import org.springframework.context.annotation.ConditionContext;
import org.springframework.core.type.AnnotatedTypeMetadata;

/**
 * Backs off the shared {@code /info} endpoint when the consuming application already exposes
 * Spring Boot Actuator's own {@code info} endpoint, since Actuator's endpoint is registered
 * dynamically (not as a discoverable bean) and would otherwise conflict with this controller
 * for the same {@code GET /info} path.
 */
class OnActuatorInfoNotExposedCondition extends SpringBootCondition {

    private static final String EXPOSURE_INCLUDE_PROPERTY = "management.endpoints.web.exposure.include";

    @Override
    public ConditionOutcome getMatchOutcome(ConditionContext context, AnnotatedTypeMetadata metadata) {
        var exposure = context.getEnvironment().getProperty(EXPOSURE_INCLUDE_PROPERTY, "");
        var exposesInfo = Arrays.stream(exposure.split(","))
                .map(String::trim)
                .map(value -> value.toLowerCase(Locale.ROOT))
                .anyMatch(value -> value.equals("*") || value.equals("info"));

        var condition = ConditionMessage.forCondition("OnActuatorInfoNotExposed");
        if (exposesInfo) {
            return ConditionOutcome.noMatch(
                    condition.because(EXPOSURE_INCLUDE_PROPERTY + " already exposes 'info'"));
        }
        return ConditionOutcome.match(
                condition.because(EXPOSURE_INCLUDE_PROPERTY + " does not expose 'info'"));
    }
}
