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
package com.nvidia.nvct.configuration.ratelimit;

import jakarta.annotation.PostConstruct;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.function.Function;
import java.util.stream.Collectors;
import lombok.AccessLevel;
import lombok.Builder;
import lombok.Data;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;
import org.springframework.util.CollectionUtils;

@Slf4j
@Data
@Configuration
@RefreshScope
@ConfigurationProperties(prefix = "nvct.rate-limiters.account-rate-limiter")
public class AccountRateLimiterProperties {

    private static final String MESG_INVALID_OVERRIDES_ENTRY =
            "Invalid or duplicate account entry in the account-rate-limiter's overrides config";

    private long allowedInvocationsPerSecond = 100;
    private List<AccountRateCappingProperties> overrides;

    @Setter(AccessLevel.NONE)
    private Map<String, AccountRateCappingProperties> overridesMap = Collections.emptyMap();

    @Setter(AccessLevel.NONE)
    private AccountRateCappingProperties defaultRateCappingProperties;

    @Data
    @Builder
    public static class AccountRateCappingProperties {
        private String ncaId;
        private long allowedInvocationsPerSecond;
    }

    @PostConstruct
    void postConstruct() {
        if (!CollectionUtils.isEmpty(overrides)) {
            this.overridesMap = overrides.stream()
                    .filter(props -> StringUtils.isNotBlank(props.getNcaId()))
                    .collect(Collectors.toMap(AccountRateCappingProperties::getNcaId,
                                              Function.identity()));

            if (overridesMap.size() != overrides.size()) {
                log.warn(MESG_INVALID_OVERRIDES_ENTRY);
            }
        }

        this.defaultRateCappingProperties =
                AccountRateCappingProperties.builder()
                        .ncaId("*")
                        .allowedInvocationsPerSecond(allowedInvocationsPerSecond)
                        .build();
    }
}
