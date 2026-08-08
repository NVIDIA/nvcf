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

import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;

import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.persistence.telemetry.TelemetriesByAccountRepository;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetriesUdt;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryByAccountEntity;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryByAccountKey;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.service.telemetry.TelemetryService;
import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.Set;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TestTelemetryService {

    @Autowired
    private TelemetriesByAccountRepository telemetriesByAccountRepository;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TelemetryService telemetryService;

    public void createTelemetry(
            String ncaId,
            UUID telemetryId,
            String endpoint,
            TelemetryProtocol protocol,
            TelemetryProvider provider,
            Set<TelemetryType> types,
            SecretDto secret) {
        var key = TelemetryByAccountKey.builder()
                .ncaId(ncaId)
                .telemetryId(telemetryId)
                .build();
        var telemetryByAccountEntity = TelemetryByAccountEntity.builder()
                .key(key)
                .name(secret.name())
                .endpoint(endpoint)
                .protocol(protocol)
                .provider(provider)
                .types(types)
                .build();

        telemetryService.saveTelemetrySecret(ncaId, telemetryId, secret);

        telemetriesByAccountRepository.save(telemetryByAccountEntity);
    }

    public void deleteAllTelemetries() {
        telemetriesByAccountRepository.deleteAll();
    }

    public FunctionEntity createTestFunctionEntityForTelemetry(
            UUID id,
            UUID versionId,
            String ncaId,
            String name,
            FunctionStatus status,
            @Nullable Set<ResourceUdt> resources,
            UUID logsTelemetry,
            UUID traceTelemetry,
            UUID metricsTelemetry) {
        var telemetries = TelemetriesUdt.builder()
                .tracesTelemetryId(traceTelemetry)
                .logsTelemetryId(logsTelemetry)
                .metricsTelemetryId(metricsTelemetry)
                .build();
        var entity = FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(status)
                .ncaId(ncaId)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .resources(resources)
                .utilsContainerImage(GO)
                .createdAt(Instant.now())
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .telemetries(telemetries)
                .build();
        functionsRepository.save(entity);
        return entity;
    }
}
