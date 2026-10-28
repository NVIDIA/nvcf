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
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.assertj.core.api.Assertions.catchThrowableOfType;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
import com.nvidia.nvcf.persistence.telemetry.TelemetriesByAccountRepository;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProtocolEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProviderEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryRequest;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryTypeEnum;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.http.HttpStatus;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;

@ExtendWith(MockitoExtension.class)
class TelemetryServiceAvailabilityTest {

    private static final String NCA_ID = "nca-test";

    @Mock
    private EssService essService;
    @Mock
    private TelemetriesByAccountRepository repository;
    @Mock
    private TelemetryLookupService telemetryLookupService;
    @Mock
    private FunctionLookupService functionLookupService;

    private final TelemetryMapperService mapper = new TelemetryMapperService();

    @ParameterizedTest
    @EnumSource(TelemetryTypeEnum.class)
    void disabledRejectsCreationBeforeStorageOrSecrets(TelemetryTypeEnum type) {
        var account = new AccountEntity();
        account.setNcaId(NCA_ID);
        account.setMaxTelemetriesAllowed(10);

        var error = catchThrowableOfType(BadRequestException.class,
                () -> service(false).saveTelemetry(account, request(type)));

        assertThat(error).isNotNull();
        assertThat(error.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(error.getBody().getDetail())
                .isEqualTo("telemetry is not supported on this deployment");
        verifyNoInteractions(repository, telemetryLookupService, functionLookupService, essService);
    }

    @ParameterizedTest
    @EnumSource(TelemetryTypeEnum.class)
    void enabledSavesTelemetryAndSecret(TelemetryTypeEnum type) {
        var account = new AccountEntity();
        account.setNcaId(NCA_ID);
        account.setMaxTelemetriesAllowed(10);
        var request = request(type);
        when(telemetryLookupService.lookupByAccount(NCA_ID)).thenReturn(Stream.empty());

        var saved = service(true).saveTelemetry(account, request);

        assertThat(saved.getKey().getNcaId()).isEqualTo(NCA_ID);
        assertThat(saved.getKey().getTelemetryId()).isNotNull();
        assertThat(saved.getName()).isEqualTo(request.secret().name());
        assertThat(mapper.toTelemetryTypeEnums(saved.getTypes())).containsExactly(type);
        verify(repository).save(saved);
        verify(essService).saveTelemetrySecret(
                NCA_ID, saved.getKey().getTelemetryId(), request.secret());
    }

    @Test
    void enabledStillEnforcesAccountLimit() {
        var account = new AccountEntity();
        account.setNcaId(NCA_ID);
        account.setMaxTelemetriesAllowed(1);
        when(repository.countByKeyNcaId(NCA_ID)).thenReturn(1L);

        assertThatThrownBy(() -> service(true).saveTelemetry(
                account, request(TelemetryTypeEnum.LOGS)))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("maximum allowed telemetries (1) exceeded");
        verifyNoInteractions(essService, telemetryLookupService);
    }

    @Test
    void disabledStillAllowsDeletingUnusedTelemetry() {
        var telemetryId = UUID.randomUUID();
        var entity = mapper.toTelemetryByAccountEntity(
                NCA_ID, telemetryId, request(TelemetryTypeEnum.LOGS));
        when(telemetryLookupService.lookupByAccountAndTelemetryIdOrThrow(NCA_ID, telemetryId))
                .thenReturn(entity);
        when(functionLookupService.lookupEntitiesUsingAccountId(NCA_ID)).thenReturn(Stream.empty());

        service(false).deleteTelemetry(NCA_ID, telemetryId);

        verify(repository).delete(entity);
        verify(essService).deleteTelemetrySecret(NCA_ID, telemetryId);
    }

    private TelemetryService service(boolean enabled) {
        return new TelemetryService(new TelemetryAvailability(enabled), essService,
                JsonMapper.builder().build(), repository, mapper,
                telemetryLookupService, functionLookupService);
    }

    private TelemetryRequest request(TelemetryTypeEnum type) {
        return new TelemetryRequest("https://telemetry.example.com", TelemetryProtocolEnum.HTTP,
                TelemetryProviderEnum.DATADOG, Set.of(type),
                new SecretDto("telemetry-secret", new StringNode("test-secret")));
    }
}
