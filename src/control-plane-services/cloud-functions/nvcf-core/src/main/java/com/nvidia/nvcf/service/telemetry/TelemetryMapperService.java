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

import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryByAccountEntity;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryByAccountKey;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryDto;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProtocolEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProviderEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryRequest;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryTypeEnum;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TelemetryMapperService {

    public TelemetryDto toTelemetryDto(TelemetryByAccountEntity entity) {
        return TelemetryDto.builder()
                .telemetryId(entity.getKey().getTelemetryId())
                .name(entity.getName())
                .endpoint(entity.getEndpoint())
                .provider(toTelemetryProviderEnum(entity.getProvider()))
                .types(toTelemetryTypeEnums(entity.getTypes()))
                .protocol(toTelemetryProtocolEnum(entity.getProtocol()))
                .createdAt(entity.getCreatedAt())
                .build();
    }

    public List<TelemetryDto> toTelemetryDtos(Stream<TelemetryByAccountEntity> telemetryEntities) {
        if (telemetryEntities == null) {
            return List.of(); // Return an empty list if input is null or empty
        }

        return telemetryEntities.map(this::toTelemetryDto).toList();
    }

    public TelemetryByAccountEntity toTelemetryByAccountEntity(
            String ncaId,
            UUID telemetryId,
            TelemetryRequest telemetryRequest) {
        var key = TelemetryByAccountKey.builder()
                .ncaId(ncaId)
                .telemetryId(telemetryId)
                .build();
        return TelemetryByAccountEntity.builder()
                .key(key)
                .name(telemetryRequest.secret().name())
                .endpoint(telemetryRequest.endpoint())
                .provider(toTelemetryProvider(telemetryRequest.provider()))
                .types(toTelemetryTypes(telemetryRequest.types()))
                .protocol(toTelemetryProtocol(telemetryRequest.protocol()))
                .build();
    }

    public TelemetryProtocolEnum toTelemetryProtocolEnum(TelemetryProtocol protocol) {
        return switch (protocol) {
            case HTTP -> TelemetryProtocolEnum.HTTP;
            case GRPC -> TelemetryProtocolEnum.GRPC;
        };
    }

    public TelemetryProviderEnum toTelemetryProviderEnum(TelemetryProvider provider) {
        return switch (provider) {
            case PROMETHEUS -> TelemetryProviderEnum.PROMETHEUS;
            case GRAFANA_CLOUD -> TelemetryProviderEnum.GRAFANA_CLOUD;
            case SPLUNK -> TelemetryProviderEnum.SPLUNK;
            case DATADOG -> TelemetryProviderEnum.DATADOG;
            case SERVICENOW -> TelemetryProviderEnum.SERVICENOW;
            case KRATOS -> TelemetryProviderEnum.KRATOS;
            case KRATOS_THANOS -> TelemetryProviderEnum.KRATOS_THANOS;
            case TIMESTREAM -> TelemetryProviderEnum.TIMESTREAM;
            case VICTORIAMETRICS -> TelemetryProviderEnum.VICTORIAMETRICS;
            case AZURE_MONITOR -> TelemetryProviderEnum.AZURE_MONITOR;
            case OTEL_COLLECTOR -> TelemetryProviderEnum.OTEL_COLLECTOR;
        };
    }

    public Set<TelemetryTypeEnum> toTelemetryTypeEnums(Set<TelemetryType> types) {
        return types.stream()
                .map(this::toTelemetryTypeEnum)
                .collect(Collectors.toSet());
    }

    private TelemetryProtocol toTelemetryProtocol(TelemetryProtocolEnum protocolEnum) {
        return switch (protocolEnum) {
            case HTTP -> TelemetryProtocol.HTTP;
            case GRPC -> TelemetryProtocol.GRPC;
        };
    }

    private TelemetryProvider toTelemetryProvider(TelemetryProviderEnum providerEnum) {
        return switch (providerEnum) {
            case PROMETHEUS -> TelemetryProvider.PROMETHEUS;
            case GRAFANA_CLOUD -> TelemetryProvider.GRAFANA_CLOUD;
            case SPLUNK -> TelemetryProvider.SPLUNK;
            case DATADOG -> TelemetryProvider.DATADOG;
            case SERVICENOW -> TelemetryProvider.SERVICENOW;
            case KRATOS -> TelemetryProvider.KRATOS;
            case KRATOS_THANOS -> TelemetryProvider.KRATOS_THANOS;
            case TIMESTREAM -> TelemetryProvider.TIMESTREAM;
            case VICTORIAMETRICS -> TelemetryProvider.VICTORIAMETRICS;
            case AZURE_MONITOR -> TelemetryProvider.AZURE_MONITOR;
            case OTEL_COLLECTOR -> TelemetryProvider.OTEL_COLLECTOR;
        };
    }

    private Set<TelemetryType> toTelemetryTypes(Set<TelemetryTypeEnum> types) {
        return types.stream()
                .map(this::toTelemetryType)
                .collect(Collectors.toSet());
    }

    private TelemetryType toTelemetryType(TelemetryTypeEnum typeEnum) {
        return switch (typeEnum) {
            case LOGS -> TelemetryType.LOGS;
            case METRICS -> TelemetryType.METRICS;
            case TRACES -> TelemetryType.TRACES;
        };
    }

    private TelemetryTypeEnum toTelemetryTypeEnum(TelemetryType type) {
        return switch (type) {
            case LOGS -> TelemetryTypeEnum.LOGS;
            case METRICS -> TelemetryTypeEnum.METRICS;
            case TRACES -> TelemetryTypeEnum.TRACES;
        };
    }

}
