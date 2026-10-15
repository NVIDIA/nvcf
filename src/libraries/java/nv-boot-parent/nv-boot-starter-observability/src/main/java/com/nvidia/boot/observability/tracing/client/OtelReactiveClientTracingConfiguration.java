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

package com.nvidia.boot.observability.tracing.client;

import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.reactive.function.client.ClientRequestObservationConvention;

/**
 * Registers {@link OtelReactiveClientRequestObservationConvention} when WebClient observation support
 * is on the classpath. Listed in {@code META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports}
 * (not {@code @Import}) so servlet-only applications without spring-webflux do not load WebFlux types.
 */
@Configuration(proxyBeanMethods = false)
@ConditionalOnClass(name = "org.springframework.web.reactive.function.client.ClientRequestObservationConvention")
public class OtelReactiveClientTracingConfiguration {

    @Bean
    @ConditionalOnMissingBean(ClientRequestObservationConvention.class)
    public ClientRequestObservationConvention otelReactiveClientRequestObservationConvention() {
        return new OtelReactiveClientRequestObservationConvention();
    }
}
