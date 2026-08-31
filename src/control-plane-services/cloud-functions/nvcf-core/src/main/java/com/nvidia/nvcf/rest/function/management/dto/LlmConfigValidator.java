/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.rest.function.management.dto;

import com.nvidia.boot.exceptions.BadRequestException;
import jakarta.annotation.Nullable;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Pattern;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

/**
 * Format-only validation for {@code llmConfig} fields at create/update, so callers get a 400 up
 * front instead of a late failure at invocation.
 *
 * <p>routingMethod carries a routing expression: an algorithm token optionally followed by
 * {@code ;key=value} tuning parameters (a profile of RFC 8941 Item with Parameters). This layer
 * checks format only and normalizes the algorithm token to the router's canonical spelling. The
 * router is the semantic authority: it rejects unknown methods, unknown or incompatible
 * parameters, and invalid values at request time, so a well-formed expression persists even when
 * it is semantically wrong.
 */
@Slf4j
public final class LlmConfigValidator {

    private LlmConfigValidator() {}

    private static final int MAX_ROUTING_METHOD_LENGTH = 1024;

    // Router LoadBalancerAlgorithm canonical spellings; keep this alias map in sync with the
    // router's serde aliases. Aliases are normalized away at write time so newly persisted
    // expressions converge on canonical names. Unknown methods are kept as typed.
    private static final Map<String, String> ALGORITHM_ALIASES = Map.of(
            "power-of-two", "power-of-n",
            "powerof2", "power-of-n",
            "powerofn", "power-of-n",
            "groq-multiregion", "wait-and-widen",
            "pulsar-multiregion", "pulsar-wait-and-widen");

    // RFC 8941 token: (ALPHA / "*") *(tchar / ":" / "/").
    private static final Pattern ALGORITHM_TOKEN_PATTERN =
            Pattern.compile("[A-Za-z*][!#$%&'*+.^_`|~0-9A-Za-z:/-]*");
    private static final Pattern PARAMETER_PATTERN =
            Pattern.compile("([a-z][a-z0-9_]*)=(.+)", Pattern.DOTALL);
    private static final Pattern INTEGER_VALUE_PATTERN = Pattern.compile("-?\\d{1,15}");
    private static final Pattern DECIMAL_VALUE_PATTERN = Pattern.compile("-?\\d{1,12}\\.\\d{1,3}");
    // Quoted string: printable ASCII except '"' and '\', with only \" and \\ escapes.
    private static final Pattern STRING_VALUE_PATTERN =
            Pattern.compile("\"(?:[\\x20\\x21\\x23-\\x5B\\x5D-\\x7E]|\\\\[\"\\\\])*\"");

    // Comma-separated '<positiveInteger>-<unit>' entries, no unit repeated.
    private static final Pattern TOKEN_RATE_LIMIT_PATTERN = Pattern.compile(
            "^(?!.*-([SMHDW]).*-\\1)[1-9]\\d*-[SMHDW](,\\s*[1-9]\\d*-[SMHDW])*$");

    private static final String MESG_INVALID_ROUTING_METHOD =
            "Invalid request: 'llmConfig.routingMethod' for model '%s' is invalid: %s";
    private static final String REASON_TOO_LONG =
            "expression exceeds " + MAX_ROUTING_METHOD_LENGTH + " characters";
    private static final String REASON_COMMA = "commas are not allowed in a routing expression";
    private static final String REASON_UNTERMINATED_STRING = "unterminated quoted string";
    private static final String REASON_INVALID_ALGORITHM =
            "the algorithm name before the first ';' must be a bare token";
    private static final String REASON_MALFORMED_PARAMETER =
            "malformed parameter '%s'; expected key=value with a lowercase snake_case key";
    private static final String REASON_INVALID_VALUE =
            "parameter '%s' value must be an integer, decimal, token, or quoted string";
    private static final String REASON_BOOLEAN_SYNTAX =
            "parameter '%s' must use =true or =false, not '?' boolean syntax";
    private static final String REASON_DUPLICATE_PARAMETER = "duplicate parameter '%s'";

    private static final String MESG_INVALID_TOKEN_RATE_LIMIT =
            "Invalid request: 'llmConfig.tokenRateLimit' for model '%s' is invalid; expected "
                    + "comma-separated '<positiveInteger>-<unit>' entries with unit in [S, M, H, D, W] "
                    + "(for example '100000-S' or '10-M,5-S')";

