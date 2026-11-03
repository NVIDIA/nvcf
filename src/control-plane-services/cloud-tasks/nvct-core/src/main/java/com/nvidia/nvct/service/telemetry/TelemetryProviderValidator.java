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
package com.nvidia.nvct.service.telemetry;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvct.service.telemetry.dto.TelemetryProviderEnum;
import com.nvidia.nvct.service.telemetry.dto.TelemetryTypeEnum;
import java.util.EnumSet;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;

/**
 * Validates that the BYOO OpenTelemetry collector can render a telemetry provider for a
 * signal type (byoo-otel-collector/internal/otelconfig/render.go).
 */
public final class TelemetryProviderValidator {

    // Signals the collector exports for each provider.
    static final Map<TelemetryProviderEnum, Set<TelemetryTypeEnum>> SUPPORTED_TYPES =
            Map.of(
                    TelemetryProviderEnum.AZURE_MONITOR, EnumSet.allOf(TelemetryTypeEnum.class),
                    TelemetryProviderEnum.DATADOG, EnumSet.allOf(TelemetryTypeEnum.class),
                    TelemetryProviderEnum.GRAFANA_CLOUD, EnumSet.allOf(TelemetryTypeEnum.class),
                    TelemetryProviderEnum.KRATOS,
                    EnumSet.of(TelemetryTypeEnum.LOGS, TelemetryTypeEnum.TRACES),
                    TelemetryProviderEnum.KRATOS_THANOS, EnumSet.of(TelemetryTypeEnum.METRICS),
                    TelemetryProviderEnum.OTEL_COLLECTOR, EnumSet.allOf(TelemetryTypeEnum.class),
                    TelemetryProviderEnum.PROMETHEUS, EnumSet.of(TelemetryTypeEnum.METRICS),
                    TelemetryProviderEnum.SERVICENOW, EnumSet.of(TelemetryTypeEnum.TRACES),
                    TelemetryProviderEnum.SPLUNK, EnumSet.of(TelemetryTypeEnum.LOGS));

    static final Set<TelemetryProviderEnum> SUPPORTED_PROVIDERS =
            EnumSet.copyOf(SUPPORTED_TYPES.keySet());

    private static final String MESG_UNSUPPORTED_PROVIDER =
            "Telemetry '%s': provider '%s' is not supported. Supported providers: %s";
    private static final String MESG_UNSUPPORTED_TYPE =
            "Telemetry '%s': provider '%s' does not support %s. Supported types: %s";

    private TelemetryProviderValidator() {
    }

    public static String supportedProviders() {
        return SUPPORTED_PROVIDERS.stream()
                .map(TelemetryProviderEnum::toString)
                .sorted()
                .collect(Collectors.joining(", "));
    }

    public static void validateReference(UUID telemetryId,
                                         TelemetryTypeEnum type,
                                         TelemetryProviderEnum provider) {
        if (!SUPPORTED_PROVIDERS.contains(provider)) {
            throw new BadRequestException(
                    MESG_UNSUPPORTED_PROVIDER.formatted(telemetryId, provider,
                                                        supportedProviders()));
        }
        var supportedTypes = SUPPORTED_TYPES.get(provider);
        if (!supportedTypes.contains(type)) {
            throw new BadRequestException(
                    MESG_UNSUPPORTED_TYPE.formatted(telemetryId, provider, type, supportedTypes));
        }
    }
}
