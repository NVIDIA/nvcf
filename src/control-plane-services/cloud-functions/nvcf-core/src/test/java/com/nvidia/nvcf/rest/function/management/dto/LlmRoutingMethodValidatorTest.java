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

import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.exceptions.BadRequestException;
import java.util.stream.Collectors;
import java.util.stream.IntStream;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class LlmRoutingMethodValidatorTest {

    private static final String MODEL = "meta/llama-3.1-8b-instruct";

    @ParameterizedTest
    @ValueSource(strings = {
        "power-of-two", "wait-and-widen", "round-robin", "random", "pulsar",
        "pulsar-wait-and-widen", "groq-multiregion", "pulsar-multiregion",
        "Power-Of-Two", "power_of_two", "wait_and_widen", "pulsar_wait_and_widen",
        "groq_multiregion", "pulsar_multiregion", "  pulsar  ",
        "pulsar;seed=stable-a",
        "pulsar; seed=stable-a; consider_kv_free_tokens=true",
        "pulsar-wait-and-widen;seed=stable-a;n=2;max_queue_time_floor_ms=100;"
                + "max_queue_time_ceil_ms=500",
        "wait-and-widen;next_bucket_unlock_factor=\"0.0625\"",
        "power-of-n;sample_count=4;comparator=queue-time",
        "wait-and-widen;max_input_work_seconds=1.5",
        "wait-and-widen;n=-1", "fastest;widen=2",
        "pulsar;  seed=x", "  pulsar;seed=x  ",
        "pulsar;n=999999999999999", "pulsar;n=-999999999999999",
        "pulsar;n=999999999999.999", "pulsar;n=-999999999999.999",
        "pulsar;seed=*", "pulsar;seed=a!#$%&'*+.^_`|~:/-",
        "pulsar;seed=\"\"", "pulsar;seed=\"a b\"", "pulsar;seed=\"a\\\"b\\\\c\""
    })
    @MethodSource("routingMethodsAtLimits")
    void validRoutingMethodsAccepted(String routingMethod) {
        assertThatCode(() -> LlmRoutingMethodValidator.validate(MODEL, routingMethod))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"   "})
    void blankRoutingMethodAccepted(String routingMethod) {
        assertThatCode(() -> LlmRoutingMethodValidator.validate(MODEL, routingMethod))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest(name = "{index}: {0} violates {1}")
    @MethodSource("invalidRoutingMethods")
    void invalidRoutingMethodsRejected(String routingMethod, String rule) {
        assertThatThrownBy(() -> LlmRoutingMethodValidator.validate(MODEL, routingMethod))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("routingMethod")
                .hasMessageContaining(MODEL)
                .hasMessageContaining(rule);
    }

    @Test
    void controlCharactersInRejectedSegmentReplaced() {
        var routingMethod = "pulsar;seed=a\nb\rc";

        assertThatThrownBy(() -> LlmRoutingMethodValidator.validate(MODEL, routingMethod))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("parameter 'seed=a?b?c' must be key=value")
                .hasMessageNotContaining("\n")
                .hasMessageNotContaining("\r");
    }

    private static Stream<String> routingMethodsAtLimits() {
        return Stream.of("a".repeat(1024), "pulsar" + parameters(32));
    }

    private static Stream<Arguments> invalidRoutingMethods() {
        return Stream.of(
                Arguments.of("round robin", "method name must match"),
                Arguments.of("power-of-3!", "method name must match"),
                Arguments.of(";seed=x", "method name must match"),
                Arguments.of("pulsar,seed=x", "commas are not allowed"),
                Arguments.of("pulsar;seed=\"a,b\"", "commas are not allowed"),
                Arguments.of("pulsar;seed=\"a;b\"", "value for 'seed'"),
                Arguments.of("pulsar ;seed=x", "method name must match"),
                Arguments.of("pulsar;seed = x", "must be key=value"),
                Arguments.of("pulsar;seed= x", "must be key=value"),
                Arguments.of("pulsar;seed=x ;n=1", "must be key=value"),
                Arguments.of("pulsar;\tseed=x", "must be key=value"),
                Arguments.of("pulsar;seed", "must be key=value"),
                Arguments.of("pulsar;seed=", "must be key=value"),
                Arguments.of("pulsar;=x", "must be key=value"),
                Arguments.of("pulsar;Seed=x", "must be key=value"),
                Arguments.of("pulsar;2n=1", "must be key=value"),
                Arguments.of("pulsar;n=?1", "value for 'n'"),
                Arguments.of("pulsar;n=?0", "value for 'n'"),
                Arguments.of("pulsar;seed=:YQ==:", "value for 'seed'"),
                Arguments.of("pulsar;n=1.2345", "value for 'n'"),
                Arguments.of("pulsar;n=1000000000000000", "value for 'n'"),
                Arguments.of("pulsar;n=1000000000000.1", "value for 'n'"),
                Arguments.of("pulsar;n=1.", "value for 'n'"),
                Arguments.of("pulsar;n=1;n=2", "duplicate parameter 'n'"),
                Arguments.of("pulsar" + parameters(33), "at most 32 parameters"),
                Arguments.of("a".repeat(1025), "expression exceeds 1024 bytes"),
                Arguments.of("pulsar;seed=\"" + "\u00e9".repeat(506) + "\"",
                        "expression exceeds 1024 bytes"),
                Arguments.of("pulsar;seed=x;", "must be key=value"),
                Arguments.of("pulsar;seed=\"unterminated", "value for 'seed'"),
                Arguments.of("pulsar;seed=\"a\\nb\"", "value for 'seed'"),
                Arguments.of("pulsar;seed=\"\u00e9\"", "value for 'seed'"),
                Arguments.of("pulsar;seed=\"a\tb\"", "value for 'seed'"));
    }

    private static String parameters(int count) {
        return IntStream.range(0, count)
                .mapToObj(index -> ";p" + index + "=x")
                .collect(Collectors.joining());
    }
}
