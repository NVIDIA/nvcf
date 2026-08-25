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
package com.nvidia.nvcf.configuration.scheduler;

import static com.nvidia.nvcf.util.NvcfConstants.MAX_THREAD_POOL_SIZE;

import jakarta.annotation.PostConstruct;
import java.util.List;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;
import org.springframework.util.CollectionUtils;
import org.springframework.util.StringUtils;

@Slf4j
@Data
@RefreshScope
@Configuration("functionDeploymentsTaskProperties")
@ConfigurationProperties(prefix = "nvcf.scheduler.function-deployments")
@ConditionalOnProperty(
        name = "nvcf.scheduler.enabled", havingValue = "true", matchIfMissing = true)
public class FunctionDeploymentsTaskProperties {

    private static final String MESG_MISSING_CURRENT_REGION =
            "nvcf.scheduler.function-deployments.current-region must be configured";
    private static final String MESG_WHITESPACE_PADDED_CURRENT_REGION =
            "nvcf.scheduler.function-deployments.current-region must not contain surrounding " +
                    "whitespace";
    private static final String MESG_INVALID_REGION =
            "Current region '%s' must be included in the list of configured regions '%s'";
    private static final String MESG_INVALID_CONFIGURED_REGIONS =
            "Configured function deployment regions must contain only nonblank, trimmed values: " +
                    "%s";
    private static final String MESG_DUPLICATE_REGIONS =
            "Configured function deployment regions must not contain duplicates: %s";
    private static final String MESG_INVALID_MAX_CONCURRENCY =
            "nvcf.scheduler.function-deployments.max-concurrency must be at least 1";
    private static final String MESG_PROPERTIES_INFO =
            "Function deployment reconciliation: Current region '{}', configured regions '{}', " +
                    "max-concurrency '{}'";

    private String currentRegion;
    private List<String> regions = List.of();
    private Integer maxConcurrency;

    public int getCurrentRegionIndex() {
        return regions.indexOf(currentRegion);
    }

    @PostConstruct
    void validateAndNormalize() {
        if (!StringUtils.hasText(currentRegion)) {
            log.error(MESG_MISSING_CURRENT_REGION);
            throw new IllegalStateException(MESG_MISSING_CURRENT_REGION);
        }
        if (!currentRegion.equals(currentRegion.trim())) {
            log.error(MESG_WHITESPACE_PADDED_CURRENT_REGION);
            throw new IllegalStateException(MESG_WHITESPACE_PADDED_CURRENT_REGION);
        }
        if (maxConcurrency == null) {
            maxConcurrency = Math.min(MAX_THREAD_POOL_SIZE,
                                      Runtime.getRuntime().availableProcessors());
        }
        if (maxConcurrency < 1) {
            log.error(MESG_INVALID_MAX_CONCURRENCY);
            throw new IllegalStateException(MESG_INVALID_MAX_CONCURRENCY);
        }
        if (!CollectionUtils.isEmpty(regions) && regions.stream().anyMatch(region ->
                !StringUtils.hasText(region) || !region.equals(region.trim()))) {
            var mesg = MESG_INVALID_CONFIGURED_REGIONS.formatted(regions);
            log.error(mesg);
            throw new IllegalStateException(mesg);
        }
        regions = CollectionUtils.isEmpty(regions) ? List.of(currentRegion) : List.copyOf(regions);
        if (regions.stream().distinct().count() != regions.size()) {
            var mesg = MESG_DUPLICATE_REGIONS.formatted(regions);
            log.error(mesg);
            throw new IllegalStateException(mesg);
        }
        if (!regions.contains(currentRegion)) {
            var mesg = MESG_INVALID_REGION.formatted(currentRegion, regions);
            log.error(mesg);
            throw new IllegalStateException(mesg);
        }
        log.info(MESG_PROPERTIES_INFO, currentRegion, regions, maxConcurrency);
    }
}
