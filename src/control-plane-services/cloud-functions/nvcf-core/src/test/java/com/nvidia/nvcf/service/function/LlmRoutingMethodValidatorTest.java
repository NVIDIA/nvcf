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

import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.configuration.llm.LlmRoutingExpressionsProperties;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class LlmRoutingMethodValidatorTest {

    private static final String MODEL = "test-model";

    private final LlmRoutingExpressionsProperties properties =
            new LlmRoutingExpressionsProperties();
    private final LlmRoutingMethodValidator validator = new LlmRoutingMethodValidator(properties);

    @ParameterizedTest
    @ValueSource(strings = {"pulsar;seed=x", "round robin;seed=?1", "pulsar,seed=x;n=1"})
    void disabledRejectsParametersBeforeGrammar(String routingMethod) {
        assertThatThrownBy(() -> validator.validate(MODEL, routingMethod))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("not enabled")
                .hasMessageContaining(MODEL);
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"pulsar", "Power_Of_Two", "groq-multiregion", "  pulsar  ", "   "})
    void disabledAcceptsMethodOnlyAndBlankValues(String routingMethod) {
        assertThatCode(() -> validator.validate(MODEL, routingMethod)).doesNotThrowAnyException();
    }

    @Test
    void disabledRejectsMalformedMethodOnlyValue() {
        assertThatThrownBy(() -> validator.validate(MODEL, "round robin"))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining(MODEL)
                .hasMessageContaining("method name must match");
    }

    @Test
    void enabledAcceptsParameters() {
        properties.setEnabled(true);

        assertThatCode(() -> validator.validate(MODEL, "pulsar;seed=x"))
                .doesNotThrowAnyException();
    }

    @Test
    void enabledRejectsMalformedExpression() {
        properties.setEnabled(true);

        assertThatThrownBy(() -> validator.validate(MODEL, "pulsar,seed=x"))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining(MODEL)
                .hasMessageContaining("commas are not allowed");
    }

    @Test
    void readsPropertyOnEachValidation() {
        assertThatThrownBy(() -> validator.validate(MODEL, "pulsar;seed=x"))
                .isInstanceOf(BadRequestException.class);
        properties.setEnabled(true);

        assertThatCode(() -> validator.validate(MODEL, "pulsar;seed=x"))
                .doesNotThrowAnyException();
    }
}
