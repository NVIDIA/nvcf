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

import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REGISTRATION_ID;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TENANT;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TENANT_REGISTRATION_DATA_KEYS;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForCreateTenantRegistration;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForDeleteTenantRegistration;

import com.nvidia.icms.configuration.bean.TenantConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.tenantregistration.TenantRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.tenantregistration.TenantRegistrationResponse;
import com.nvidia.icms.outbound.cassandra.tenantregistration.TenantRegistrationRepository;
import com.nvidia.icms.outbound.cassandra.tenantregistration.entity.TenantRegistrationEntity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TenantRegistrationService {

    private final TenantRegistrationRepository tenantRegistrationRepository;
    private final TenantConfigurationProperties tenantConfigurationProperties;
    private final TelemetryEventClient telemetryEventClient;
    private final AppAuditService auditService;

    public TenantRegistrationResponse register(String ncaId, String tenant, TenantRegistrationRequest request,
            Map<String, Object> auditProps) {
        validateTenant(tenant);

        UUID registrationId = UUID.randomUUID();
        TenantRegistrationEntity entity = TenantRegistrationEntity.builder()
                .registrationId(registrationId)
                .ncaId(ncaId)
                .tenant(tenant)
                .tenantRegistrationData(request.getTenantRegistrationData())
                .functionId(request.getFunctionId())
                .functionVersionId(request.getFunctionVersionId())
                .createTime(Instant.now())
                .build();

        tenantRegistrationRepository.insert(entity);
        log.info("Tenant registration created: registrationId={}, tenant={}, ncaId={}", registrationId, tenant, ncaId);

        populateAuditValuesForCreateTenantRegistration(auditProps, registrationId.toString());
        auditService.sendAuditEventForTenantRegistrationEntity(auditProps, null, entity);

        sendTelemetryEvent(ncaId, tenant, registrationId, request);

        return TenantRegistrationResponse.builder()
                .registrationId(registrationId)
                .build();
    }

    public void delete(String ncaId, String tenant, UUID registrationId, Map<String, Object> auditProps) {
        validateTenant(tenant);

        // Find the registration
        Optional<TenantRegistrationEntity> registrationOpt = 
                tenantRegistrationRepository.findByRegistrationId(registrationId);

        if (registrationOpt.isEmpty()) {
            throw new IcmsNotFoundException("Registration not found: " + registrationId);
        }

        TenantRegistrationEntity registration = registrationOpt.get();

        // Validate tenant ownership
        if (!tenant.equals(registration.getTenant())) {
            log.warn("Tenant ownership mismatch for registrationId={}: provided tenant='{}', actual tenant='{}'",
                    registrationId, tenant, registration.getTenant());
            throw new AccessDeniedException("Registration doesn't belong to provided tenant");
        }

        // Validate ncaId ownership
        if (!ncaId.equals(registration.getNcaId())) {
            log.warn("NcaId ownership mismatch for registrationId={}: provided ncaId='{}', actual ncaId='{}'",
                    registrationId, ncaId, registration.getNcaId());
            throw new AccessDeniedException("Registration doesn't belong to provided ncaId");
        }

        tenantRegistrationRepository.delete(registration);
        log.info("Tenant registration deleted: registrationId={}, tenant={}, ncaId={}", registrationId, tenant, ncaId);

        populateAuditValuesForDeleteTenantRegistration(auditProps, registrationId.toString());
        auditService.sendAuditEventForTenantRegistrationEntity(auditProps, registration, null);

        sendDeleteTelemetryEvent(ncaId, tenant, registrationId, registration);
    }

    private void validateTenant(String tenant) {
        if (StringUtils.isBlank(tenant)) {
            throw new IcmsBadRequestException("tenant must not be blank");
        }
        if (tenantConfigurationProperties.getValidTenants() == null
                || !tenantConfigurationProperties.getValidTenants().contains(tenant)) {
            throw new IcmsBadRequestException("Invalid tenant: " + tenant);
        }
    }

    private void sendTelemetryEvent(String ncaId, String tenant, UUID registrationId, TenantRegistrationRequest request) {
        try {
            Map<String, Object> metadata = new HashMap<>();
            metadata.put(TENANT.getName(), tenant);
            metadata.put(REGISTRATION_ID.getName(), registrationId.toString());
            if (request.getTenantRegistrationData() != null) {
                metadata.put(TENANT_REGISTRATION_DATA_KEYS.getName(),
                        List.copyOf(request.getTenantRegistrationData().keySet()));
            }

            GenericMetric metric = new GenericMetric()
                    .withEventName(Events.TENANT_REGISTRATION_CREATED.toString())
                    .withNcaId(ncaId)
                    .withFunctionId(request.getFunctionId())
                    .withFunctionVersionId(request.getFunctionVersionId())
                    .withMetadata(metadata);

            telemetryEventClient.triggerEvent(List.of(metric));
        } catch (Exception ex) {
            log.warn("Failed to send telemetry event for tenant registration: registrationId={}", registrationId, ex);
        }
    }

    private void sendDeleteTelemetryEvent(String ncaId, String tenant, UUID registrationId, TenantRegistrationEntity registration) {
        try {
            Map<String, Object> metadata = new HashMap<>();
            metadata.put(TENANT.getName(), tenant);
            metadata.put(REGISTRATION_ID.getName(), registrationId.toString());
            if (registration.getTenantRegistrationData() != null) {
                metadata.put(TENANT_REGISTRATION_DATA_KEYS.getName(), 
                        List.copyOf(registration.getTenantRegistrationData().keySet()));
            }

            GenericMetric metric = new GenericMetric()
                    .withEventName(Events.TENANT_REGISTRATION_DELETED.toString())
                    .withNcaId(ncaId)
                    .withFunctionId(registration.getFunctionId())
                    .withFunctionVersionId(registration.getFunctionVersionId())
                    .withMetadata(metadata);

            telemetryEventClient.triggerEvent(List.of(metric));
        } catch (Exception ex) {
            log.warn("Failed to send delete telemetry event for tenant registration: registrationId={}", registrationId, ex);
        }
    }
}
