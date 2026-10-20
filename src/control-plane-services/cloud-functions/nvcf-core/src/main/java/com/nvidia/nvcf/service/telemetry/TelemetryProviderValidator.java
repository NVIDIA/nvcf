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
 * Validates telemetry providers, types, and secret values against what the BYOO OpenTelemetry
 * collector can render (byoo-otel-collector/internal/otelconfig/render.go). The collector
 * renders an exporter only for the provider and signal pairs in {@link #SUPPORTED_TYPES}. It
 * writes a string secret to a single credential file and flattens an object of string fields
 * into one credential file per key, so every secret must have one of those shapes.
 */
public final class TelemetryProviderValidator {

    private static final String CLIENT_CERT = "clientCert";
    private static final String CLIENT_KEY = "clientKey";
    private static final String CA_FILE = "caFile";

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

    private static final Set<TelemetryProviderEnum> STRING_SECRET_PROVIDERS = EnumSet.of(
            TelemetryProviderEnum.DATADOG,
            TelemetryProviderEnum.OTEL_COLLECTOR,
            TelemetryProviderEnum.SERVICENOW,
            TelemetryProviderEnum.SPLUNK);

    // Object fields each signal's exporter requires. The KRATOS traces exporter authenticates
    // with the client certificate pair alone and also reads caFile and collectorId when set.
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
    private static final String MESG_UNSUPPORTED_TYPES =
            "Telemetry provider '%s' does not support types %s. Supported types: %s";
    private static final String MESG_UNSUPPORTED_REFERENCED_TYPE =
            "Telemetry '%s': provider '%s' does not support %s. Supported types: %s";
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

    /**
     * Validates a telemetry that a function or task references for one signal type.
     */
    public static void validateReference(UUID telemetryId,
                                         TelemetryTypeEnum type,
                                         TelemetryProviderEnum provider) {
        if (!SUPPORTED_PROVIDERS.contains(provider)) {
            throw new BadRequestException(MESG_UNSUPPORTED_REFERENCED_PROVIDER.formatted(
                    telemetryId, type, provider, supportedProviders()));
        }
        var supportedTypes = SUPPORTED_TYPES.get(provider);
        if (!supportedTypes.contains(type)) {
            throw new BadRequestException(MESG_UNSUPPORTED_REFERENCED_TYPE.formatted(
                    telemetryId, provider, type, supportedTypes));
        }
    }

    public static void validateSecretValue(TelemetryProviderEnum provider,
                                           Set<TelemetryTypeEnum> types,
                                           JsonNode value) {
        validateProvider(provider);
        var requestedTypes = EnumSet.noneOf(TelemetryTypeEnum.class);
        if (types != null) {
            requestedTypes.addAll(types);
        }
        validateTypes(provider, requestedTypes);
        validateExtractable(provider, value);
        if (requestedTypes.isEmpty()) {
            return;
        }

        if (STRING_SECRET_PROVIDERS.contains(provider)) {
            if (!value.isString() || StringUtils.isBlank(value.asString())) {
                throw new BadRequestException(
                        MESG_SECRET_MUST_BE_STRING.formatted(provider, requestedTypes));
            }
            return;
        }

        if (!value.isObject()) {
            throw new BadRequestException(
                    MESG_SECRET_MUST_BE_OBJECT.formatted(provider, requestedTypes));
        }

        if (REMOTE_WRITE_TLS_PROVIDERS.contains(provider)) {
            validateRemoteWriteTls(provider, requestedTypes, value);
            return;
        }

        var missing = new LinkedHashSet<String>();
        var typesNeedingMissing = EnumSet.noneOf(TelemetryTypeEnum.class);
        var keysByType = REQUIRED_SECRET_KEYS.get(provider);
        for (var type : requestedTypes) {
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

    private static void validateTypes(TelemetryProviderEnum provider,
                                      Set<TelemetryTypeEnum> types) {
        var supportedTypes = SUPPORTED_TYPES.get(provider);
        var unsupportedTypes = EnumSet.noneOf(TelemetryTypeEnum.class);
        unsupportedTypes.addAll(types);
        unsupportedTypes.removeAll(supportedTypes);
        if (!unsupportedTypes.isEmpty()) {
            throw new BadRequestException(MESG_UNSUPPORTED_TYPES.formatted(
                    provider, unsupportedTypes, supportedTypes));
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
                                               Set<TelemetryTypeEnum> requestedTypes,
                                               JsonNode value) {
        var hasCert = hasContent(value, CLIENT_CERT);
        var hasKey = hasContent(value, CLIENT_KEY);
        if (hasCert != hasKey) {
            throw new BadRequestException(MESG_SECRET_TLS_PAIR.formatted(provider, requestedTypes));
        }
        if (!hasCert && !hasContent(value, CA_FILE)) {
            throw new BadRequestException(
                    MESG_SECRET_TLS_MISSING.formatted(provider, requestedTypes));
        }
    }

    private static boolean hasContent(JsonNode value, String key) {
        var field = value.get(key);
        return field != null && field.isString() && StringUtils.isNotBlank(field.asString());
    }
}
