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
package com.nvidia.icms.configuration.cpu;

import java.time.Duration;
import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;

/**
 * Configuration for CPU-only workloads (instance types with no GPU, registered under the
 * reserved GPU name {@code CPU}).
 *
 * <p>The feature is off by default. With {@code enabled=false} ICMS behaves exactly as it did
 * before CPU-only workloads existed for every GPU name.
 */
@RefreshScope
@Configuration
@ConfigurationProperties(prefix = "icms.cpu-workloads")
@Data
public class CpuWorkloadsConfigurationProperties {

    /** Master switch. When false no CPU-specific behavior is active. */
    private boolean enabled = false;

    /** Number of cluster rows read per page by the collision guard. */
    private int guardPageSize = 200;

    /** Time between two background attempts of the collision guard while its result is not clean. */
    private Duration guardRetryInterval = Duration.ofSeconds(30);

    /**
     * How long the collision guard waits, after a registration that may hold a GPU under the reserved
     * name was let through, before it may report a clean result. It covers the time a registration takes
     * from validation to being stored. The guard retries every {@code guardRetryInterval} meanwhile.
     */
    private Duration guardRegistrationGrace = Duration.ofMinutes(2);
}
