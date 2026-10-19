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
package com.nvidia.nvcf.service.telemetry;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProviderEnum;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.MethodSource;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;

class TelemetryProviderValidatorTest {

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();

    @Test
    void supportedProvidersMatchCollectorRenderedProviders() {
        assertThat(TelemetryProviderValidator.supportedProviders()).isEqualTo(
                "AZURE_MONITOR, DATADOG, GRAFANA_CLOUD, KRATOS, KRATOS_THANOS, "
                        + "OTEL_COLLECTOR, PROMETHEUS, SERVICENOW, SPLUNK");
    }

    @ParameterizedTest
    @EnumSource(value = TelemetryProviderEnum.class, names = {"TIMESTREAM", "VICTORIAMETRICS"})
    void shouldRejectProvidersTheCollectorCannotRender(TelemetryProviderEnum provider) {
        assertThatThrownBy(() -> TelemetryProviderValidator.validateProvider(provider))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("'" + provider + "' is not supported")
                .hasMessageContaining(TelemetryProviderValidator.supportedProviders());
        assertThatThrownBy(() -> TelemetryProviderValidator.validateSecretValue(
                provider, new StringNode("secret")))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("Supported providers");
    }

    @ParameterizedTest
    @EnumSource(value = TelemetryProviderEnum.class, mode = EnumSource.Mode.EXCLUDE,
            names = {"TIMESTREAM", "VICTORIAMETRICS"})
    void shouldAcceptProvidersTheCollectorCanRender(TelemetryProviderEnum provider) {
        assertThatCode(() -> TelemetryProviderValidator.validateProvider(provider))
                .doesNotThrowAnyException();
    }

    static Stream<Arguments> validSecrets() {
        return Stream.of(
                Arguments.of(TelemetryProviderEnum.SPLUNK, new StringNode("token")),
                Arguments.of(TelemetryProviderEnum.DATADOG, new StringNode("api-key")),
                Arguments.of(TelemetryProviderEnum.SERVICENOW, new StringNode("token")),
                Arguments.of(TelemetryProviderEnum.OTEL_COLLECTOR, new StringNode("token")),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, object(
                        "instanceId", "123", "apiKey", "key")),
                Arguments.of(TelemetryProviderEnum.KRATOS, object(
                        "collectorId", "id", "clientCert", "cert", "clientKey", "key")),
                Arguments.of(TelemetryProviderEnum.AZURE_MONITOR, object(
                        "instrumentationKey", "ikey", "applicationId", "app",
                        "liveEndpoint", "https://live.example.com")),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, object(
                        "clientCert", "cert", "clientKey", "key")),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, object("caFile", "ca")),
                Arguments.of(TelemetryProviderEnum.KRATOS_THANOS, object(
                        "clientCert", "cert", "clientKey", "key", "caFile", "ca")));
    }

    @ParameterizedTest
    @MethodSource("validSecrets")
    void shouldAcceptSecretShapeTheCollectorCanRender(
            TelemetryProviderEnum provider, JsonNode value) {
        assertThatCode(() -> TelemetryProviderValidator.validateSecretValue(provider, value))
                .doesNotThrowAnyException();
    }

    static Stream<Arguments> invalidSecrets() {
        return Stream.of(
                Arguments.of(TelemetryProviderEnum.SPLUNK, object("token", "value"),
                             "must be a non-empty string"),
                Arguments.of(TelemetryProviderEnum.DATADOG, new StringNode(" "),
                             "must be a non-empty string"),
                Arguments.of(TelemetryProviderEnum.SERVICENOW, null,
                             "must be a non-empty string"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, new StringNode("key"),
                             "must be a JSON object"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, object("instanceId", "123"),
                             "missing required fields [apiKey]"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD,
                             object("username", "user", "password", "pass"),
                             "missing required fields [instanceId, apiKey]"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD,
                             object("instanceId", "123", "apiKey", ""),
                             "missing required fields [apiKey]"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD,
                             JSON_MAPPER.createObjectNode().put("instanceId", 123)
                                     .put("apiKey", "key"),
                             "secret field 'instanceId' must be a string"),
                Arguments.of(TelemetryProviderEnum.KRATOS,
                             object("clientCert", "cert", "clientKey", "key"),
                             "missing required fields [collectorId]"),
                Arguments.of(TelemetryProviderEnum.AZURE_MONITOR,
                             object("instrumentationKey", "ikey"),
                             "missing required fields [applicationId, liveEndpoint]"),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, object("clientCert", "cert"),
                             "'clientCert' and 'clientKey' must be set together"),
                Arguments.of(TelemetryProviderEnum.KRATOS_THANOS,
                             object("clientKey", "key", "caFile", "ca"),
                             "'clientCert' and 'clientKey' must be set together"),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, object("caCert", "ca"),
                             "needs 'clientCert' and 'clientKey', 'caFile', or both"),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, new StringNode("token"),
                             "must be a JSON object"));
    }

    @ParameterizedTest
    @MethodSource("invalidSecrets")
    void shouldRejectSecretShapeTheCollectorCannotRender(
            TelemetryProviderEnum provider, JsonNode value, String expectedMessage) {
        assertThatThrownBy(() -> TelemetryProviderValidator.validateSecretValue(provider, value))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("Telemetry provider '" + provider + "'")
                .hasMessageContaining(expectedMessage);
    }

    private static JsonNode object(String... keyValues) {
        var node = JSON_MAPPER.createObjectNode();
        for (int i = 0; i < keyValues.length; i += 2) {
            node.put(keyValues[i], keyValues[i + 1]);
        }
        return node;
    }
}
