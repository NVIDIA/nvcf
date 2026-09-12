/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.rest.function.management.dto;

import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.exceptions.BadRequestException;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class LlmConfigValidatorTest {

    private static final String MODEL = "meta/llama-3.1-8b-instruct";

    @ParameterizedTest
    @ValueSource(strings = {
        "power-of-two", "wait-and-widen", "round-robin", "random", "pulsar",
        "pulsar-wait-and-widen", "groq-multiregion", "pulsar-multiregion",
        // Router normalizes case and '_' to '-', so these are accepted too.
        "Power-Of-Two", "power_of_two", "wait_and_widen", "pulsar_wait_and_widen",
        "groq_multiregion", "pulsar_multiregion", "  pulsar  "
    })
    void validRoutingMethodsAccepted(String routingMethod) {
        assertThatCode(() -> LlmConfigValidator.validateRoutingMethod(MODEL, routingMethod))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"   "})
    void blankRoutingMethodAccepted(String routingMethod) {
        assertThatCode(() -> LlmConfigValidator.validateRoutingMethod(MODEL, routingMethod))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @ValueSource(strings = {"weighted", "sticky", "not-a-method", "round robin", "power-of-3"})
    void invalidRoutingMethodsRejected(String routingMethod) {
        assertThatThrownBy(() -> LlmConfigValidator.validateRoutingMethod(MODEL, routingMethod))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("routingMethod")
                .hasMessageContaining(MODEL);
    }

    @ParameterizedTest
    @ValueSource(strings = {
        "100000-S", "10-M", "5-H", "1-D", "2-W", "3-MO", "10-M,5-S", "10-M, 5-S", "3-MO,10-M",
        // Minute and Month must not be confused with each other in either order.
        "1-M,2-MO", "1-MO,2-M", "1-M,2-H,3-D,4-W,5-MO"
    })
    void validTokenRateLimitsAccepted(String tokenRateLimit) {
        assertThatCode(() -> LlmConfigValidator.validateTokenRateLimit(MODEL, tokenRateLimit))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"   "})
    void blankTokenRateLimitAccepted(String tokenRateLimit) {
        assertThatCode(() -> LlmConfigValidator.validateTokenRateLimit(MODEL, tokenRateLimit))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @ValueSource(strings = {
        "20",        // no unit
        "20-X",      // bad unit
        "-5-S",      // negative value
        "+5-S",      // signed value
        "0-S",       // zero value
        "abc-S",     // non-numeric value
        "10-",       // missing unit
        "-S",        // missing value
        "10-SS",     // multi-char unit that isn't MO
        "10-M,5-M",  // duplicate unit
        "10-MO,5-MO", // duplicate MO unit
        "10-M,",     // trailing empty fragment
        "10-M,bad"   // one bad fragment
    })
    void invalidTokenRateLimitsRejected(String tokenRateLimit) {
        assertThatThrownBy(() -> LlmConfigValidator.validateTokenRateLimit(MODEL, tokenRateLimit))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("tokenRateLimit")
                .hasMessageContaining(MODEL);
    }

    @ParameterizedTest
    @ValueSource(strings = {
        "100000-S", "10-M", "5-H", "1-D", "2-W", "3-MO", "10-M,5-S", "3-MO,10-M",
        // Minute and Month must not be confused with each other in either order.
        "1-M,2-MO", "1-MO,2-M", "1-M,2-H,3-D,4-W,5-MO"
    })
    void validInputTokenRateLimitsAccepted(String inputTokenRateLimit) {
        assertThatCode(() -> LlmConfigValidator.validateInputTokenRateLimit(MODEL, inputTokenRateLimit))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"   "})
    void blankInputTokenRateLimitAccepted(String inputTokenRateLimit) {
        assertThatCode(() -> LlmConfigValidator.validateInputTokenRateLimit(MODEL, inputTokenRateLimit))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @ValueSource(strings = {"20", "20-X", "0-S", "10-M,5-M", "10-MO,5-MO"})
    void invalidInputTokenRateLimitsRejected(String inputTokenRateLimit) {
        assertThatThrownBy(
                () -> LlmConfigValidator.validateInputTokenRateLimit(MODEL, inputTokenRateLimit))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("inputTokenRateLimit")
                .hasMessageContaining(MODEL);
    }

    @ParameterizedTest
    @ValueSource(strings = {
        "100000-S", "10-M", "5-H", "1-D", "2-W", "3-MO", "10-M,5-S", "3-MO,10-M",
        // Minute and Month must not be confused with each other in either order.
        "1-M,2-MO", "1-MO,2-M", "1-M,2-H,3-D,4-W,5-MO"
    })
    void validOutputTokenRateLimitsAccepted(String outputTokenRateLimit) {
        assertThatCode(() -> LlmConfigValidator.validateOutputTokenRateLimit(MODEL, outputTokenRateLimit))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"   "})
    void blankOutputTokenRateLimitAccepted(String outputTokenRateLimit) {
        assertThatCode(() -> LlmConfigValidator.validateOutputTokenRateLimit(MODEL, outputTokenRateLimit))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @ValueSource(strings = {"20", "20-X", "0-S", "10-M,5-M", "10-MO,5-MO"})
    void invalidOutputTokenRateLimitsRejected(String outputTokenRateLimit) {
        assertThatThrownBy(
                () -> LlmConfigValidator.validateOutputTokenRateLimit(MODEL, outputTokenRateLimit))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("outputTokenRateLimit")
                .hasMessageContaining(MODEL);
    }
}
