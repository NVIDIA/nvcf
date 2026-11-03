/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.rest.function.management.dto;

import com.nvidia.boot.exceptions.BadRequestException;
import jakarta.annotation.Nullable;
import java.util.regex.Pattern;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

/**
 * Rejects an invalid {@code llmConfig} tokenRateLimit at create/update, so callers get a 400 up
 * front instead of a late failure at invocation.
 */
@Slf4j
public final class LlmConfigValidator {

    private LlmConfigValidator() {}

    // Comma-separated '<positiveInteger>-<unit>' entries, no unit repeated. "M(?!O)" keeps
    // Minute and Month disjoint in both directions: the initial capture won't let "M" match
    // the first letter of a later "MO", and the trailing (?!O) on the backreference stops a
    // duplicate-check false positive where an earlier "M" backreferences into a later "MO"'s
    // leading "M".
    private static final Pattern TOKEN_RATE_LIMIT_PATTERN = Pattern.compile(
            "^(?!.*-(MO|M(?!O)|S|H|D|W).*-\\1(?!O))"
                    + "[1-9]\\d*-(?:MO|M(?!O)|S|H|D|W)"
                    + "(,\\s*[1-9]\\d*-(?:MO|M(?!O)|S|H|D|W))*$");

    private static final String MESG_INVALID_TOKEN_RATE_LIMIT =
            "Invalid request: 'llmConfig.tokenRateLimit' for model '%s' is invalid; expected "
                    + "comma-separated '<positiveInteger>-<unit>' entries with unit in "
                    + "[S, M, H, D, W, MO] (for example '100000-S' or '10-M,5-S')";

    /** Rejects a tokenRateLimit that is not '<positiveInteger>-<unit>' fragments. */
    public static void validateTokenRateLimit(String modelName, @Nullable String tokenRateLimit) {
        if (StringUtils.isBlank(tokenRateLimit)) {
            return;
        }
        if (!TOKEN_RATE_LIMIT_PATTERN.matcher(tokenRateLimit).matches()) {
            var mesg = MESG_INVALID_TOKEN_RATE_LIMIT.formatted(modelName);
            log.warn(mesg);
            throw new BadRequestException(mesg);
        }
    }
}
