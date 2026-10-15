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
package com.nvidia.nvcf.service.ratelimit;

import com.nvidia.nvcf.configuration.ratelimit.AccountRateLimiterProperties;
import com.nvidia.nvcf.configuration.ratelimit.FunctionRateLimiterProperties;
import jakarta.annotation.Nullable;
import java.util.Optional;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class RateLimiterService {

    private final AccountRateLimiterProperties accountProperties;
    private final FunctionRateLimiterProperties functionProperties;
    private final BucketRateLimiter bucketRateLimiter;
    private final LegacyRateLimiter legacyRateLimiter;


    RateLimiterService(AccountRateLimiterProperties accountProperties,
                       FunctionRateLimiterProperties functionProperties) {
        this.accountProperties = accountProperties;
        this.functionProperties = functionProperties;
        bucketRateLimiter = new BucketRateLimiter();
        legacyRateLimiter = new LegacyRateLimiter();
    }

    public void verifyLimits(String ncaId) {
        var defaultCappingProps = accountProperties.getDefaultRateCappingProperties();
        var rateCappingProps = accountProperties.getOverridesMap()
                .getOrDefault(ncaId, defaultCappingProps);
        var policy = rateCappingProps.getPolicy();
        var invocationsPerSecond = rateCappingProps.getAllowedInvocationsPerSecond();

        switch (policy) {
            case LEGACY -> legacyRateLimiter.accountLimit(ncaId, invocationsPerSecond);
            case BUCKET -> bucketRateLimiter.accountLimit(ncaId, invocationsPerSecond);
            default -> throw new IllegalStateException("Invalid policy: " + policy);
        }
    }

    /**
     * Perform rate limit verification of both nca id and function id.
     *
     * @param ncaId      non-null account id
     * @param functionId non-null function id
     */
    public void verifyLimits(String ncaId, UUID functionId) {
        verifyLimits(ncaId);
        verifyFunctionLimits(functionId);
    }

    /**
     * Perform rate limit verification of nca id and either version id or function id.
     * Function id is chosen only if version id is null.
     *
     * @param ncaId      non-null account id
     * @param functionId non-null function id
     * @param versionId  nullable version id
     */
    public void verifyLimits(String ncaId, UUID functionId, @Nullable UUID versionId) {
        verifyLimits(ncaId);
        Optional.ofNullable(versionId).ifPresentOrElse(
                v -> verifyFunctionVersionLimits(functionId, versionId),
                () -> verifyFunctionLimits(functionId));
    }

    private void verifyFunctionLimits(UUID functionId) {
        var defaultCappingProps = functionProperties.getDefaultRateCappingProperties();
        var rateCappingProps = functionProperties.getFunctionOverridesMap()
                .getOrDefault(functionId, defaultCappingProps);
        var policy = rateCappingProps.getPolicy();
        var invocationsPerSecond = rateCappingProps.getAllowedInvocationsPerSecond();

        switch (rateCappingProps.getPolicy()) {
            case LEGACY -> legacyRateLimiter.functionLimit(functionId, invocationsPerSecond);
            case BUCKET -> bucketRateLimiter.functionLimit(functionId, invocationsPerSecond);
            default -> throw new IllegalStateException("Invalid policy: " + policy);
        }
    }

    private void verifyFunctionVersionLimits(UUID functionId, UUID functionVersionId) {
        var defaultCappingProps = functionProperties.getDefaultRateCappingProperties();
        var funcCappingProps = functionProperties.getFunctionOverridesMap()
                .getOrDefault(functionId, defaultCappingProps);
        var rateCappingProps = functionProperties.getVersionOverridesMap()
                .getOrDefault(functionVersionId, funcCappingProps);
        var policy = rateCappingProps.getPolicy();
        var invocationsPerSecond = rateCappingProps.getAllowedInvocationsPerSecond();

        switch (rateCappingProps.getPolicy()) {
            case LEGACY -> legacyRateLimiter.functionVersionLimit(functionId,
                                                                  functionVersionId,
                                                                  invocationsPerSecond);
            case BUCKET -> bucketRateLimiter.functionVersionLimit(functionId,
                                                                  functionVersionId,
                                                                  invocationsPerSecond);
            default -> throw new IllegalStateException("Invalid policy: " + policy);
        }
    }
}
