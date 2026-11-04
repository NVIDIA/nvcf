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

import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.registries.service.registry.client.ngc.dto.WwwAuthenticateChallenge;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

/**
 * Parses {@link WwwAuthenticateChallenge} out of a 401 response's {@code WWW-Authenticate}
 * headers, per RFC 7235 and the Docker registry token authentication specification.
 */
@Slf4j
@UtilityClass
class AuthenticateChallengeUtils {

    private static final String BEARER_SCHEME = "Bearer";
    private static final String REALM_PARAM = "realm";
    private static final String SERVICE_PARAM = "service";
    private static final String SCOPE_PARAM = "scope";

    private static final String MESG_NO_BEARER_CHALLENGE =
            "Registry returned 401 without a usable Bearer challenge. WWW-Authenticate: %s";

    /**
     * Parses the Bearer challenge out of all {@code WWW-Authenticate} header values of a 401
     * response.
     *
     * <p>Handles several challenges within one header value and spread across several header
     * values, quoted and unquoted parameters in any order, commas inside quoted strings (such
     * as the multi-action scope {@code repository:org/image:pull,push}), and backslash-escaped
     * characters. Scheme and parameter names are matched case-insensitively; a repeated
     * parameter keeps its first occurrence.
     *
     * @throws UpstreamException if no Bearer challenge carrying a non-empty realm is found -
     *                           the registry speaking broken protocol, not a credential
     *                           failure, since the probe carries no credential to reject; the
     *                           message carries the raw header values for diagnostics
     */
    static WwwAuthenticateChallenge parse(List<String> wwwAuthenticateHeaderValues) {
        if (wwwAuthenticateHeaderValues != null) {
            for (var headerValue : wwwAuthenticateHeaderValues) {
                var challenge = findBearerChallenge(headerValue);
                if (challenge != null) {
                    return challenge;
                }
            }
        }

        var mesg = MESG_NO_BEARER_CHALLENGE.formatted(wwwAuthenticateHeaderValues);
        log.error(mesg);
        throw new UpstreamException(mesg);
    }

    private static WwwAuthenticateChallenge findBearerChallenge(String headerValue) {
        if (StringUtils.isBlank(headerValue)) {
            return null;
        }

        return parseChallenges(headerValue).stream()
                .filter(candidate -> BEARER_SCHEME.equalsIgnoreCase(candidate.scheme())
                        && StringUtils.isNotBlank(candidate.params().get(REALM_PARAM)))
                .findFirst()
                .map(candidate -> new WwwAuthenticateChallenge(
                        candidate.params().get(REALM_PARAM),
                        StringUtils.defaultIfEmpty(candidate.params().get(SERVICE_PARAM), null),
                        StringUtils.defaultIfEmpty(candidate.params().get(SCOPE_PARAM), null)))
                .orElse(null);
    }

    private record RawChallenge(String scheme, Map<String, String> params) {

    }

    /**
     * Splits one header value into its challenges. Each comma-separated element is either
     * {@code scheme param=value} or a bare {@code scheme}, which starts a new challenge, or
     * {@code param=value}, which belongs to the challenge that precedes it.
     */
    private static List<RawChallenge> parseChallenges(String headerValue) {
        var challenges = new ArrayList<RawChallenge>();
        RawChallenge current = null;

        for (var element : splitOnUnquotedCommas(headerValue)) {
            var trimmed = element.trim();
            if (trimmed.isEmpty()) {
                continue;
            }

            var separator = indexOfUnquoted(trimmed, '=');
            if (separator < 0) {
                current = new RawChallenge(trimmed, new HashMap<>());
                challenges.add(current);
                continue;
            }

            var name = trimmed.substring(0, separator).trim();
            var value = unquote(trimmed.substring(separator + 1).trim());

            var schemeEnd = lastIndexOfWhitespace(name);
            if (schemeEnd >= 0) {
                current = new RawChallenge(name.substring(0, schemeEnd).trim(), new HashMap<>());
                challenges.add(current);
                name = name.substring(schemeEnd + 1).trim();
            }

            if (current != null && !name.isEmpty()) {
                current.params().putIfAbsent(name.toLowerCase(Locale.ROOT), value);
            }
        }

        return challenges;
    }

    private static List<String> splitOnUnquotedCommas(String headerValue) {
        var elements = new ArrayList<String>();
        var elementStart = 0;
        var quoted = false;

        for (var i = 0; i < headerValue.length(); i++) {
            var current = headerValue.charAt(i);
            if (quoted && current == '\\') {
                i++;
            } else if (current == '"') {
                quoted = !quoted;
            } else if (current == ',' && !quoted) {
                elements.add(headerValue.substring(elementStart, i));
                elementStart = i + 1;
            }
        }
        elements.add(headerValue.substring(elementStart));

        return elements;
    }

    private static int indexOfUnquoted(String element, char target) {
        var quoted = false;

        for (var i = 0; i < element.length(); i++) {
            var current = element.charAt(i);
            if (quoted && current == '\\') {
                i++;
            } else if (current == '"') {
                quoted = !quoted;
            } else if (current == target && !quoted) {
                return i;
            }
        }

        return -1;
    }

    private static int lastIndexOfWhitespace(String name) {
        for (var i = name.length() - 1; i >= 0; i--) {
            if (Character.isWhitespace(name.charAt(i))) {
                return i;
            }
        }

        return -1;
    }

    private static String unquote(String value) {
        if (value.length() < 2 || !value.startsWith("\"") || !value.endsWith("\"")) {
            return value;
        }

        var quoted = value.substring(1, value.length() - 1);
        var unquoted = new StringBuilder(quoted.length());
        for (var i = 0; i < quoted.length(); i++) {
            var current = quoted.charAt(i);
            if (current == '\\' && i + 1 < quoted.length()) {
                i++;
                unquoted.append(quoted.charAt(i));
            } else {
                unquoted.append(current);
            }
        }

        return unquoted.toString();
    }
}
