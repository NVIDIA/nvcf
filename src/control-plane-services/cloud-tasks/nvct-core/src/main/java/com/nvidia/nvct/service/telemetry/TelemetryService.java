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

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvct.persistence.task.entity.TelemetriesUdt;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.telemetry.dto.TelemetryDto;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RequiredArgsConstructor
public class TelemetryService {

    private static final String MESG_TELEMETRY_NOT_AVAILABLE =
            "Telemetry '%s': Not found in account '%s'";

    private final AccountService accountService;
    private final JsonMapper jsonMapper;

    public record Telemetries(TelemetryDto logsTelemetry,
                              TelemetryDto metricsTelemetry,
                              TelemetryDto tracesTelemetry) { }

    public record SerializeTelemetriesDto(Telemetries telemetries) { }

    @SneakyThrows
    public String base64Encode(String ncaId, TelemetriesUdt telemetriesUdt) {
        if (telemetriesUdt == null) {
            return StringUtils.EMPTY;
        }

        if (telemetriesUdt.getLogsTelemetryId() == null &&
                telemetriesUdt.getMetricsTelemetryId() == null &&
                telemetriesUdt.getTracesTelemetryId() == null) {
            return StringUtils.EMPTY;
        }

        // Serialized JSON should be as defined in section 4.2.5 of the SDD.
        var telemetriesMap = accountService.getAccountTelemetryMap(ncaId);
        var logsTelemetry = toTelemetryDto(ncaId, telemetriesMap,
                                           telemetriesUdt.getLogsTelemetryId());
        var metricsTelemetry = toTelemetryDto(ncaId, telemetriesMap,
                                              telemetriesUdt.getMetricsTelemetryId());
        var tracesTelemetry = toTelemetryDto(ncaId, telemetriesMap,
                                             telemetriesUdt.getTracesTelemetryId());
        var telemetries = new Telemetries(logsTelemetry.orElse(null),
                                          metricsTelemetry.orElse(null),
                                          tracesTelemetry.orElse(null));
        var json = jsonMapper.writeValueAsString(new SerializeTelemetriesDto(telemetries));
        return Base64.getEncoder().encodeToString(json.getBytes(StandardCharsets.UTF_8));
    }

    private Optional<TelemetryDto> toTelemetryDto(
            String ncaId,
            Map<UUID, TelemetryDto> telemetryMap,
            UUID telemetryId) {
        if (telemetryId == null) {
            return Optional.empty();
        }

        if (CollectionUtils.isEmpty(telemetryMap) || !telemetryMap.containsKey(telemetryId)) {
            var mesg = MESG_TELEMETRY_NOT_AVAILABLE.formatted(telemetryId, ncaId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        return Optional.of(telemetryMap.get(telemetryId));
    }

}
