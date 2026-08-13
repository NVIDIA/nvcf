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
package com.nvidia.nvcf.rest.telemetry;

import com.nvidia.nvcf.rest.telemetry.dto.ListTelemetryResponse;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryRequest;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryResponse;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.telemetry.TelemetryLookupService;
import com.nvidia.nvcf.service.telemetry.TelemetryMapperService;
import com.nvidia.nvcf.service.telemetry.TelemetryService;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TelemetryFacade {

    private static final String MESG_TELEMETRY_CREATED =
            "Account '%s': Created Telemetry '%s'";
    private static final String MESG_TELEMETRY_DELETED =
            "Account '%s': Deleted Telemetry '%s'";

    private final TelemetryService telemetryService;
    private final TelemetryMapperService telemetryMapperService;
    private final TelemetryLookupService telemetryLookupService;
    private final AccountService accountService;

    public TelemetryResponse createTelemetry(String ncaId, TelemetryRequest telemetryRequest) {
        var accountEntity = accountService.getAccount(ncaId);  // Validate the presence of account.
        var telemetryEntity = telemetryService.saveTelemetry(accountEntity, telemetryRequest);
        var telemetryDto = telemetryMapperService.toTelemetryDto(telemetryEntity);
        log.info(MESG_TELEMETRY_CREATED.formatted(ncaId, telemetryDto.telemetryId()));
        return new TelemetryResponse(telemetryDto);
    }

    public TelemetryResponse getTelemetry(String ncaId, UUID telemetryId) {
        var telemetryEntity = telemetryLookupService
                .lookupByAccountAndTelemetryIdOrThrow(ncaId, telemetryId);
        var telemetryDto = telemetryMapperService.toTelemetryDto(telemetryEntity);
        return new TelemetryResponse(telemetryDto);
    }

    public ListTelemetryResponse listTelemetries(String ncaId) {
        var telemetryDtos = telemetryLookupService.lookupByAccount(ncaId)
                .map(telemetryMapperService::toTelemetryDto)
                .toList();
        return ListTelemetryResponse.builder()
                .telemetries(telemetryDtos)
                .build();
    }

    public void deleteTelemetry(String ncaId, UUID telemetryId) {
        telemetryService.deleteTelemetry(ncaId, telemetryId);
        log.info(MESG_TELEMETRY_DELETED.formatted(telemetryId, ncaId));
    }
}
