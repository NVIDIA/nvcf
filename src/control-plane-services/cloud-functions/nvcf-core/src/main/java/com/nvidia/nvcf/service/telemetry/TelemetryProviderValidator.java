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

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProviderEnum;
import java.util.ArrayList;
import java.util.EnumSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import org.apache.commons.lang3.StringUtils;
import tools.jackson.databind.JsonNode;

/**
 * Validates telemetry providers and secret values against what the BYOO OpenTelemetry
 * collector can render. The collector writes a string secret to a single credential file
 * and flattens an object secret into one credential file per key, so each provider needs
 * the secret shape its exporter configuration reads.
 */
public final class TelemetryProviderValidator {

    static final Set<TelemetryProviderEnum> SUPPORTED_PROVIDERS = EnumSet.of(
            TelemetryProviderEnum.AZURE_MONITOR,
            TelemetryProviderEnum.DATADOG,
            TelemetryProviderEnum.GRAFANA_CLOUD,
            TelemetryProviderEnum.KRATOS,
            TelemetryProviderEnum.KRATOS_THANOS,
            TelemetryProviderEnum.OTEL_COLLECTOR,
            TelemetryProviderEnum.PROMETHEUS,
            TelemetryProviderEnum.SERVICENOW,
            TelemetryProviderEnum.SPLUNK);

    private static final Set<TelemetryProviderEnum> STRING_SECRET_PROVIDERS = EnumSet.of(
            TelemetryProviderEnum.DATADOG,
            TelemetryProviderEnum.OTEL_COLLECTOR,
            TelemetryProviderEnum.SERVICENOW,
            TelemetryProviderEnum.SPLUNK);

    private static final Map<TelemetryProviderEnum, List<String>> REQUIRED_SECRET_KEYS = Map.of(
            TelemetryProviderEnum.AZURE_MONITOR,
            List.of("instrumentationKey", "applicationId", "liveEndpoint"),
            TelemetryProviderEnum.GRAFANA_CLOUD, List.of("instanceId", "apiKey"),
            TelemetryProviderEnum.KRATOS, List.of("collectorId", "clientCert", "clientKey"));

    private static final Set<TelemetryProviderEnum> REMOTE_WRITE_TLS_PROVIDERS = EnumSet.of(
            TelemetryProviderEnum.KRATOS_THANOS,
            TelemetryProviderEnum.PROMETHEUS);

    private static final String CLIENT_CERT = "clientCert";
    private static final String CLIENT_KEY = "clientKey";
    private static final String CA_FILE = "caFile";

    private static final String MESG_UNSUPPORTED_PROVIDER =
            "Telemetry provider '%s' is not supported. Supported providers: %s";
    private static final String MESG_SECRET_MUST_BE_STRING =
            "Telemetry provider '%s': secret value must be a non-empty string";
    private static final String MESG_SECRET_MUST_BE_OBJECT =
            "Telemetry provider '%s': secret value must be a JSON object";
    private static final String MESG_SECRET_FIELD_NOT_STRING =
            "Telemetry provider '%s': secret field '%s' must be a string";
    private static final String MESG_SECRET_MISSING_FIELDS =
            "Telemetry provider '%s': secret is missing required fields %s";
    private static final String MESG_SECRET_TLS_PAIR =
            "Telemetry provider '%s': secret fields 'clientCert' and 'clientKey' must be set together";
    private static final String MESG_SECRET_TLS_MISSING =
            "Telemetry provider '%s': secret needs 'clientCert' and 'clientKey', 'caFile', or both";

    private TelemetryProviderValidator() {
    }

    public static String supportedProviders() {
        return SUPPORTED_PROVIDERS.stream()
                .map(TelemetryProviderEnum::toString)
                .sorted()
                .collect(Collectors.joining(", "));
    }

    public static void validateProvider(TelemetryProviderEnum provider) {
        if (!SUPPORTED_PROVIDERS.contains(provider)) {
            throw new BadRequestException(
                    MESG_UNSUPPORTED_PROVIDER.formatted(provider, supportedProviders()));
        }
    }

    public static void validateSecretValue(TelemetryProviderEnum provider, JsonNode value) {
        validateProvider(provider);
        if (STRING_SECRET_PROVIDERS.contains(provider)) {
            if (value == null || !value.isString() || StringUtils.isBlank(value.asString())) {
                throw new BadRequestException(MESG_SECRET_MUST_BE_STRING.formatted(provider));
            }
            return;
        }

        if (value == null || !value.isObject()) {
            throw new BadRequestException(MESG_SECRET_MUST_BE_OBJECT.formatted(provider));
        }
        for (var entry : value.properties()) {
            if (!entry.getValue().isString()) {
                throw new BadRequestException(
                        MESG_SECRET_FIELD_NOT_STRING.formatted(provider, entry.getKey()));
            }
        }

        if (REMOTE_WRITE_TLS_PROVIDERS.contains(provider)) {
            validateRemoteWriteTls(provider, value);
            return;
        }

        var missing = new ArrayList<String>();
        for (var key : REQUIRED_SECRET_KEYS.get(provider)) {
            if (!hasContent(value, key)) {
                missing.add(key);
            }
        }
        if (!missing.isEmpty()) {
            throw new BadRequestException(MESG_SECRET_MISSING_FIELDS.formatted(provider, missing));
        }
    }

    private static void validateRemoteWriteTls(TelemetryProviderEnum provider, JsonNode value) {
        var hasCert = hasContent(value, CLIENT_CERT);
        var hasKey = hasContent(value, CLIENT_KEY);
        if (hasCert != hasKey) {
            throw new BadRequestException(MESG_SECRET_TLS_PAIR.formatted(provider));
        }
        if (!hasCert && !hasContent(value, CA_FILE)) {
            throw new BadRequestException(MESG_SECRET_TLS_MISSING.formatted(provider));
        }
    }

    private static boolean hasContent(JsonNode value, String key) {
        var field = value.get(key);
        return field != null && field.isString() && StringUtils.isNotBlank(field.asString());
    }
}
