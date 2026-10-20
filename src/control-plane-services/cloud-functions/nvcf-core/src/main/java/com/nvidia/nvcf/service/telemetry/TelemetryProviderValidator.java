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
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryTypeEnum;
import java.util.EnumSet;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import org.apache.commons.lang3.StringUtils;
import tools.jackson.databind.JsonNode;

/**
 * Validates telemetry providers and secret values against what the BYOO OpenTelemetry
 * collector can render. The collector writes a string secret to a single credential file
 * and flattens an object of string fields into one credential file per key, so every secret
 * must have one of those shapes. Beyond that, a provider's secret rule applies only when the
 * telemetry's types include a signal whose exporter reads the secret, because the control
 * plane also registers endpoints for signals the collector does not export for that provider.
 */
public final class TelemetryProviderValidator {

    private static final String CLIENT_CERT = "clientCert";
    private static final String CLIENT_KEY = "clientKey";
    private static final String CA_FILE = "caFile";

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

    // Signals whose collector exporter reads the provider's secret.
    private static final Map<TelemetryProviderEnum, Set<TelemetryTypeEnum>> SECRET_READ_BY_TYPES =
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

    private static final Set<TelemetryProviderEnum> STRING_SECRET_PROVIDERS = EnumSet.of(
            TelemetryProviderEnum.DATADOG,
            TelemetryProviderEnum.OTEL_COLLECTOR,
            TelemetryProviderEnum.SERVICENOW,
            TelemetryProviderEnum.SPLUNK);

    // Object fields each signal's exporter reads. KRATOS sends the collector ID header only
    // for logs; its traces exporter authenticates with the client certificate pair alone.
    private static final Map<TelemetryProviderEnum, Map<TelemetryTypeEnum, List<String>>>
            REQUIRED_SECRET_KEYS = Map.of(
                    TelemetryProviderEnum.AZURE_MONITOR,
                    forAllTypes(List.of("instrumentationKey", "applicationId", "liveEndpoint")),
                    TelemetryProviderEnum.GRAFANA_CLOUD,
                    forAllTypes(List.of("instanceId", "apiKey")),
                    TelemetryProviderEnum.KRATOS,
                    Map.of(TelemetryTypeEnum.LOGS,
                           List.of("collectorId", CLIENT_CERT, CLIENT_KEY),
                           TelemetryTypeEnum.TRACES,
                           List.of(CLIENT_CERT, CLIENT_KEY)));

    private static final Set<TelemetryProviderEnum> REMOTE_WRITE_TLS_PROVIDERS = EnumSet.of(
            TelemetryProviderEnum.KRATOS_THANOS,
            TelemetryProviderEnum.PROMETHEUS);

    private static final String MESG_UNSUPPORTED_PROVIDER =
            "Telemetry provider '%s' is not supported. Supported providers: %s";
    private static final String MESG_UNSUPPORTED_REFERENCED_PROVIDER =
            "Telemetry '%s' for %s: provider '%s' is not supported. Supported providers: %s";
    private static final String MESG_SECRET_NOT_EXTRACTABLE =
            "Telemetry provider '%s': secret value must be a string or a JSON object";
    private static final String MESG_SECRET_FIELD_NOT_STRING =
            "Telemetry provider '%s': secret field '%s' must be a string";
    private static final String MESG_SECRET_MUST_BE_STRING =
            "Telemetry provider '%s' with types %s: secret value must be a non-empty string";
    private static final String MESG_SECRET_MUST_BE_OBJECT =
            "Telemetry provider '%s' with types %s: secret value must be a JSON object";
    private static final String MESG_SECRET_MISSING_FIELDS =
            "Telemetry provider '%s' with types %s: secret is missing required fields %s";
    private static final String MESG_SECRET_TLS_PAIR =
            "Telemetry provider '%s' with types %s: secret fields 'clientCert' and 'clientKey' "
                    + "must be set together";
    private static final String MESG_SECRET_TLS_MISSING =
            "Telemetry provider '%s' with types %s: secret needs 'clientCert' and 'clientKey', "
                    + "'caFile', or both";

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

