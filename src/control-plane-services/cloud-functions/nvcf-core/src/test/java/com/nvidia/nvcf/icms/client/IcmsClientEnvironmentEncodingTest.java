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
package com.nvidia.nvcf.icms.client;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.stream.Stream;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;

class IcmsClientEnvironmentEncodingTest {

    // Expected values follow the Go double-quoted string syntax that the NVCA text decoder
    // unquotes, so each value reaches the worker as one entry.
    static Stream<Arguments> values() {
        return Stream.of(
                Arguments.of("plain value", "dynamo-frontend", "dynamo-frontend"),
                Arguments.of("number", 8000, "8000"),
                Arguments.of("quotes without control characters", "--a \"b c\"", "--a \"b c\""),
                Arguments.of("line feed", "svc\nEXTRA=1", "\"svc\\u000aEXTRA=1\""),
                Arguments.of("carriage return and tab", "a\rb\tc", "\"a\\u000db\\u0009c\""),
                Arguments.of("backslash and quote with a control character",
                             "a\\b\"c\n", "\"a\\\\b\\\"c\\u000a\""),
                Arguments.of("C1 control character", "a\u0085b", "\"a\\u0085b\""));
    }

    @ParameterizedTest(name = "{0}")
    @MethodSource("values")
    void valueIsWrittenAsOneEntry(String label, Object value, String expected) {
        assertThat(IcmsClient.encodeEnvironmentValue(value)).isEqualTo(expected);
    }
}
