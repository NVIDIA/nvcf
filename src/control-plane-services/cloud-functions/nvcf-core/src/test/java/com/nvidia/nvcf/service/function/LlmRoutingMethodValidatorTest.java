/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
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
