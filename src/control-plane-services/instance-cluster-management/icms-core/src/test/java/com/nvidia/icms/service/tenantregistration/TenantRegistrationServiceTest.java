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
package com.nvidia.icms.service.tenantregistration;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.TenantConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.tenantregistration.TenantRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.tenantregistration.TenantRegistrationResponse;
import com.nvidia.icms.outbound.cassandra.tenantregistration.TenantRegistrationRepository;
import com.nvidia.icms.outbound.cassandra.tenantregistration.entity.TenantRegistrationEntity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.access.AccessDeniedException;

@ExtendWith(MockitoExtension.class)
class TenantRegistrationServiceTest {

    @Mock
    private TenantRegistrationRepository tenantRegistrationRepository;

    @Mock
    private TenantConfigurationProperties tenantConfigurationProperties;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private AppAuditService auditService;

    @Captor
    private ArgumentCaptor<TenantRegistrationEntity> entityCaptor;

    private TenantRegistrationService service;

    private static final String NCA_ID = "test-nca-id";
    private static final String TENANT = "gdn";
    private static final UUID FUNCTION_ID = UUID.randomUUID();
    private static final UUID FUNCTION_VERSION_ID = UUID.randomUUID();

    @BeforeEach
    void setUp() {
        service = new TenantRegistrationService(
                tenantRegistrationRepository,
                tenantConfigurationProperties,
                telemetryEventClient,
                auditService
        );
    }

    private static Map<String, Object> emptyAuditProps() {
        return new HashMap<>();
    }

    @Test
    void register_success() {
        // Prepare
        Map<String, String> tenantData = new HashMap<>();
        tenantData.put("gdnAppId", "test-app-id");
        tenantData.put("otherKey", "otherValue");

        TenantRegistrationRequest request = TenantRegistrationRequest.builder()
                .tenantRegistrationData(tenantData)
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .build();

        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.insert(any(TenantRegistrationEntity.class)))
                .thenAnswer(invocation -> invocation.getArgument(0));
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        TenantRegistrationResponse response = service.register(NCA_ID, TENANT, request, emptyAuditProps());

        // Assert
        assertNotNull(response);
        assertNotNull(response.getRegistrationId());
        
        verify(tenantRegistrationRepository).insert(entityCaptor.capture());
        TenantRegistrationEntity capturedEntity = entityCaptor.getValue();
        
        assertNotNull(capturedEntity.getRegistrationId());
        assertEquals(NCA_ID, capturedEntity.getNcaId());
        assertEquals(TENANT, capturedEntity.getTenant());
        assertEquals(tenantData, capturedEntity.getTenantRegistrationData());
        assertEquals(FUNCTION_ID, capturedEntity.getFunctionId());
        assertEquals(FUNCTION_VERSION_ID, capturedEntity.getFunctionVersionId());
        assertNotNull(capturedEntity.getCreateTime());

        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void register_blankTenant_throwsBadRequest() {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();

        // Act & Assert
        assertThrows(IcmsBadRequestException.class, 
                () -> service.register(NCA_ID, "", request, emptyAuditProps()));
        
        verify(tenantRegistrationRepository, never()).insert(any());
        verify(telemetryEventClient, never()).triggerEvent(anyList());
    }

    @Test
    void register_nullTenant_throwsBadRequest() {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();

        // Act & Assert
        assertThrows(IcmsBadRequestException.class, 
                () -> service.register(NCA_ID, null, request, emptyAuditProps()));
        
        verify(tenantRegistrationRepository, never()).insert(any());
        verify(telemetryEventClient, never()).triggerEvent(anyList());
    }

