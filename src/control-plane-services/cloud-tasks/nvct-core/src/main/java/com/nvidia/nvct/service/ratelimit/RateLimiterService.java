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
package com.nvidia.nvct.service.ratelimit;

import com.nvidia.nvct.configuration.ratelimit.AccountRateLimiterProperties;
import com.nvidia.nvct.configuration.ratelimit.TaskRateLimiterProperties;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class RateLimiterService {

    private final AccountRateLimiterProperties accountProperties;
    private final TaskRateLimiterProperties taskProperties;
    private final BucketRateLimiter bucketRateLimiter;

    RateLimiterService(AccountRateLimiterProperties accountProperties,
                       TaskRateLimiterProperties taskProperties) {
        this.accountProperties = accountProperties;
        this.taskProperties = taskProperties;
        bucketRateLimiter = new BucketRateLimiter();
    }

    public void verifyLimits(String ncaId, UUID taskId) {
        verifyLimits(ncaId);
        verifyLimits(taskId);
    }

    public void verifyLimits(String ncaId) {
        var defaultCappingProps = accountProperties.getDefaultRateCappingProperties();
        var rateCappingProps = accountProperties.getOverridesMap()
                .getOrDefault(ncaId, defaultCappingProps);
        var invocationsPerSecond = rateCappingProps.getAllowedInvocationsPerSecond();
        bucketRateLimiter.accountLimit(ncaId, invocationsPerSecond);
    }

    public void verifyLimits(UUID taskId) {
        var defaultCappingProps = taskProperties.getDefaultRateCappingProperties();
        var rateCappingProps = taskProperties.getTaskOverridesMap()
                .getOrDefault(taskId, defaultCappingProps);
        var invocationsPerSecond = rateCappingProps.getAllowedInvocationsPerSecond();
        bucketRateLimiter.taskLimit(taskId, invocationsPerSecond);
    }
}
