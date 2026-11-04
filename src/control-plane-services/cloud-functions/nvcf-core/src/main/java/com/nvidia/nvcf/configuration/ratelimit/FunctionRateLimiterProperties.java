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
package com.nvidia.nvcf.configuration.ratelimit;

import jakarta.annotation.PostConstruct;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import lombok.AccessLevel;
import lombok.Builder;
import lombok.Data;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;
import org.springframework.util.CollectionUtils;

@Slf4j
@Data
@Configuration
@RefreshScope
@ConfigurationProperties(prefix = "nvcf.rate-limiters.function-rate-limiter")
public class FunctionRateLimiterProperties {
    public static final UUID VERSION_WILDCARD = new UUID(0, 0);

    private static final String MESG_INVALID_OVERRIDES_ENTRY =
            "Invalid or duplicate entry in the function-rate-limiter's overrides config";

    private RateLimiterPolicy policy = RateLimiterPolicy.LEGACY;
    private long allowedInvocationsPerSecond = 100;
    private List<FunctionCappingProperties> overrides;

    // Only contains override entries that have just the functionId and no functionVersionId.
    @Setter(AccessLevel.NONE)
    private Map<UUID, FunctionCappingProperties> functionOverridesMap = Collections.emptyMap();

    // Only contains override entries that have functionVersionId. functionId is ignored.
    @Setter(AccessLevel.NONE)
    private Map<UUID, FunctionCappingProperties> versionOverridesMap = Collections.emptyMap();

    @Setter(AccessLevel.NONE)
    private FunctionCappingProperties defaultRateCappingProperties;

    @Builder
    @Data
    public static class FunctionCappingProperties {
        private UUID functionId;
        private UUID functionVersionId;
        private RateLimiterPolicy policy;
        private long allowedInvocationsPerSecond;
    }

    @PostConstruct
    void postConstruct() {
        if (!CollectionUtils.isEmpty(overrides)) {
            this.functionOverridesMap = overrides.stream()
                    .filter(props -> props.getFunctionId() != null)
                    .filter(props -> props.getFunctionVersionId() == null)
                    .collect(Collectors.toMap(
                            FunctionCappingProperties::getFunctionId,
                            Function.identity()));
            this.versionOverridesMap = overrides.stream()
                    .filter(props -> props.getFunctionVersionId() != null)
                    .collect(Collectors.toMap(
                            FunctionCappingProperties::getFunctionVersionId,
                            Function.identity()));

            if (functionOverridesMap.size() + versionOverridesMap.size() != overrides.size()) {
                log.warn(MESG_INVALID_OVERRIDES_ENTRY);
            }
        }

        this.defaultRateCappingProperties =
                FunctionCappingProperties.builder()
                        .policy(policy)
                        .functionId(VERSION_WILDCARD)
                        .functionVersionId(VERSION_WILDCARD)
                        .allowedInvocationsPerSecond(allowedInvocationsPerSecond)
                        .build();
    }
}
