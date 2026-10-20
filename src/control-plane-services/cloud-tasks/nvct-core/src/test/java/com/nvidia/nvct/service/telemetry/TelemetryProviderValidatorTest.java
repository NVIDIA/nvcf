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

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvct.service.telemetry.dto.TelemetryProviderEnum;
import com.nvidia.nvct.service.telemetry.dto.TelemetryTypeEnum;
import java.util.Arrays;
import java.util.EnumSet;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.MethodSource;

class TelemetryProviderValidatorTest {

    private static final UUID TELEMETRY_ID = UUID.fromString(
            "4df8cb5b-94e0-4fcc-ac12-ddb8da2f25aa");

    // Signals each provider's exporter renders in byoo-otel-collector render.go.
    private static final Map<TelemetryProviderEnum, Set<TelemetryTypeEnum>> COLLECTOR_TYPES =
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

    @Test
    void supportedProvidersMatchCollectorRenderedProviders() {
        assertThat(TelemetryProviderValidator.supportedProviders()).isEqualTo(
                "AZURE_MONITOR, DATADOG, GRAFANA_CLOUD, KRATOS, KRATOS_THANOS, "
                        + "OTEL_COLLECTOR, PROMETHEUS, SERVICENOW, SPLUNK");
    }

    @ParameterizedTest
    @EnumSource(value = TelemetryProviderEnum.class, names = {"TIMESTREAM", "VICTORIAMETRICS"})
    void shouldRejectProvidersTheCollectorCannotRender(TelemetryProviderEnum provider) {
        assertThatThrownBy(() -> TelemetryProviderValidator.validateReference(
                TELEMETRY_ID, TelemetryTypeEnum.METRICS, provider))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining(TELEMETRY_ID.toString())
                .hasMessageContaining("provider '" + provider + "' is not supported")
                .hasMessageContaining(TelemetryProviderValidator.supportedProviders());
    }

    static Stream<Arguments> providerTypes() {
        return COLLECTOR_TYPES.keySet().stream()
                .flatMap(provider -> Arrays.stream(TelemetryTypeEnum.values())
                        .map(type -> Arguments.of(provider, type)));
    }

    @ParameterizedTest
    @MethodSource("providerTypes")
    void shouldAcceptOnlyTypesTheCollectorExportsForTheProvider(
            TelemetryProviderEnum provider, TelemetryTypeEnum type) {
        var supportedTypes = COLLECTOR_TYPES.get(provider);
        if (supportedTypes.contains(type)) {
            assertThatCode(() -> TelemetryProviderValidator.validateReference(
                    TELEMETRY_ID, type, provider))
                    .doesNotThrowAnyException();
        } else {
            assertThatThrownBy(() -> TelemetryProviderValidator.validateReference(
                    TELEMETRY_ID, type, provider))
                    .isInstanceOf(BadRequestException.class)
                    .hasMessageContaining("Telemetry '" + TELEMETRY_ID + "': provider '"
                                                  + provider + "' does not support " + type
                                                  + ". Supported types: " + supportedTypes);
        }
    }
}