    public static void validateProvider(UUID telemetryId,
                                        TelemetryTypeEnum type,
                                        TelemetryProviderEnum provider) {
        if (!SUPPORTED_PROVIDERS.contains(provider)) {
            throw new BadRequestException(MESG_UNSUPPORTED_REFERENCED_PROVIDER.formatted(
                    telemetryId, type, provider, supportedProviders()));
        }
    }

    public static void validateSecretValue(TelemetryProviderEnum provider,
                                           Set<TelemetryTypeEnum> types,
                                           JsonNode value) {
        validateProvider(provider);
        validateExtractable(provider, value);

        var readByTypes = EnumSet.noneOf(TelemetryTypeEnum.class);
        if (types != null) {
            readByTypes.addAll(types);
        }
        readByTypes.retainAll(SECRET_READ_BY_TYPES.get(provider));
        if (readByTypes.isEmpty()) {
            return;
        }

        if (STRING_SECRET_PROVIDERS.contains(provider)) {
            if (!value.isString() || StringUtils.isBlank(value.asString())) {
                throw new BadRequestException(
                        MESG_SECRET_MUST_BE_STRING.formatted(provider, readByTypes));
            }
            return;
        }

        if (!value.isObject()) {
            throw new BadRequestException(
                    MESG_SECRET_MUST_BE_OBJECT.formatted(provider, readByTypes));
        }

        if (REMOTE_WRITE_TLS_PROVIDERS.contains(provider)) {
            validateRemoteWriteTls(provider, readByTypes, value);
            return;
        }

        var missing = new LinkedHashSet<String>();
        var typesNeedingMissing = EnumSet.noneOf(TelemetryTypeEnum.class);
        var keysByType = REQUIRED_SECRET_KEYS.get(provider);
        for (var type : readByTypes) {
            for (var key : keysByType.getOrDefault(type, List.of())) {
                if (!hasContent(value, key)) {
                    missing.add(key);
                    typesNeedingMissing.add(type);
                }
            }
        }
        if (!missing.isEmpty()) {
            throw new BadRequestException(MESG_SECRET_MISSING_FIELDS.formatted(
                    provider, typesNeedingMissing, List.copyOf(missing)));
        }
    }

    private static Map<TelemetryTypeEnum, List<String>> forAllTypes(List<String> keys) {
        return EnumSet.allOf(TelemetryTypeEnum.class).stream()
                .collect(Collectors.toMap(type -> type, type -> keys));
    }

    private static void validateExtractable(TelemetryProviderEnum provider, JsonNode value) {
        if (value == null || !(value.isString() || value.isObject())) {
            throw new BadRequestException(MESG_SECRET_NOT_EXTRACTABLE.formatted(provider));
        }
        if (value.isString()) {
            return;
        }
        for (var entry : value.properties()) {
            if (!entry.getValue().isString()) {
                throw new BadRequestException(
                        MESG_SECRET_FIELD_NOT_STRING.formatted(provider, entry.getKey()));
            }
        }
    }

    private static void validateRemoteWriteTls(TelemetryProviderEnum provider,
                                               Set<TelemetryTypeEnum> readByTypes,
                                               JsonNode value) {
        var hasCert = hasContent(value, CLIENT_CERT);
        var hasKey = hasContent(value, CLIENT_KEY);
        if (hasCert != hasKey) {
            throw new BadRequestException(MESG_SECRET_TLS_PAIR.formatted(provider, readByTypes));
        }
        if (!hasCert && !hasContent(value, CA_FILE)) {
            throw new BadRequestException(
                    MESG_SECRET_TLS_MISSING.formatted(provider, readByTypes));
        }
    }

    private static boolean hasContent(JsonNode value, String key) {
        var field = value.get(key);
        return field != null && field.isString() && StringUtils.isNotBlank(field.asString());
    }
}
