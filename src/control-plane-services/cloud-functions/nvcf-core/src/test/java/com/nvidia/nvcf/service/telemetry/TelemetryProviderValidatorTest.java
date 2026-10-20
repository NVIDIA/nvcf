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
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryTypeEnum;
import java.util.EnumSet;
import java.util.Set;
import java.util.UUID;
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
    private static final UUID TELEMETRY_ID = UUID.fromString(
            "4df8cb5b-94e0-4fcc-ac12-ddb8da2f25aa");
    private static final Set<TelemetryTypeEnum> ALL_TYPES = EnumSet.allOf(TelemetryTypeEnum.class);
    private static final Set<TelemetryTypeEnum> LOGS = Set.of(TelemetryTypeEnum.LOGS);
    private static final Set<TelemetryTypeEnum> METRICS = Set.of(TelemetryTypeEnum.METRICS);
    private static final Set<TelemetryTypeEnum> TRACES = Set.of(TelemetryTypeEnum.TRACES);
    private static final Set<TelemetryTypeEnum> LOGS_AND_TRACES =
            Set.of(TelemetryTypeEnum.LOGS, TelemetryTypeEnum.TRACES);

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
                provider, ALL_TYPES, new StringNode("secret")))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("Supported providers");
    }

    @ParameterizedTest
    @EnumSource(value = TelemetryProviderEnum.class, mode = EnumSource.Mode.EXCLUDE,
            names = {"TIMESTREAM", "VICTORIAMETRICS"})
    void shouldAcceptProvidersTheCollectorCanRender(TelemetryProviderEnum provider) {
        assertThatCode(() -> TelemetryProviderValidator.validateProvider(provider))
                .doesNotThrowAnyException();
        assertThatCode(() -> TelemetryProviderValidator.validateProvider(
                TELEMETRY_ID, TelemetryTypeEnum.LOGS, provider))
                .doesNotThrowAnyException();
    }

    @ParameterizedTest
    @EnumSource(value = TelemetryProviderEnum.class, names = {"TIMESTREAM", "VICTORIAMETRICS"})
    void shouldNameTheReferencedTelemetryAndSignalForAnUnsupportedProvider(
            TelemetryProviderEnum provider) {
        assertThatThrownBy(() -> TelemetryProviderValidator.validateProvider(
                TELEMETRY_ID, TelemetryTypeEnum.METRICS, provider))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("Telemetry '" + TELEMETRY_ID + "' for METRICS: provider '"
                                              + provider + "' is not supported. "
                                              + "Supported providers: "
                                              + TelemetryProviderValidator.supportedProviders());
    }

    static Stream<Arguments> validSecrets() {
        return Stream.of(
                Arguments.of(TelemetryProviderEnum.SPLUNK, LOGS, new StringNode("token")),
                Arguments.of(TelemetryProviderEnum.DATADOG, ALL_TYPES, new StringNode("api-key")),
                Arguments.of(TelemetryProviderEnum.SERVICENOW, TRACES, new StringNode("token")),
                Arguments.of(TelemetryProviderEnum.OTEL_COLLECTOR, ALL_TYPES,
                             new StringNode("token")),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, ALL_TYPES, object(
                        "instanceId", "123", "apiKey", "key")),
                Arguments.of(TelemetryProviderEnum.KRATOS, LOGS, object(
                        "collectorId", "id", "clientCert", "cert", "clientKey", "key")),
                Arguments.of(TelemetryProviderEnum.AZURE_MONITOR, ALL_TYPES, object(
                        "instrumentationKey", "ikey", "applicationId", "app",
                        "liveEndpoint", "https://live.example.com")),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, METRICS, object(
                        "clientCert", "cert", "clientKey", "key")),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, METRICS, object("caFile", "ca")),
                Arguments.of(TelemetryProviderEnum.KRATOS_THANOS, METRICS, object(
                        "clientCert", "cert", "clientKey", "key", "caFile", "ca")),
                // KRATOS needs collectorId only for logs, and nothing for metrics.
                Arguments.of(TelemetryProviderEnum.KRATOS, METRICS, object(
                        "clientCert", "cert", "clientKey", "key")),
                Arguments.of(TelemetryProviderEnum.KRATOS, METRICS, object("token", "value")),
                Arguments.of(TelemetryProviderEnum.KRATOS, TRACES, object(
                        "clientCert", "cert", "clientKey", "key")),
                Arguments.of(TelemetryProviderEnum.KRATOS, LOGS_AND_TRACES, object(
                        "collectorId", "id", "clientCert", "cert", "clientKey", "key")),
                Arguments.of(TelemetryProviderEnum.KRATOS, Set.of(TelemetryTypeEnum.METRICS,
                                                                  TelemetryTypeEnum.TRACES),
                             object("clientCert", "cert", "clientKey", "key", "caFile", "ca")),
                // Other rules that only one signal's exporter reads.
                Arguments.of(TelemetryProviderEnum.SPLUNK, METRICS, object("token", "value")),
                Arguments.of(TelemetryProviderEnum.SERVICENOW, LOGS, object("token", "value")),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, LOGS, new StringNode("token")),
                Arguments.of(TelemetryProviderEnum.KRATOS_THANOS, TRACES,
                             object("clientCert", "cert")));
    }

    @ParameterizedTest
    @MethodSource("validSecrets")
    void shouldAcceptSecretShapeTheCollectorCanRender(
            TelemetryProviderEnum provider, Set<TelemetryTypeEnum> types, JsonNode value) {
        assertThatCode(() -> TelemetryProviderValidator.validateSecretValue(provider, types, value))
                .doesNotThrowAnyException();
    }

    static Stream<Arguments> invalidSecrets() {
        return Stream.of(
                Arguments.of(TelemetryProviderEnum.SPLUNK, LOGS, object("token", "value"),
                             "with types [LOGS]: secret value must be a non-empty string"),
                Arguments.of(TelemetryProviderEnum.DATADOG, METRICS, new StringNode(" "),
                             "must be a non-empty string"),
                Arguments.of(TelemetryProviderEnum.SERVICENOW, TRACES, null,
                             "must be a string or a JSON object"),
                Arguments.of(TelemetryProviderEnum.SERVICENOW, TRACES, object("token", "value"),
                             "with types [TRACES]: secret value must be a non-empty string"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, LOGS, new StringNode("key"),
                             "must be a JSON object"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, TRACES,
                             object("instanceId", "123"),
                             "missing required fields [apiKey]"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, METRICS,
                             object("username", "user", "password", "pass"),
                             "missing required fields [instanceId, apiKey]"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, METRICS,
                             object("instanceId", "123", "apiKey", ""),
                             "missing required fields [apiKey]"),
                Arguments.of(TelemetryProviderEnum.GRAFANA_CLOUD, METRICS,
                             JSON_MAPPER.createObjectNode().put("instanceId", 123)
                                     .put("apiKey", "key"),
                             "secret field 'instanceId' must be a string"),
                Arguments.of(TelemetryProviderEnum.KRATOS, LOGS,
                             object("clientCert", "cert", "clientKey", "key"),
                             "Telemetry provider 'KRATOS' with types [LOGS]: "
                                     + "secret is missing required fields [collectorId]"),
                Arguments.of(TelemetryProviderEnum.KRATOS, ALL_TYPES,
                             object("clientCert", "cert", "clientKey", "key"),
                             "Telemetry provider 'KRATOS' with types [LOGS]: "
                                     + "secret is missing required fields [collectorId]"),
                Arguments.of(TelemetryProviderEnum.KRATOS, LOGS_AND_TRACES,
                             object("clientCert", "cert", "clientKey", "key"),
                             "Telemetry provider 'KRATOS' with types [LOGS]: "
                                     + "secret is missing required fields [collectorId]"),
                Arguments.of(TelemetryProviderEnum.KRATOS, LOGS,
                             object("collectorId", " ", "clientCert", "cert", "clientKey", "key"),
                             "Telemetry provider 'KRATOS' with types [LOGS]: "
                                     + "secret is missing required fields [collectorId]"),
                Arguments.of(TelemetryProviderEnum.KRATOS, LOGS, object("collectorId", "id"),
                             "with types [LOGS]: "
                                     + "secret is missing required fields [clientCert, clientKey]"),
                Arguments.of(TelemetryProviderEnum.KRATOS, TRACES, object("collectorId", "id"),
                             "with types [TRACES]: "
                                     + "secret is missing required fields [clientCert, clientKey]"),
                Arguments.of(TelemetryProviderEnum.KRATOS, ALL_TYPES, object("clientCert", "cert"),
                             "with types [LOGS, TRACES]: secret is missing required fields "
                                     + "[collectorId, clientKey]"),
                Arguments.of(TelemetryProviderEnum.AZURE_MONITOR, LOGS,
                             object("instrumentationKey", "ikey"),
                             "missing required fields [applicationId, liveEndpoint]"),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, METRICS,
                             object("clientCert", "cert"),
                             "'clientCert' and 'clientKey' must be set together"),
                Arguments.of(TelemetryProviderEnum.KRATOS_THANOS, METRICS,
                             object("clientKey", "key", "caFile", "ca"),
                             "'clientCert' and 'clientKey' must be set together"),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, METRICS, object("caCert", "ca"),
                             "needs 'clientCert' and 'clientKey', 'caFile', or both"),
                Arguments.of(TelemetryProviderEnum.PROMETHEUS, METRICS, new StringNode("token"),
                             "with types [METRICS]: secret value must be a JSON object"),
                // Every secret must be extractable by the collector, whatever its types.
                Arguments.of(TelemetryProviderEnum.KRATOS, METRICS,
                             JSON_MAPPER.createObjectNode().put("clientCert", "cert")
                                     .set("nested", object("key", "value")),
                             "secret field 'nested' must be a string"),
                Arguments.of(TelemetryProviderEnum.SPLUNK, METRICS,
                             JSON_MAPPER.createArrayNode().add("token"),
                             "must be a string or a JSON object"));
    }

    @ParameterizedTest
    @MethodSource("invalidSecrets")
    void shouldRejectSecretShapeTheCollectorCannotRender(
            TelemetryProviderEnum provider, Set<TelemetryTypeEnum> types, JsonNode value,
            String expectedMessage) {
        assertThatThrownBy(() -> TelemetryProviderValidator.validateSecretValue(
                provider, types, value))
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
