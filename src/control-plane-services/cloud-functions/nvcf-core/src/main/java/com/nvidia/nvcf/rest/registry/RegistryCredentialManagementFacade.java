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
package com.nvidia.nvcf.rest.registry;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.rest.registry.dto.AddRegistryCredentialRequest;
import com.nvidia.nvcf.rest.registry.dto.ListRegistryCredentialDetailsResponse;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.rest.registry.dto.RecognizedRegistriesResponse;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsResponse;
import com.nvidia.nvcf.rest.registry.dto.UpdateRegistryCredentialRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.registry.RegistryCredentialAuditService;
import com.nvidia.nvcf.service.registry.RegistryCredentialLookupService;
import com.nvidia.nvcf.service.registry.RegistryCredentialService;
import com.nvidia.nvcf.service.registry.RegistryFunctionMapperService;
import com.nvidia.nvcf.util.NvcfUtils;
import jakarta.servlet.http.HttpServletRequest;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class RegistryCredentialManagementFacade {
    private final AccountService accountService;
    private final RegistryCredentialAuditService registryCredentialAuditService;
    private final RegistryCredentialLookupService registryCredentialLookupService;
    private final RegistryCredentialService registryCredentialService;
    private final RegistryLookupService registryLookupService;
    private final RegistryFunctionMapperService registryFunctionMapperService;

    public RegistryCredentialDetailsResponse addRegistryCredential(
            String ncaId,
            AddRegistryCredentialRequest request,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var accountEntity = accountService.getAccount(ncaId); // Validate specified account exists.
        var registry = registryCredentialService.addRegistryCredential(ncaId,
                                                                       accountEntity,
                                                                       request,
                                                                       ProvisionedByEnum.USER,
                                                                       payloadBuilder);
        return RegistryCredentialDetailsResponse.builder()
                .registryCredential(
                        registryFunctionMapperService.toRegistryCredentialDetailsDto(registry))
                .build();
    }

    public ListRegistryCredentialDetailsResponse listRegistryCredentials(
            String ncaId,
            Set<ArtifactTypeEnum> artifactTypeEnums,
            Set<ProvisionedByEnum> provisionedByEnums) {
        var registryCredentials = registryCredentialLookupService
                .getRegistryCredentialDtos(ncaId, artifactTypeEnums, provisionedByEnums);
        return ListRegistryCredentialDetailsResponse.builder()
                .registryCredentials(registryCredentials)
                .build();
    }

    public RegistryCredentialDetailsResponse getRegistryCredentialDetails(
            String ncaId,
            UUID registryCredentialId) {
        var entity = registryCredentialLookupService
                .lookupRegistryCredentialByAccountAndIdOrThrow(ncaId, registryCredentialId);
        return RegistryCredentialDetailsResponse.builder()
                .registryCredential(registryFunctionMapperService
                                            .toRegistryCredentialDetailsDto(entity))
                .build();
    }

    public RegistryCredentialDetailsResponse updateRegistryCredential(
            String ncaId,
            UUID registryCredentialId,
            UpdateRegistryCredentialRequest request,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var registry = registryCredentialService.updateRegistryCredential(ncaId,
                                                                          registryCredentialId,
                                                                          request, payloadBuilder);
        return RegistryCredentialDetailsResponse.builder()
                .registryCredential(registry)
                .build();
    }

    public void deleteRegistryCredential(
            String ncaId,
            UUID registryCredentialId,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        registryCredentialService
                .deleteRegistryCredential(ncaId,
                                          registryCredentialId,
                                          payloadBuilder,
                                          false);  // Should not delete system provisioned reg creds
    }

    private List<Map<String, String>> flattenRegistryInfo(
            Map<String, String> registryNameToHostName) {
        return registryNameToHostName.entrySet().stream()
                .sorted(Map.Entry.comparingByKey())
                .map(x -> Map.of(
                        "name", x.getKey(),
                        "hostname", x.getValue()))
                .toList();
    }

    @SneakyThrows
    public RecognizedRegistriesResponse getRecognizedRegistries() {
        var recognizedRegistries = Map.of(
                ArtifactTypeEnum.CONTAINER,
                flattenRegistryInfo(registryLookupService.getContainerRegistryNameToHostname()),
                ArtifactTypeEnum.HELM,
                flattenRegistryInfo(registryLookupService.getHelmRegistryNameToHostname()),
                ArtifactTypeEnum.MODEL,
                flattenRegistryInfo(registryLookupService.getModelRegistryNameToHostname()),
                ArtifactTypeEnum.RESOURCE,
                flattenRegistryInfo(registryLookupService.getResourceRegistryNameToHostname()));
        return RecognizedRegistriesResponse.builder()
                .recognizedRegistries(recognizedRegistries)
                .build();
    }

    private AuditEventPayload.Builder auditEventPayloadBuilder(
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var customProperties = NvcfUtils.getCustomProperties(httpServletRequest);
        return registryCredentialAuditService.auditEventPayloadBuilder(authentication, customProperties);
    }

}
