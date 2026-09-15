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

package com.nvidia.boot.telemetry;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.telemetry.client.CloudEventBuilderProvider;
import com.nvidia.boot.telemetry.client.OAuth2BearerFilter;
import com.nvidia.boot.telemetry.client.TelemetryClient;
import com.nvidia.boot.telemetry.client.TelemetryProperties;
import com.nvidia.boot.telemetry.client.TelemetryWebClientFactory;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.context.annotation.Bean;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * Auto-configuration for NV Boot telemetry. Apps must add TelemetryProperties to the Spring
 * context (e.g., via {@code @Bean}.
 */
@AutoConfiguration
@ConditionalOnBean(TelemetryProperties.class)
public class TelemetryAutoConfiguration {

    @Bean
    @ConditionalOnMissingBean(TelemetryClient.class)
    public TelemetryClient telemetryClient(
            WebClient.Builder webClientBuilder,  // Prototype-scoped
            TelemetryProperties properties,
            JsonMapper jsonMapper) {
        var connectTimeout = properties.getConnectTimeout() != null
                ? properties.getConnectTimeout()
                : java.time.Duration.ofSeconds(30);
        var readTimeout = properties.getReadTimeout() != null
                ? properties.getReadTimeout()
                : java.time.Duration.ofSeconds(30);

        var tokenWebClient = TelemetryWebClientFactory.createTokenWebClient(
                webClientBuilder.clone(),
                connectTimeout,
                readTimeout);
        var oauth2BearerFilter = new OAuth2BearerFilter(properties, tokenWebClient);

        var webClient = TelemetryWebClientFactory.createTelemetryWebClient(
                webClientBuilder.clone(),
                properties.getUrl(),
                connectTimeout,
                readTimeout,
                oauth2BearerFilter);

        return new TelemetryClient(properties, jsonMapper, webClient);
    }

    @Bean
    @ConditionalOnMissingBean(CloudEventBuilderProvider.class)
    public CloudEventBuilderProvider cloudEventBuilderProvider() {
        return new CloudEventBuilderProvider();
    }
}
