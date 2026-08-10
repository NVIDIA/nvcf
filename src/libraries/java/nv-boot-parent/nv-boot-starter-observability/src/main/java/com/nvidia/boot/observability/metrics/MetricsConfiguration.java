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

package com.nvidia.boot.observability.metrics;

import io.micrometer.core.instrument.MeterRegistry;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.micrometer.metrics.autoconfigure.MeterRegistryCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * Configures common tags for all metrics.
 */
@Configuration
public class MetricsConfiguration {

    @Bean
    public MeterRegistryCustomizer<MeterRegistry> commonTagsMeterRegistryCustomizer(
            @Value("${management.metrics.tags.env:${ENVIRONMENT:default}}") String env,
            @Value("${management.metrics.tags.host_id:${HOSTNAME:unknown}}") String hostId,
            @Value("${management.metrics.tags.host_dc:${AWS_REGION:${CLOUD_REGION:unknown}}}")
            String hostDc) {
        return registry -> registry.config()
                .commonTags("env", env)
                .commonTags("host_id", hostId)
                .commonTags("host_dc", hostDc);
    }
}
