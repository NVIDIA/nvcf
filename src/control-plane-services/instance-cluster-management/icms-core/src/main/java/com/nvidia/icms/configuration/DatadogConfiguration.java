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
import com.nvidia.icms.service.telemetry.DatadogEventLogger;
import io.micrometer.tracing.Tracer;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class DatadogConfiguration {

    @Bean
    public DatadogEventLogger datadogEventLogger(
            @Value("${icms.datadog.events.enabled:false}") boolean enabled,
            ObjectMapper objectMapper,
            Tracer tracer) {
        return new DatadogEventLogger(enabled, objectMapper, tracer);
    }
}
