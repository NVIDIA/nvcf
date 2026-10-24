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
package com.nvidia.icms.configuration;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.boot.telemetry.client.CloudEventBuilderProvider;
import com.nvidia.boot.telemetry.client.TelemetryClient;
import com.nvidia.boot.telemetry.client.TelemetryProperties;
import com.nvidia.icms.service.telemetry.DatadogEventLogger;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class TelemetryConfiguration {

    @Bean
    @RefreshScope
    @ConfigurationProperties(prefix = "icms.telemetry")
    public TelemetryProperties telemetryProperties() {
        return new TelemetryProperties();
    }

    @Bean
    @RefreshScope
    public TelemetryEventClient telemetryEventClient(
            @Value("${icms.telemetry.enabled}") boolean isEnabled,
            @Value("${spring.application.env}") String env,
            @Value("${spring.application.host.dc}") String region,
            @Value("${icms.telemetry.resource-name}") String resourceName,
            TelemetryClient telemetryClient,
            CloudEventBuilderProvider cloudEventBuilderProvider,
            ObjectMapper objectMapper,
            DatadogEventLogger datadogEventLogger) {

        return new TelemetryEventClient(telemetryClient, cloudEventBuilderProvider, resourceName,
                isEnabled, env, region, objectMapper, datadogEventLogger);
    }
}