    /**
     * Validates the routing expression's format and returns it with the algorithm token
     * normalized to canonical spelling and optional whitespace after ';' removed. Blank input is
     * returned unchanged. Never checks whether the method or parameters exist or apply.
     */
    public static String validateAndNormalizeRoutingMethod(
            String modelName, @Nullable String routingMethod) {
        if (StringUtils.isBlank(routingMethod)) {
            return routingMethod;
        }
        var raw = routingMethod.trim();
        if (raw.length() > MAX_ROUTING_METHOD_LENGTH) {
            throw invalidRoutingMethod(modelName, REASON_TOO_LONG);
        }
        if (raw.indexOf(',') >= 0) {
            throw invalidRoutingMethod(modelName, REASON_COMMA);
        }
        var segments = splitOutsideStrings(modelName, raw);
        var algorithm = segments.get(0);
        if (!ALGORITHM_TOKEN_PATTERN.matcher(algorithm).matches()) {
            throw invalidRoutingMethod(modelName, REASON_INVALID_ALGORITHM);
        }
        var normalized = new StringBuilder(normalizeAlgorithm(algorithm));
        var seenKeys = new HashSet<String>();
        for (var segment : segments.subList(1, segments.size())) {
            // RFC 8941 allows spaces after ';' only; anything else is part of the parameter.
            var parameter = StringUtils.stripStart(segment, " ");
            var matcher = PARAMETER_PATTERN.matcher(parameter);
            if (!matcher.matches()) {
                throw invalidRoutingMethod(
                        modelName, REASON_MALFORMED_PARAMETER.formatted(parameter));
            }
            var key = matcher.group(1);
            var value = matcher.group(2);
            if (!seenKeys.add(key)) {
                throw invalidRoutingMethod(modelName, REASON_DUPLICATE_PARAMETER.formatted(key));
            }
            validateParameterValue(modelName, key, value);
            normalized.append(';').append(key).append('=').append(value);
        }
        return normalized.toString();
    }

    /** Rejects a tokenRateLimit that is not '<positiveInteger>-<unit>' fragments. */
    public static void validateTokenRateLimit(String modelName, @Nullable String tokenRateLimit) {
        if (StringUtils.isBlank(tokenRateLimit)) {
            return;
        }
        if (!TOKEN_RATE_LIMIT_PATTERN.matcher(tokenRateLimit).matches()) {
            var mesg = MESG_INVALID_TOKEN_RATE_LIMIT.formatted(modelName);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    /** Splits on top-level ';' only; a ';' inside a quoted string stays in its segment. */
    private static List<String> splitOutsideStrings(String modelName, String raw) {
        var segments = new ArrayList<String>();
        var current = new StringBuilder();
        var inString = false;
        for (var i = 0; i < raw.length(); i++) {
            var c = raw.charAt(i);
            if (inString) {
                current.append(c);
                if (c == '\\' && i + 1 < raw.length()) {
                    current.append(raw.charAt(++i));
                } else if (c == '"') {
                    inString = false;
                }
            } else if (c == ';') {
                segments.add(current.toString());
                current.setLength(0);
            } else {
                if (c == '"') {
                    inString = true;
                }
                current.append(c);
            }
        }
        if (inString) {
            throw invalidRoutingMethod(modelName, REASON_UNTERMINATED_STRING);
        }
        segments.add(current.toString());
        return segments;
    }

    private static void validateParameterValue(String modelName, String key, String value) {
        if (value.charAt(0) == '?') {
            throw invalidRoutingMethod(modelName, REASON_BOOLEAN_SYNTAX.formatted(key));
        }
        var isValidValue = STRING_VALUE_PATTERN.matcher(value).matches()
                || INTEGER_VALUE_PATTERN.matcher(value).matches()
                || DECIMAL_VALUE_PATTERN.matcher(value).matches()
                || ALGORITHM_TOKEN_PATTERN.matcher(value).matches();
        if (!isValidValue) {
            throw invalidRoutingMethod(modelName, REASON_INVALID_VALUE.formatted(key));
        }
    }

    private static String normalizeAlgorithm(String algorithm) {
        var lowered = algorithm.toLowerCase(Locale.ROOT).replace('_', '-');
        return ALGORITHM_ALIASES.getOrDefault(lowered, lowered);
    }

    private static BadRequestException invalidRoutingMethod(String modelName, String reason) {
        var mesg = MESG_INVALID_ROUTING_METHOD.formatted(modelName, reason);
        log.error(mesg);
        return new BadRequestException(mesg);
    }
}
