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
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;

class TelemetryProviderValidatorTest {

    private static final UUID TELEMETRY_ID = UUID.fromString(
            "4df8cb5b-94e0-4fcc-ac12-ddb8da2f25aa");

    @Test
    void supportedProvidersMatchCollectorRenderedProviders() {
        assertThat(TelemetryProviderValidator.supportedProviders()).isEqualTo(
                "AZURE_MONITOR, DATADOG, GRAFANA_CLOUD, KRATOS, KRATOS_THANOS, "
                        + "OTEL_COLLECTOR, PROMETHEUS, SERVICENOW, SPLUNK");
    }

    @ParameterizedTest
    @EnumSource(value = TelemetryProviderEnum.class, names = {"TIMESTREAM", "VICTORIAMETRICS"})
    void shouldRejectProvidersTheCollectorCannotRender(TelemetryProviderEnum provider) {
        assertThatThrownBy(() -> TelemetryProviderValidator.validateProvider(TELEMETRY_ID, provider))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining(TELEMETRY_ID.toString())
                .hasMessageContaining("provider '" + provider + "' is not supported")
                .hasMessageContaining(TelemetryProviderValidator.supportedProviders());
    }

    @ParameterizedTest
    @EnumSource(value = TelemetryProviderEnum.class, mode = EnumSource.Mode.EXCLUDE,
            names = {"TIMESTREAM", "VICTORIAMETRICS"})
    void shouldAcceptProvidersTheCollectorCanRender(TelemetryProviderEnum provider) {
        assertThatCode(() -> TelemetryProviderValidator.validateProvider(TELEMETRY_ID, provider))
                .doesNotThrowAnyException();
    }
}
