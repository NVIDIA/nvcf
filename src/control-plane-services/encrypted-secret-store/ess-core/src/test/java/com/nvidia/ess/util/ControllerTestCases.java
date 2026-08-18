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
package com.nvidia.ess.util;

import java.util.stream.Stream;
import org.apache.commons.lang3.Strings;
import org.junit.jupiter.params.provider.Arguments;
import org.springframework.web.util.UriComponentsBuilder;

public class ControllerTestCases {

    public static Stream<Arguments> invalidHeadersArguments() {
        return Stream.of(
                Arguments.of("", "namespace"),
                Arguments.of(null, "namespace"),
                Arguments.of("auth header", ""),
                Arguments.of("auth header", null)
        );
    }

    public static Stream<String> invalidNamespaceOrEntityTypeOrEntityNameArguments() {
        return Stream.of(
            null,
            "",
            "ns-or-et-or-e-with-semicolon;",
            "ns-or-et-or-e-with-encoded-semicolon%3B",
            "ns-or-et-or-e-with-encoded-semicolon%3b",
            "ns-or-et-or-e-/-with-slash",
            "ns-or-et-or-e-%2f-with-encoded-slash",
            "ns-or-et-or-e-%2F-with-encoded-slash",
            "ns-or-et-or-e--with-backslash-\\",
            "ns-or-et-or-e--with-encoded-backslash-%5c",
            "ns-or-et-or-e--with-encoded-backslash-%5C",
            "ns-or-et-or-e--with-lf-\n",
            "ns-or-et-or-e--with-encoded-lf-%0a",
            "ns-or-et-or-e--with-encoded-lf-%0A",
            "ns-or-et-or-e--with-cr-\r",
            "ns-or-et-or-e--with-encoded-cr-%0d",
            "ns-or-et-or-e--with-encoded-cr-%0D",
            "ns-or-et-or-e--with-null-\0",
            "ns-or-et-or-e--with-encoded-null-%00",
            "ns-or-et-or-e-with-modulo-%"
        );
    }

    public static Stream<String> invalidSecretPathArguments() {
        return Stream.of(
            "secret-with-semicolon;",
            "secret-with-encoded-semicolon%3B",
            "secret-with-encoded-semicolon%3b",
            "secret-%2f-with-encoded-slash",
            "secret-%2F-with-encoded-slash",
            "secret--with-backslash-\\",
            "secret--with-encoded-backslash-%5c",
            "secret--with-encoded-backslash-%5C",
            "secret--with-encoded-lf-%0a",
            "secret--with-encoded-lf-%0A",
            "secret--with-encoded-cr-%0d",
            "secret--with-encoded-cr-%0D",
            "secret--with-encoded-null-%00",
            "secret-with-encoded-modulo-%25"
        ).flatMap(
            invalidSecretPathElement -> Stream.of(
                // Invalid single-element secret-path.
                invalidSecretPathElement,
                // Two-element path with invalid leaf.
                "root/" + invalidSecretPathElement,
                // Two-element path with invalid root.
                invalidSecretPathElement + "/leaf",
                // Multi-element path with an invalid element in the middle.
                "root/" + invalidSecretPathElement + "/leaf"
            )
        );
    }

    public static Stream<String> validNamespaceOrEntityTypeOrEntityNameArguments() {
        return Stream.of(
            "ns-or-et-or-e-with-colon:",
            "ns-or-et-or-e_with_underscores",
            "ns.or.et.or.e.with.period.chars",
            "nsoretorewithalphanumericcharsonly123"
        );
    }

    public static String buildUrl(String urlPath) {
        return Strings.CS.removeEnd(UriComponentsBuilder.fromPath(urlPath)
                .encode()
                .toUriString(), "/");
    }
}
