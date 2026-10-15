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

import com.nvidia.nvct.util.NvctConstants;
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
@ConfigurationProperties(prefix = "nvct.rate-limiters.task-rate-limiter")
public class TaskRateLimiterProperties {

    private static final String MESG_INVALID_OVERRIDES_ENTRY =
            "Invalid or duplicate entry in the task-rate-limiter's overrides config";

    private long allowedInvocationsPerSecond = 100;
    private List<TaskRateCappingProperties> overrides;

    // Only contains override entries that have taskId.
    @Setter(AccessLevel.NONE)
    private Map<UUID, TaskRateCappingProperties> taskOverridesMap = Collections.emptyMap();

    @Setter(AccessLevel.NONE)
    private TaskRateCappingProperties defaultRateCappingProperties;

    @Builder
    @Data
    public static class TaskRateCappingProperties {
        private UUID taskId;
        private long allowedInvocationsPerSecond;
    }

    @PostConstruct
    void postConstruct() {
        if (!CollectionUtils.isEmpty(overrides)) {
            this.taskOverridesMap = overrides.stream()
                    .filter(props -> props.getTaskId() != null)
                    .collect(Collectors.toMap(
                            TaskRateCappingProperties::getTaskId, Function.identity()));

            if (taskOverridesMap.size() != overrides.size()) {
                log.warn(MESG_INVALID_OVERRIDES_ENTRY);
            }
        }

        this.defaultRateCappingProperties =
                TaskRateCappingProperties.builder()
                        .taskId(NvctConstants.UUID_WILDCARD)
                        .allowedInvocationsPerSecond(allowedInvocationsPerSecond)
                        .build();
    }
}
