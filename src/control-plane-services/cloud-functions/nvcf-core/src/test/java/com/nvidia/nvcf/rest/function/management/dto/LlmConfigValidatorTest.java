/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.rest.function.management.dto;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.exceptions.BadRequestException;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class LlmConfigValidatorTest {

    private static final String MODEL = "meta/llama-3.1-8b-instruct";

    @ParameterizedTest
    @CsvSource(textBlock = """
            # method-only values, canonical spellings kept
            pulsar                                  | pulsar
            random                                  | random
            round-robin                             | round-robin
            wait-and-widen                          | wait-and-widen
            pulsar-wait-and-widen                   | pulsar-wait-and-widen
            power-of-n                              | power-of-n
            # spelling variants normalize to the router's canonical names
            round_robin                             | round-robin
            Power-Of-Two                            | power-of-n
            power_of_two                            | power-of-n
            powerOf2                                | power-of-n
            groq-multiregion                        | wait-and-widen
            groq_multiregion                        | wait-and-widen
            pulsar-multiregion                      | pulsar-wait-and-widen
            '  pulsar  '                            | pulsar
            # expressions with parameters; space after ';' is dropped
            pulsar;seed=stable-a                    | pulsar;seed=stable-a
            'pulsar; seed=stable-a'                 | pulsar;seed=stable-a
            power_of_two;sample_count=4             | power-of-n;sample_count=4
            pulsar;seed=stable-a;n=2                | pulsar;seed=stable-a;n=2
            pulsar;consider_kv_free_tokens=true     | pulsar;consider_kv_free_tokens=true
            wait-and-widen;next_bucket_unlock_factor=0.25 | wait-and-widen;next_bucket_unlock_factor=0.25
            pulsar;seed="stable a"                  | pulsar;seed="stable a"
            pulsar;n=-7                             | pulsar;n=-7
            # format-only: unknown methods and parameters persist by design
            fastest                                 | fastest
            pulsar;widen=2                          | pulsar;widen=2
            fastest;seed=x                          | fastest;seed=x
            """, delimiter = '|')
    void wellFormedRoutingMethodsNormalized(String routingMethod, String expected) {
        assertThat(LlmConfigValidator.validateAndNormalizeRoutingMethod(MODEL, routingMethod))
                .isEqualTo(expected);
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"   "})
    void blankRoutingMethodReturnedUnchanged(String routingMethod) {
        assertThatCode(() -> {
            var result = LlmConfigValidator.validateAndNormalizeRoutingMethod(
                    MODEL, routingMethod);
            assertThat(result).isEqualTo(routingMethod);
        }).doesNotThrowAnyException();
    }

    @ParameterizedTest
    @ValueSource(strings = {
        "round robin",                  // whitespace inside the algorithm token
        "9lives",                       // token must start with a letter
        "pulsar ;seed=x",               // space before ';'
        "pulsar;seed = x",              // space around '='
        "pulsar;;n=2",                  // empty parameter
        "pulsar;n=2;",                  // trailing ';'
        "pulsar;n=2;n=3",               // duplicate key
        "pulsar;Seed=x",                // uppercase key
        "pulsar;flag",                  // valueless parameter
        "pulsar;n=",                    // empty value
        "pulsar;b=?1",                  // SFV boolean syntax
        "pulsar;f=0.1234",              // more than 3 fractional digits
        "pulsar;s=\"unterminated",      // unterminated quoted string
        "pul,sar",                      // comma in token
        "pulsar;s=\"a,b\"",             // comma inside a quoted string
        "pulsar;s=\"a;b\"",             // semicolon inside a quoted string
        "pulsar;s=a b",                 // whitespace inside a bare value
        "pulsar/v2",                    // algorithm charset is [a-z0-9-] after normalization
        "*pulsar",                      // algorithm must start with a letter
    })
    void malformedRoutingMethodsRejected(String routingMethod) {
        assertThatThrownBy(
                () -> LlmConfigValidator.validateAndNormalizeRoutingMethod(MODEL, routingMethod))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("routingMethod")
                .hasMessageContaining(MODEL);
    }

    @Test
    void oversizedRoutingMethodRejected() {
        var oversized = "pulsar;seed=" + "a".repeat(1024);
        assertThatThrownBy(
                () -> LlmConfigValidator.validateAndNormalizeRoutingMethod(MODEL, oversized))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("1024");
    }

    @Test
    void parameterCountCappedAt32() {
        var atCap = new StringBuilder("pulsar");
        for (var i = 0; i < 32; i++) {
            atCap.append(";p").append(i).append("=1");
        }
        assertThat(LlmConfigValidator.validateAndNormalizeRoutingMethod(MODEL, atCap.toString()))
                .isEqualTo(atCap.toString());
        var overCap = atCap + ";p32=1";
        assertThatThrownBy(
                () -> LlmConfigValidator.validateAndNormalizeRoutingMethod(MODEL, overCap))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("32 parameters");
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
