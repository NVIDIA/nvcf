/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.rest.function.management.dto;

import com.nvidia.boot.exceptions.BadRequestException;
import jakarta.annotation.Nullable;
import java.util.Locale;
import java.util.Set;
import java.util.regex.Pattern;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

/**
 * Rejects invalid {@code llmConfig} routingMethod/tokenRateLimit at create/update, so callers
 * get a 400 up front instead of a late failure at invocation.
 */
@Slf4j
public final class LlmConfigValidator {

    private LlmConfigValidator() {}

    // Stargate LoadBalancerAlgorithm values; keep in sync. Blank = router default.
    private static final Set<String> VALID_ROUTING_METHODS = Set.of(
            "power-of-two",
            "wait-and-widen",
            "round-robin",
            "random",
            "pulsar",
            "pulsar-wait-and-widen",
            // Deprecated Stargate aliases retained for existing deployments.
            "groq-multiregion",
            "pulsar-multiregion");

    // Comma-separated '<positiveInteger>-<unit>' entries, no unit repeated. "M(?!O)" keeps
    // Minute and Month disjoint in both directions: the initial capture won't let "M" match
    // the first letter of a later "MO", and the trailing (?!O) on the backreference stops a
    // duplicate-check false positive where an earlier "M" backreferences into a later "MO"'s
    // leading "M".
    private static final Pattern TOKEN_RATE_LIMIT_PATTERN = Pattern.compile(
            "^(?!.*-(MO|M(?!O)|S|H|D|W).*-\\1(?!O))"
                    + "[1-9]\\d*-(?:MO|M(?!O)|S|H|D|W)"
                    + "(,\\s*[1-9]\\d*-(?:MO|M(?!O)|S|H|D|W))*$");

    private static final String MESG_INVALID_ROUTING_METHOD =
            "Invalid request: 'llmConfig.routingMethod' for model '%s' is invalid; supported "
                    + "values are [power-of-two, wait-and-widen, round-robin, random, pulsar, "
                    + "pulsar-wait-and-widen, groq-multiregion, pulsar-multiregion]";
    private static final String MESG_INVALID_TOKEN_RATE_LIMIT =
            "Invalid request: 'llmConfig.%s' for model '%s' is invalid; expected "
                    + "comma-separated '<positiveInteger>-<unit>' entries with unit in "
                    + "[S, M, H, D, W, MO] (for example '100000-S' or '10-M,5-S')";

    /** Rejects a routingMethod that is not one of the supported router algorithms. */
    public static void validateRoutingMethod(String modelName, @Nullable String routingMethod) {
        if (StringUtils.isBlank(routingMethod)) {
            return;
        }
        // Match the router: lowercase, '_' -> '-'.
        var normalized = routingMethod.trim().toLowerCase(Locale.ROOT).replace('_', '-');
        if (!VALID_ROUTING_METHODS.contains(normalized)) {
            var mesg = MESG_INVALID_ROUTING_METHOD.formatted(modelName);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    /** Rejects a tokenRateLimit that is not '<positiveInteger>-<unit>' fragments. */
    public static void validateTokenRateLimit(String modelName, @Nullable String tokenRateLimit) {
        validateTokenRateLimitField(modelName, "tokenRateLimit", tokenRateLimit);
    }

    /** Rejects an inputTokenRateLimit that is not '<positiveInteger>-<unit>' fragments. */
    public static void validateInputTokenRateLimit(
            String modelName, @Nullable String inputTokenRateLimit) {
        validateTokenRateLimitField(modelName, "inputTokenRateLimit", inputTokenRateLimit);
    }

    /** Rejects an outputTokenRateLimit that is not '<positiveInteger>-<unit>' fragments. */
    public static void validateOutputTokenRateLimit(
            String modelName, @Nullable String outputTokenRateLimit) {
        validateTokenRateLimitField(modelName, "outputTokenRateLimit", outputTokenRateLimit);
    }

    private static void validateTokenRateLimitField(
            String modelName, String fieldName, @Nullable String value) {
        if (StringUtils.isBlank(value)) {
            return;
        }
        if (!TOKEN_RATE_LIMIT_PATTERN.matcher(value).matches()) {
            var mesg = MESG_INVALID_TOKEN_RATE_LIMIT.formatted(fieldName, modelName);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }
}
