/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.rest.function.management.dto;

import com.nvidia.boot.exceptions.BadRequestException;
import jakarta.annotation.Nullable;
import java.nio.charset.StandardCharsets;
import java.util.HashSet;
import java.util.Set;
import java.util.regex.Pattern;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

/**
 * Validates routing expression syntax and token rate limits.
 * Routing semantics belong to the router.
 */
@Slf4j
public final class LlmConfigValidator {

    private static final int MAX_EXPRESSION_BYTES = 1024;
    private static final int MAX_PARAMETERS = 32;
    private static final Pattern METHOD_PATTERN = Pattern.compile("[A-Za-z][A-Za-z0-9_-]*");
    private static final Pattern PARAMETER_PATTERN =
            Pattern.compile(" *([a-z][a-z0-9_]*)=(\\S(?:.*\\S)?)");
    private static final Pattern INTEGER_PATTERN = Pattern.compile("-?[0-9]{1,15}");
    private static final Pattern DECIMAL_PATTERN = Pattern.compile("-?[0-9]{1,12}\\.[0-9]{1,3}");
    private static final Pattern TOKEN_PATTERN =
            Pattern.compile("[A-Za-z*][A-Za-z0-9!#$%&'*+.^_`|~:/-]*");
    private static final Pattern STRING_PATTERN = Pattern.compile(
            "\"(?:[\\x20\\x21\\x23-\\x2b\\x2d-\\x3a\\x3c-\\x5b\\x5d-\\x7e]|\\\\[\"\\\\])*\"");

    // Comma-separated '<positiveInteger>-<unit>' entries, no unit repeated.
    private static final Pattern TOKEN_RATE_LIMIT_PATTERN = Pattern.compile(
            "^(?!.*-([SMHDW]).*-\\1)[1-9]\\d*-[SMHDW](,\\s*[1-9]\\d*-[SMHDW])*$");

    private static final String MESG_INVALID_ROUTING_METHOD =
            "Invalid request: 'llmConfig.routingMethod' for model '%s' is invalid: %s";
    private static final String MESG_EXPRESSION_TOO_LONG = "expression exceeds 1024 bytes";
    private static final String MESG_COMMAS_NOT_ALLOWED = "commas are not allowed";
    private static final String MESG_INVALID_METHOD_NAME =
            "method name must match [A-Za-z][A-Za-z0-9_-]*";
    private static final String MESG_TOO_MANY_PARAMETERS = "at most 32 parameters are allowed";
    private static final String MESG_INVALID_PARAMETER =
            "parameter '%s' must be key=value with key matching [a-z][a-z0-9_]*";
    private static final String MESG_INVALID_VALUE =
            "value for '%s' must be an integer, decimal, token, or quoted string";
    private static final String MESG_DUPLICATE_PARAMETER = "duplicate parameter '%s'";
    private static final String MESG_INVALID_TOKEN_RATE_LIMIT =
            "Invalid request: 'llmConfig.tokenRateLimit' for model '%s' is invalid; expected "
                    + "comma-separated '<positiveInteger>-<unit>' entries with unit in [S, M, H, D, W] "
                    + "(for example '100000-S' or '10-M,5-S')";

    private LlmConfigValidator() {}

    /** Validates the routing expression profile without interpreting methods or parameters. */
    public static void validateRoutingMethod(String modelName, @Nullable String routingMethod) {
        if (StringUtils.isBlank(routingMethod)) {
            return;
        }
        var value = routingMethod.trim();
        if (value.getBytes(StandardCharsets.UTF_8).length > MAX_EXPRESSION_BYTES) {
            rejectRoutingMethod(modelName, MESG_EXPRESSION_TOO_LONG);
        }
        if (value.contains(",")) {
            rejectRoutingMethod(modelName, MESG_COMMAS_NOT_ALLOWED);
        }
        var segments = value.split(";", -1);
        if (!METHOD_PATTERN.matcher(segments[0]).matches()) {
            rejectRoutingMethod(modelName, MESG_INVALID_METHOD_NAME);
        }
        if (segments.length - 1 > MAX_PARAMETERS) {
            rejectRoutingMethod(modelName, MESG_TOO_MANY_PARAMETERS);
        }
        var keys = new HashSet<String>();
        for (var index = 1; index < segments.length; index++) {
            validateParameter(modelName, segments[index], keys);
        }
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

    private static void validateParameter(String modelName, String segment, Set<String> keys) {
        var parameter = PARAMETER_PATTERN.matcher(segment);
        if (!parameter.matches()) {
            rejectRoutingMethod(modelName, MESG_INVALID_PARAMETER.formatted(segment));
        }
        var key = parameter.group(1);
        if (!isValidBareValue(parameter.group(2))) {
            rejectRoutingMethod(modelName, MESG_INVALID_VALUE.formatted(key));
        }
        if (!keys.add(key)) {
            rejectRoutingMethod(modelName, MESG_DUPLICATE_PARAMETER.formatted(key));
        }
    }

    private static boolean isValidBareValue(String value) {
        return INTEGER_PATTERN.matcher(value).matches()
                || DECIMAL_PATTERN.matcher(value).matches()
                || TOKEN_PATTERN.matcher(value).matches()
                || STRING_PATTERN.matcher(value).matches();
    }

    private static void rejectRoutingMethod(String modelName, String rule) {
        var mesg = MESG_INVALID_ROUTING_METHOD.formatted(modelName, rule);
        log.error(mesg);
        throw new BadRequestException(mesg);
    }
}
