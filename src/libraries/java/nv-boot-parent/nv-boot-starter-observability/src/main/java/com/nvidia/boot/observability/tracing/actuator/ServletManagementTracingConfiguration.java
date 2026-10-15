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

package com.nvidia.boot.observability.tracing.actuator;

import io.micrometer.observation.ObservationRegistry;
import jakarta.servlet.DispatcherType;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.boot.micrometer.tracing.autoconfigure.MicrometerTracingAutoConfiguration;
import org.springframework.boot.micrometer.tracing.opentelemetry.autoconfigure.OpenTelemetryTracingAutoConfiguration;
import org.springframework.boot.actuate.autoconfigure.web.ManagementContextConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication.Type;
import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Import;
import org.springframework.core.Ordered;
import org.springframework.http.server.observation.ServerRequestObservationConvention;
import org.springframework.web.filter.ServerHttpObservationFilter;

/**
 * Management context configuration that enables tracing for actuator endpoints
 * when the management server runs on a separate port (e.g. 8181).
 *
 * <p>Servlet filters from the main application context are not applied to the
 * management child context (Spring Boot #31811). This configuration registers
 * {@link ServerHttpObservationFilter} in the management context so HTTP
 * requests to actuator endpoints produce traces.
 *
 * <p>Placed in {@code com.nvidia.boot.observability.tracing.actuator} so it is not scanned by
 * the main application. Registration is via {@code ManagementContextConfiguration.imports}.
 */
@ManagementContextConfiguration
@Import({
    OpenTelemetryTracingAutoConfiguration.class,
    MicrometerTracingAutoConfiguration.class
})
@ConditionalOnWebApplication(type = Type.SERVLET)
public class ServletManagementTracingConfiguration {

    @Bean
    public FilterRegistrationBean<ServerHttpObservationFilter> serverHttpObservationFilter(
            ObservationRegistry observationRegistry,
            ObjectProvider<ServerRequestObservationConvention> observationConventionProvider) {
        var convention = observationConventionProvider.getIfAvailable();
        var filter = convention != null
                ? new ServerHttpObservationFilter(observationRegistry, convention)
                : new ServerHttpObservationFilter(observationRegistry);
        var registration = new FilterRegistrationBean<ServerHttpObservationFilter>(filter);
        registration.setDispatcherTypes(DispatcherType.REQUEST, DispatcherType.ASYNC,
                                        DispatcherType.ERROR);
        registration.setOrder(Ordered.HIGHEST_PRECEDENCE + 1);
        registration.addUrlPatterns("/*");
        return registration;
    }
}
