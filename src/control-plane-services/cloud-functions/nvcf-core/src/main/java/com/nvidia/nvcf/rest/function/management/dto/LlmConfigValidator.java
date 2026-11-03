/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.rest.function.management.dto;

import com.nvidia.boot.exceptions.BadRequestException;
import jakarta.annotation.Nullable;
import java.util.HashSet;
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

    // One '<positiveInteger>-<unit>' entry; whitespace is allowed only after a comma.
    private static final Pattern TOKEN_RATE_LIMIT_ENTRY = Pattern.compile(
            "([1-9]\\d*)-(MO|S|M|H|D|W)");

    private static final String MESG_INVALID_TOKEN_RATE_LIMIT =
            "Invalid request: 'llmConfig.tokenRateLimit' for model '%s' is invalid; expected "
                    + "comma-separated '<positiveInteger>-<unit>' entries with unit in "
                    + "[S, M, H, D, W, MO] (for example '100000-S' or '10-M,5-S')";

    /** Rejects a tokenRateLimit that is not '<positiveInteger>-<unit>' fragments. */
    public static void validateTokenRateLimit(String modelName, @Nullable String tokenRateLimit) {
        if (StringUtils.isBlank(tokenRateLimit)) {
            return;
        }
        if (!isValidTokenRateLimit(tokenRateLimit)) {
            var mesg = MESG_INVALID_TOKEN_RATE_LIMIT.formatted(modelName);
            log.warn(mesg);
            throw new BadRequestException(mesg);
        }
    }

    // Parsed entry by entry rather than with one regex so validation stays linear in length.
    public static boolean isValidTokenRateLimit(String tokenRateLimit) {
        var entries = tokenRateLimit.split(",", -1);
        var units = new HashSet<String>();
        for (int i = 0; i < entries.length; i++) {
            var entry = i == 0 ? entries[i] : entries[i].stripLeading();
            var matcher = TOKEN_RATE_LIMIT_ENTRY.matcher(entry);
            if (!matcher.matches() || !units.add(matcher.group(2))) {
                return false;
            }
            try {
                Long.parseLong(matcher.group(1));
            } catch (NumberFormatException e) {
                return false;
            }
        }
        return true;
    }
}
