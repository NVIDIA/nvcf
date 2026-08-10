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

import static java.lang.String.format;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.telemetry.TelemetriesByAccountRepository;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryByAccountEntity;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TelemetryLookupService {
    private static final String MESG_TELEMETRY_NOT_FOUND_ACCOUNT =
            "Account '%s' Telemetry '%s': Not found";

    private final TelemetriesByAccountRepository telemetryByAccountRepository;

    public TelemetryByAccountEntity lookupByAccountAndTelemetryIdOrThrow(
            String ncaId,
            UUID telemetryId) {
        return telemetryByAccountRepository.findByKeyNcaIdAndKeyTelemetryId(ncaId, telemetryId)
                .orElseThrow(() -> {
                    var message = format(MESG_TELEMETRY_NOT_FOUND_ACCOUNT, ncaId, telemetryId);
                    log.debug(message);
                    return new NotFoundException(message);
                });
    }

    public Stream<TelemetryByAccountEntity> lookupByAccount(String ncaId) {
        return telemetryByAccountRepository.findByKeyNcaId(ncaId);
    }
}