    @Test
    void register_invalidTenant_throwsBadRequest() {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();
        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));

        // Act & Assert
        assertThrows(IcmsBadRequestException.class, 
                () -> service.register(NCA_ID, "invalid-tenant", request, emptyAuditProps()));
        
        verify(tenantRegistrationRepository, never()).insert(any());
        verify(telemetryEventClient, never()).triggerEvent(anyList());
    }

    @Test
    void register_tenantNotInConfigList_throwsBadRequest() {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();
        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("other-tenant"));

        // Act & Assert
        assertThrows(IcmsBadRequestException.class, 
                () -> service.register(NCA_ID, TENANT, request, emptyAuditProps()));
        
        verify(tenantRegistrationRepository, never()).insert(any());
        verify(telemetryEventClient, never()).triggerEvent(anyList());
    }

    @Test
    void register_nullValidTenantsConfig_throwsBadRequest() {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();
        when(tenantConfigurationProperties.getValidTenants()).thenReturn(null);

        // Act & Assert
        assertThrows(IcmsBadRequestException.class, 
                () -> service.register(NCA_ID, TENANT, request, emptyAuditProps()));
        
        verify(tenantRegistrationRepository, never()).insert(any());
        verify(telemetryEventClient, never()).triggerEvent(anyList());
    }

    @Test
    void register_repositoryInsertFails_throwsInternalServerError() {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();
        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.insert(any(TenantRegistrationEntity.class)))
                .thenThrow(new IcmsInternalServerException("Database error"));

        // Act & Assert
        assertThrows(IcmsInternalServerException.class, 
                () -> service.register(NCA_ID, TENANT, request, emptyAuditProps()));
        
        verify(telemetryEventClient, never()).triggerEvent(anyList());
    }

    @Test
    void register_telemetryFails_registrationStillSucceeds() {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();
        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.insert(any(TenantRegistrationEntity.class)))
                .thenAnswer(invocation -> invocation.getArgument(0));
        doThrow(new RuntimeException("Telemetry error"))
                .when(telemetryEventClient).triggerEvent(anyList());

        // Act
        TenantRegistrationResponse response = service.register(NCA_ID, TENANT, request, emptyAuditProps());

        // Assert - Registration should still succeed even if telemetry fails
        assertNotNull(response);
        assertNotNull(response.getRegistrationId());
        
        verify(tenantRegistrationRepository).insert(any(TenantRegistrationEntity.class));
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void delete_success() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        TenantRegistrationEntity entity = TenantRegistrationEntity.builder()
                .registrationId(registrationId)
                .ncaId(NCA_ID)
                .tenant(TENANT)
                .tenantRegistrationData(Map.of("key", "value"))
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .createTime(Instant.now())
                .build();

        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.findByRegistrationId(registrationId))
                .thenReturn(Optional.of(entity));
        doNothing().when(tenantRegistrationRepository).delete(any(TenantRegistrationEntity.class));
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        service.delete(NCA_ID, TENANT, registrationId, emptyAuditProps());

        // Assert
        verify(tenantRegistrationRepository).findByRegistrationId(registrationId);
        verify(tenantRegistrationRepository).delete(entity);
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void delete_registrationNotFound_throwsNotFound() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.findByRegistrationId(registrationId))
                .thenReturn(Optional.empty());

        // Act & Assert
        assertThrows(IcmsNotFoundException.class,
                () -> service.delete(NCA_ID, TENANT, registrationId, emptyAuditProps()));

        verify(tenantRegistrationRepository).findByRegistrationId(registrationId);
        verify(tenantRegistrationRepository, never()).delete(any());
    }

    @Test
    void delete_differentTenant_throwsForbidden() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        TenantRegistrationEntity entity = TenantRegistrationEntity.builder()
                .registrationId(registrationId)
                .ncaId(NCA_ID)
                .tenant("other-tenant")
                .tenantRegistrationData(Map.of("key", "value"))
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .createTime(Instant.now())
                .build();

        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.findByRegistrationId(registrationId))
                .thenReturn(Optional.of(entity));

        // Act & Assert
        AccessDeniedException ex = assertThrows(AccessDeniedException.class,
                () -> service.delete(NCA_ID, TENANT, registrationId, emptyAuditProps()));
        assertEquals("Registration doesn't belong to provided tenant", ex.getMessage());

        verify(tenantRegistrationRepository).findByRegistrationId(registrationId);
        verify(tenantRegistrationRepository, never()).delete(any());
    }

    @Test
    void delete_differentNcaId_throwsForbidden() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        TenantRegistrationEntity entity = TenantRegistrationEntity.builder()
                .registrationId(registrationId)
                .ncaId("other-nca-id")
                .tenant(TENANT)
                .tenantRegistrationData(Map.of("key", "value"))
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .createTime(Instant.now())
                .build();

        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.findByRegistrationId(registrationId))
                .thenReturn(Optional.of(entity));

        // Act & Assert
        AccessDeniedException ex = assertThrows(AccessDeniedException.class,
                () -> service.delete(NCA_ID, TENANT, registrationId, emptyAuditProps()));
        assertEquals("Registration doesn't belong to provided ncaId", ex.getMessage());

        verify(tenantRegistrationRepository).findByRegistrationId(registrationId);
        verify(tenantRegistrationRepository, never()).delete(any());
    }

    @Test
    void delete_invalidTenant_throwsBadRequest() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));

        // Act & Assert
        assertThrows(IcmsBadRequestException.class,
                () -> service.delete(NCA_ID, "invalid-tenant", registrationId, emptyAuditProps()));

        verify(tenantRegistrationRepository, never()).findByRegistrationId(any());
        verify(tenantRegistrationRepository, never()).delete(any());
    }

    @Test
    void delete_telemetryFails_deletionStillSucceeds() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        TenantRegistrationEntity entity = TenantRegistrationEntity.builder()
                .registrationId(registrationId)
                .ncaId(NCA_ID)
                .tenant(TENANT)
                .tenantRegistrationData(Map.of("key", "value"))
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .createTime(Instant.now())
                .build();

        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.findByRegistrationId(registrationId))
                .thenReturn(Optional.of(entity));
        doNothing().when(tenantRegistrationRepository).delete(any(TenantRegistrationEntity.class));
        doThrow(new RuntimeException("Telemetry error"))
                .when(telemetryEventClient).triggerEvent(anyList());

        // Act - Should not throw exception even if telemetry fails
        service.delete(NCA_ID, TENANT, registrationId, emptyAuditProps());

        // Assert - Deletion should still succeed
        verify(tenantRegistrationRepository).findByRegistrationId(registrationId);
        verify(tenantRegistrationRepository).delete(entity);
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void delete_blankTenant_throwsBadRequest() {
        // Prepare
        UUID registrationId = UUID.randomUUID();

        // Act & Assert
        assertThrows(IcmsBadRequestException.class,
                () -> service.delete(NCA_ID, "", registrationId, emptyAuditProps()));

        verify(tenantRegistrationRepository, never()).findByRegistrationId(any());
        verify(tenantRegistrationRepository, never()).delete(any());
    }

    @Test
    void delete_nullTenant_throwsBadRequest() {
        // Prepare
        UUID registrationId = UUID.randomUUID();

        // Act & Assert
        assertThrows(IcmsBadRequestException.class,
                () -> service.delete(NCA_ID, null, registrationId, emptyAuditProps()));

        verify(tenantRegistrationRepository, never()).findByRegistrationId(any());
        verify(tenantRegistrationRepository, never()).delete(any());
    }

    @Test
    void delete_nullTenantConfig_throwsBadRequest() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        when(tenantConfigurationProperties.getValidTenants()).thenReturn(null);

        // Act & Assert
        assertThrows(IcmsBadRequestException.class,
                () -> service.delete(NCA_ID, TENANT, registrationId, emptyAuditProps()));

        verify(tenantRegistrationRepository, never()).findByRegistrationId(any());
        verify(tenantRegistrationRepository, never()).delete(any());
    }

    @Test
    void delete_entityWithNullTenantRegistrationData_success() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        TenantRegistrationEntity entity = TenantRegistrationEntity.builder()
                .registrationId(registrationId)
                .ncaId(NCA_ID)
                .tenant(TENANT)
                .tenantRegistrationData(null) // Edge case: null data
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .createTime(Instant.now())
                .build();

        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.findByRegistrationId(registrationId))
                .thenReturn(Optional.of(entity));
        doNothing().when(tenantRegistrationRepository).delete(any(TenantRegistrationEntity.class));
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        service.delete(NCA_ID, TENANT, registrationId, emptyAuditProps());

        // Assert - Should handle null data gracefully
        verify(tenantRegistrationRepository).findByRegistrationId(registrationId);
        verify(tenantRegistrationRepository).delete(entity);
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void delete_repositoryDeleteFails_throwsInternalServerError() {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        TenantRegistrationEntity entity = TenantRegistrationEntity.builder()
                .registrationId(registrationId)
                .ncaId(NCA_ID)
                .tenant(TENANT)
                .tenantRegistrationData(Map.of("key", "value"))
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .createTime(Instant.now())
                .build();

        when(tenantConfigurationProperties.getValidTenants()).thenReturn(List.of("gdn"));
        when(tenantRegistrationRepository.findByRegistrationId(registrationId))
                .thenReturn(Optional.of(entity));
        doThrow(new IcmsInternalServerException("Database error"))
                .when(tenantRegistrationRepository).delete(any(TenantRegistrationEntity.class));

        // Act & Assert
        assertThrows(IcmsInternalServerException.class,
                () -> service.delete(NCA_ID, TENANT, registrationId, emptyAuditProps()));

        verify(tenantRegistrationRepository).findByRegistrationId(registrationId);
        verify(tenantRegistrationRepository).delete(entity);
    }

    private TenantRegistrationRequest createValidRequest() {
        Map<String, String> tenantData = new HashMap<>();
        tenantData.put("gdnAppId", "test-app-id");
        
        return TenantRegistrationRequest.builder()
                .tenantRegistrationData(tenantData)
                .functionId(FUNCTION_ID)
                .functionVersionId(FUNCTION_VERSION_ID)
                .build();
    }
}
