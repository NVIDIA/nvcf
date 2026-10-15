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

package com.nvidia.boot.registries.service.registry.client.ngc;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.exceptions.UpstreamException;
import java.util.Arrays;
import java.util.List;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class AuthenticateChallengeUtilsTest {

    private static final String NGC_REALM = "https://stg.nvcr.io/proxy_auth";
    private static final String REALM = "https://auth.example.com/token";

    @ParameterizedTest
    @MethodSource("createParseableChallenges")
    void parse_ValidBearerChallenge_ExtractsParams(String headerValue,
                                                   String expectedRealm,
                                                   String expectedService,
                                                   String expectedScope) {
        var challenge = AuthenticateChallengeUtils.parse(List.of(headerValue));

        assertThat(challenge.realm()).isEqualTo(expectedRealm);
        assertThat(challenge.service()).isEqualTo(expectedService);
        assertThat(challenge.scope()).isEqualTo(expectedScope);
    }

    private static Stream<Arguments> createParseableChallenges() {
        return Stream.of(
                // All params quoted.
                Arguments.of("Bearer realm=\"" + REALM + "\",service=\"registry.example.com\","
                                     + "scope=\"repository:org/image:pull\"",
                             REALM, "registry.example.com", "repository:org/image:pull"),
                // Unquoted values.
                Arguments.of("Bearer realm=" + REALM + ",service=registry.example.com",
                             REALM, "registry.example.com", null),
                // Params in shuffled order.
                Arguments.of("Bearer scope=\"repository:org/image:pull\",realm=\"" + REALM + "\","
                                     + "service=\"registry.example.com\"",
                             REALM, "registry.example.com", "repository:org/image:pull"),
                // Whitespace around delimiters.
                Arguments.of("Bearer realm = \"" + REALM + "\" , scope = \"s\"",
                             REALM, null, "s"),
                // Trailing comma.
                Arguments.of("Bearer realm=\"" + REALM + "\",service=\"reg\",",
                             REALM, "reg", null),
                // Scope absent.
                Arguments.of("Bearer realm=\"" + REALM + "\",service=\"reg\"",
                             REALM, "reg", null),
                // Empty scope is treated as absent.
                Arguments.of("Bearer realm=\"" + REALM + "\",scope=\"\"",
                             REALM, null, null),
                // Empty service is treated as absent.
                Arguments.of("Bearer realm=\"" + REALM + "\",service=\"\"",
                             REALM, null, null),
                // Multi-action scope keeps the comma inside quotes.
                Arguments.of("Bearer realm=\"" + REALM + "\",scope=\"repository:org/image:pull,push\"",
                             REALM, null, "repository:org/image:pull,push"),
                // Escaped quote inside a quoted value.
                Arguments.of("Bearer realm=\"" + REALM + "\",scope=\"a\\\"b\"",
                             REALM, null, "a\"b"),
                // Lowercase scheme.
                Arguments.of("bearer realm=\"" + REALM + "\"", REALM, null, null),
                // Uppercase scheme.
                Arguments.of("BEARER realm=\"" + REALM + "\"", REALM, null, null),
                // Mixed-case param names.
                Arguments.of("Bearer REALM=\"" + REALM + "\",Service=\"reg\",SCOPE=\"s\"",
                             REALM, "reg", "s"),
                // A duplicate param keeps the first occurrence.
                Arguments.of("Bearer realm=\"" + REALM + "\",realm=\"https://other.example.com\"",
                             REALM, null, null),
                // A Basic challenge precedes the Bearer.
                Arguments.of("Basic realm=\"basic-zone\", Bearer realm=\"" + REALM + "\",scope=\"s\"",
                             REALM, null, "s"),
                // A Basic challenge follows the Bearer without leaking its params.
                Arguments.of("Bearer realm=\"" + REALM + "\", Basic realm=\"basic-zone\"",
                             REALM, null, null),
                // A bare Basic scheme precedes the Bearer.
                Arguments.of("Basic, Bearer realm=\"" + REALM + "\"", REALM, null, null),
                // The first Bearer challenge lacking a realm is skipped.
                Arguments.of("Bearer scope=\"s\", Bearer realm=\"" + REALM + "\"",
                             REALM, null, null),
                // A foreign realm is passed through untouched.
                Arguments.of("Bearer realm=\"https://foreign-auth.example.com/token\"",
                             "https://foreign-auth.example.com/token", null, null),
                // Shapes captured from stg.nvcr.io during the P0 spike: the manifest challenge
                // has no service, and the /v2/ challenge has an empty scope.
                Arguments.of("Bearer realm=\"" + NGC_REALM + "\",scope=\"repository:org/image:pull\"",
                             NGC_REALM, null, "repository:org/image:pull"),
                Arguments.of("Bearer realm=\"" + NGC_REALM + "\",scope=\"\"",
                             NGC_REALM, null, null));
    }

    @Test
    void parse_BearerInLaterHeaderValue_SelectsIt() {
        var challenge = AuthenticateChallengeUtils.parse(List.of(
                "Basic realm=\"basic-zone\"",
                "Bearer realm=\"" + REALM + "\",service=\"reg\"",
                "Digest realm=\"digest-zone\""));

        assertThat(challenge.realm()).isEqualTo(REALM);
        assertThat(challenge.service()).isEqualTo("reg");
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "Basic realm=\"basic-only\"",
            "Bearer",
            "Bearer service=\"reg\",scope=\"repository:org/image:pull\"",
            "Bearer realm=\"\"",
            "Bearer realm=\"   \"",
            "complete garbage text with no challenge"
    })
    void parse_NoUsableBearerChallenge_ThrowsCarryingRawHeader(String headerValue) {
        // The probe carries no credential, so an unusable challenge is the registry speaking
        // broken protocol - an upstream failure, never a credential rejection.
        assertThatThrownBy(() -> AuthenticateChallengeUtils.parse(List.of(headerValue)))
                .isInstanceOf(UpstreamException.class)
                .hasMessageContaining(headerValue);
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = { "   " })
    void parse_BlankHeaderValue_Throws(String headerValue) {
        var headerValues = Arrays.asList(headerValue);

        assertThatThrownBy(() -> AuthenticateChallengeUtils.parse(headerValues))
                .isInstanceOf(UpstreamException.class);
    }

    @ParameterizedTest
    @NullAndEmptySource
    void parse_NoHeaderValues_Throws(List<String> headerValues) {
        assertThatThrownBy(() -> AuthenticateChallengeUtils.parse(headerValues))
                .isInstanceOf(UpstreamException.class);
    }
}
