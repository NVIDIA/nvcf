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

package com.nvidia.boot.observability;

import com.nvidia.boot.observability.metrics.MetricsConfiguration;
import com.nvidia.boot.observability.tracing.client.OtelBlockingClientTracingConfiguration;
import com.nvidia.boot.observability.tracing.server.OtelReactiveTracingConfiguration;
import com.nvidia.boot.observability.tracing.server.OtelServletTracingConfiguration;
import com.nvidia.boot.observability.tracing.OtelTracingConfiguration;
import com.nvidia.boot.observability.tracing.SpringCloudInfrastructureRoleConfiguration;
import com.nvidia.boot.observability.tracing.redaction.AttributeRedactingSpanExporterConfiguration;
import com.nvidia.boot.observability.tracing.stacktrace.ExceptionShorteningSpanExporterConfiguration;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.context.annotation.Import;

/**
 * Auto-configuration for NV Boot observability: logging (filtered stack traces),
 * metrics (common tags), OpenTelemetry tracing with semantic conventions,
 * attribute redaction, exception shortening, and management context tracing
 * for actuator endpoints.
 */
@AutoConfiguration
@Import({
    MetricsConfiguration.class,
    OtelTracingConfiguration.class,
    OtelServletTracingConfiguration.class,
    OtelBlockingClientTracingConfiguration.class,
    OtelReactiveTracingConfiguration.class,
    AttributeRedactingSpanExporterConfiguration.class,
    ExceptionShorteningSpanExporterConfiguration.class,
    SpringCloudInfrastructureRoleConfiguration.class
})
public class ObservabilityAutoConfiguration {
}
