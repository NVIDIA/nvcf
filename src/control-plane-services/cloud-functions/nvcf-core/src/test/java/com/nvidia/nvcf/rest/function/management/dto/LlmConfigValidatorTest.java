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
        "power-of-two", "round-robin", "random", "groq-multiregion", "pulsar",
        // Router normalizes case and '_' to '-', so these are accepted too.
        "Power-Of-Two", "power_of_two", "  pulsar  "
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
    @ValueSource(strings = {"100000-S", "10-M", "5-H", "1-D", "2-W", "10-M,5-S", "10-M, 5-S"})
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
        "10-SS",     // multi-char unit
        "10-M,5-M",  // duplicate unit
        "10-M,",     // trailing empty fragment
        "10-M,bad"   // one bad fragment
    })
    void invalidTokenRateLimitsRejected(String tokenRateLimit) {
        assertThatThrownBy(() -> LlmConfigValidator.validateTokenRateLimit(MODEL, tokenRateLimit))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("tokenRateLimit")
                .hasMessageContaining(MODEL);
    }
}
