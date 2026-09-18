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

import com.nvidia.boot.exceptions.BadRequestException;
import jakarta.annotation.Nullable;
import java.nio.charset.StandardCharsets;
import java.util.HashSet;
import java.util.Set;
import java.util.regex.Pattern;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

/**
 * Validates the syntax of {@code llmConfig.routingMethod} at create/update: a method name
 * optionally followed by {@code ;key=value} parameters. Methods and parameters are not
 * interpreted here; the router owns their semantics and the value is stored as received.
 */
@Slf4j
public final class LlmRoutingMethodValidator {

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
    private static final Pattern CONTROL_CHARACTERS =
            Pattern.compile("[\\p{Cntrl}\\p{Zl}\\p{Zp}]");

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

    private LlmRoutingMethodValidator() {}

    /**
     * Rejects a routingMethod whose syntax the router could not parse and returns the value to
     * store: the input without outer spaces, so validated and stored bytes are identical. Any
     * other outer character, including tabs and line breaks, fails the grammar.
     */
    @Nullable
    public static String validate(String modelName, @Nullable String routingMethod) {
        if (routingMethod == null) {
            return null;
        }
        var value = StringUtils.strip(routingMethod, " ");
        if (value.isEmpty()) {
            return value;
        }
        if (value.getBytes(StandardCharsets.UTF_8).length > MAX_EXPRESSION_BYTES) {
            reject(modelName, MESG_EXPRESSION_TOO_LONG);
        }
        if (value.contains(",")) {
            reject(modelName, MESG_COMMAS_NOT_ALLOWED);
        }
        var segments = value.split(";", -1);
        if (!METHOD_PATTERN.matcher(segments[0]).matches()) {
            reject(modelName, MESG_INVALID_METHOD_NAME);
        }
        if (segments.length - 1 > MAX_PARAMETERS) {
            reject(modelName, MESG_TOO_MANY_PARAMETERS);
        }
        var keys = new HashSet<String>();
        for (var index = 1; index < segments.length; index++) {
            validateParameter(modelName, segments[index], keys);
        }
        return value;
    }

    private static void validateParameter(String modelName, String segment, Set<String> keys) {
        var parameter = PARAMETER_PATTERN.matcher(segment);
        if (!parameter.matches()) {
            reject(modelName, MESG_INVALID_PARAMETER.formatted(withoutControlCharacters(segment)));
        }
        var key = parameter.group(1);
        if (!isValidBareValue(parameter.group(2))) {
            reject(modelName, MESG_INVALID_VALUE.formatted(key));
        }
        if (!keys.add(key)) {
            reject(modelName, MESG_DUPLICATE_PARAMETER.formatted(key));
        }
    }

    // The segment is raw request text echoed in the log and the 400 body; a line break in it
    // could forge a log line.
    private static String withoutControlCharacters(String segment) {
        return CONTROL_CHARACTERS.matcher(segment).replaceAll("?");
    }

    private static boolean isValidBareValue(String value) {
        return INTEGER_PATTERN.matcher(value).matches()
                || DECIMAL_PATTERN.matcher(value).matches()
                || TOKEN_PATTERN.matcher(value).matches()
                || STRING_PATTERN.matcher(value).matches();
    }

    private static void reject(String modelName, String rule) {
        var mesg = MESG_INVALID_ROUTING_METHOD.formatted(modelName, rule);
        log.error(mesg);
        throw new BadRequestException(mesg);
    }
}
