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

package com.nvidia.boot.observability.tracing;

import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.sdk.resources.Resource;
import java.lang.management.ManagementFactory;
import org.springframework.boot.micrometer.tracing.opentelemetry.autoconfigure.SdkTracerProviderBuilderCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * OpenTelemetry tracing configuration shared by servlet and reactive stacks: resource attributes
 * for host/process/runtime. Servlet- and WebFlux-specific HTTP observation conventions are
 * registered by {@link com.nvidia.boot.observability.tracing.server.OtelServletTracingConfiguration}
 * and {@link com.nvidia.boot.observability.tracing.server.OtelReactiveTracingConfiguration}.
 */
@Configuration
public class OtelTracingConfiguration {

    /**
     * Adds host, process, and runtime resource attributes to spans, aligning with
     * what the OpenTelemetry Java agent provides by default.
     */
    @Bean
    public SdkTracerProviderBuilderCustomizer otelResourceAttributesCustomizer() {
        return builder -> builder.addResource(createProcessAndHostResource());
    }

    private static Resource createProcessAndHostResource() {
        var attributes = Attributes.builder()
                .put(AttributeKey.longKey("process.pid"), ProcessHandle.current().pid())
                .put(AttributeKey.stringKey("host.arch"), System.getProperty("os.arch", "unknown"))
                .put(AttributeKey.stringKey("os.type"), System.getProperty("os.name", "unknown"))
                .put(AttributeKey.stringKey("os.description"), buildOsDescription())
                .put(AttributeKey.stringKey("process.runtime.name"), "Java")
                .put(AttributeKey.stringKey("process.runtime.version"),
                     System.getProperty("java.version", "unknown"))
                .put(AttributeKey.stringKey("process.runtime.description"),
                     buildProcessRuntimeDescription())
                .put(AttributeKey.stringKey("process.executable.path"),
                     System.getProperty("java.home", "") + "/bin/java")
                .put(AttributeKey.stringArrayKey("process.command_args"),
                     ManagementFactory.getRuntimeMXBean().getInputArguments())
                .build();

        return Resource.create(attributes);
    }

    private static String buildOsDescription() {
        var osName = System.getProperty("os.name", "");
        var osVersion = System.getProperty("os.version", "");
        return (osName + " " + osVersion).trim();
    }

    private static String buildProcessRuntimeDescription() {
        var runtimeName = System.getProperty("java.runtime.name", "Java");
        var runtimeVersion = System.getProperty("java.runtime.version", "");
        return (runtimeName + " " + runtimeVersion).trim();
    }
}
