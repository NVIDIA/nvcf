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
package com.nvidia.nvcf.service.function;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.assertj.core.api.Assertions.catchThrowableOfType;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryByAccountEntity;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetriesDto;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.instance.InstanceService;
import com.nvidia.nvcf.service.registry.RegistryArtifactService;
import com.nvidia.nvcf.service.telemetry.TelemetryAvailability;
import com.nvidia.nvcf.service.telemetry.TelemetryLookupService;
import com.nvidia.nvcf.service.telemetry.TelemetryMapperService;
import java.net.URI;
import java.util.Arrays;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.http.HttpStatus;
import tools.jackson.databind.json.JsonMapper;

@ExtendWith(MockitoExtension.class)
class FunctionManagementTelemetryTest {

    private static final String NCA_ID = "nca-test";
    private static final UUID FUNCTION_ID = UUID.randomUUID();
    private static final UUID VERSION_ID = UUID.randomUUID();
    private static final UUID TELEMETRY_ID = UUID.randomUUID();

    @Mock
    private AccountService accountService;
    @Mock
    private AuthorizedPartiesService azpsService;
    @Mock
    private FunctionAuditService auditService;
    @Mock
    private FunctionLookupService functionLookupService;
    @Mock
    private FunctionMapperService functionMapperService;
    @Mock
    private FunctionLlmService functionLlmService;
    @Mock
    private InstanceService instanceService;
    @Mock
    private FunctionDeploymentService deploymentService;
    @Mock
    private EssService essService;
    @Mock
    private TelemetryLookupService telemetryLookupService;
    @Mock
    private FunctionsRepository repository;
    @Mock
    private RegistryArtifactService registryArtifactService;

    @BeforeEach
    void setUp() {
        var account = new AccountEntity();
        account.setMaxFunctionsAllowed(10);
        when(accountService.getAccount(NCA_ID)).thenReturn(account);
    }

    @ParameterizedTest
    @MethodSource("telemetryCases")
    void disabledRejectsEverySignalBeforeWriting(boolean newVersion, TelemetryType type) {
        assertRejected(newVersion, telemetries(type));
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void disabledRejectsEmptyTelemetries(boolean newVersion) {
        assertRejected(newVersion, new TelemetriesDto(null, null, null));
    }

    @ParameterizedTest
    @MethodSource("telemetryCases")
    void enabledValidatesAndSavesTelemetryReferences(boolean newVersion, TelemetryType type) {
        var telemetry = new TelemetryByAccountEntity();
        telemetry.setTypes(Set.of(type));
        telemetry.setProvider(TelemetryProvider.DATADOG);
        when(telemetryLookupService.lookupByAccountAndTelemetryIdOrThrow(NCA_ID, TELEMETRY_ID))
                .thenReturn(telemetry);

        assertSaved(true, newVersion, telemetries(type));

        verify(telemetryLookupService).lookupByAccountAndTelemetryIdOrThrow(NCA_ID, TELEMETRY_ID);
    }

    @ParameterizedTest
    @MethodSource("modeAndVersionCases")
    void functionsWithoutTelemetriesWorkInEitherMode(boolean enabled, boolean newVersion) {
        assertSaved(enabled, newVersion, null);
        verifyNoInteractions(telemetryLookupService);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void enabledAcceptsEmptyTelemetries(boolean newVersion) {
        assertSaved(true, newVersion, new TelemetriesDto(null, null, null));
        verifyNoInteractions(telemetryLookupService);
    }

    @Test
    void enabledStillRejectsWrongTelemetryType() {
        var telemetry = new TelemetryByAccountEntity();
        telemetry.setTypes(Set.of(TelemetryType.METRICS));
        when(telemetryLookupService.lookupByAccountAndTelemetryIdOrThrow(NCA_ID, TELEMETRY_ID))
                .thenReturn(telemetry);

        assertThatThrownBy(() -> register(true, false, request(telemetries(TelemetryType.LOGS))))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("Invalid telemetry type");
        verify(repository, never()).save(any(FunctionEntity.class));
        verifyNoInteractions(essService, auditService, functionMapperService);
    }

    private void assertRejected(boolean newVersion, TelemetriesDto telemetries) {
        var error = catchThrowableOfType(BadRequestException.class,
                () -> register(false, newVersion, request(telemetries)));

        assertThat(error).isNotNull();
        assertThat(error.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(error.getBody().getDetail())
                .isEqualTo("telemetry is not supported on this deployment");
        verify(repository, never()).save(any(FunctionEntity.class));
        verifyNoInteractions(telemetryLookupService, functionMapperService, functionLlmService,
                registryArtifactService, essService, auditService);
    }

    private void assertSaved(boolean enabled, boolean newVersion, TelemetriesDto telemetries) {
        var request = request(telemetries);
        var optFunctionId = newVersion ? Optional.of(FUNCTION_ID) : Optional.<UUID>empty();
        var entity = FunctionEntity.builder()
                .functionId(FUNCTION_ID)
                .functionVersionId(VERSION_ID)
                .ncaId(NCA_ID)
                .functionName("test-function")
                .functionStatus(FunctionStatus.INACTIVE)
                .functionType(FunctionType.DEFAULT)
                .inferenceUrl("/infer")
                .apiBodyFormat(ApiBodyFormat.CUSTOM)
                .build();
        when(functionMapperService.toFunctionEntity(NCA_ID, optFunctionId, request, null))
                .thenReturn(entity);
        if (newVersion) {
            when(repository.findAllByFunctionId(FUNCTION_ID)).thenReturn(Stream.empty());
            when(functionLookupService.lookupUsingFunctionId(FUNCTION_ID)).thenReturn(List.of());
        }

        register(enabled, newVersion, request);

        verify(repository).save(entity);
        verify(functionMapperService).toFunctionDto(entity, Optional.empty(), Optional.empty());
        verify(auditService).auditFunctionCreate(any(AuditEventPayload.Builder.class),
                eq(entity));
    }

    private void register(boolean enabled, boolean newVersion, CreateFunctionRequest request) {
        var service = new FunctionManagementService(accountService, azpsService, auditService,
                functionLookupService, functionMapperService, functionLlmService,
                JsonMapper.builder().build(), instanceService, deploymentService, essService,
                new TelemetryAvailability(enabled), telemetryLookupService,
                new TelemetryMapperService(), repository, registryArtifactService);
        service.registerFunction(NCA_ID, newVersion ? Optional.of(FUNCTION_ID) : Optional.empty(),
                request, () -> true, AuditEventPayload.builder());
    }

    private CreateFunctionRequest request(TelemetriesDto telemetries) {
        return CreateFunctionRequest.builder()
                .name("test-function")
                .inferenceUrl(URI.create("/infer"))
                .telemetries(telemetries)
                .build();
    }

    private static TelemetriesDto telemetries(TelemetryType type) {
        return switch (type) {
            case LOGS -> new TelemetriesDto(TELEMETRY_ID, null, null);
            case METRICS -> new TelemetriesDto(null, TELEMETRY_ID, null);
            case TRACES -> new TelemetriesDto(null, null, TELEMETRY_ID);
        };
    }

    private static Stream<Arguments> telemetryCases() {
        return Stream.of(false, true).flatMap(newVersion -> Arrays.stream(TelemetryType.values())
                .map(type -> Arguments.of(newVersion, type)));
    }

    private static Stream<Arguments> modeAndVersionCases() {
        return Stream.of(false, true).flatMap(enabled -> Stream.of(false, true)
                .map(newVersion -> Arguments.of(enabled, newVersion)));
    }
}
