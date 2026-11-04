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

import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_REGISTRY_CRED;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_REGISTRY_CRED;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_REGISTRY_CRED;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.registry.RegistryCredentialEssService;
import com.nvidia.nvcf.service.registry.RegistryCredentialLookupService;
import com.nvidia.nvcf.service.registry.RegistryFunctionMapperService;
import java.util.Base64;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Service
public class TestRegistryCredentialService {
    @Autowired
    private RegistryCredentialLookupService registryCredentialLookupService;

    @Autowired
    private RegistryCredentialEssService registryCredentialEssService;

    @Autowired
    private AccountService accountService;

    @Autowired
    private RegistryFunctionMapperService registryFunctionMapperService;

    public void validateRegistryCredentials(String ncaId) {
        validateRegistryCredential(
                ncaId, ArtifactTypeEnum.CONTAINER, "stg.nvcr.io",
                TEST_CONTAINER_REGISTRY_CRED);

        validateRegistryCredential(
                ncaId, ArtifactTypeEnum.HELM, "helm.stg.ngc.nvidia.com",
                TEST_HELM_REGISTRY_CRED);

        validateRegistryCredential(
                ncaId, ArtifactTypeEnum.MODEL, "api.stg.ngc.nvidia.com",
                TEST_MODEL_REGISTRY_CRED);

        validateRegistryCredential(
                ncaId, ArtifactTypeEnum.RESOURCE, "api.stg.ngc.nvidia.com",
                TEST_MODEL_REGISTRY_CRED);

        verifyBase64EncodedSecretInEss(ncaId);
    }

    public List<RegistryCredentialDetailsDto> getAllCredentialDetails(String ncaId) {
        return registryCredentialLookupService.getRegistryCredentialDtos(ncaId);
    }

    public RegistryCredentialDetailsDto getRegistryCredentialDetails(String ncaId,
                                                                     UUID registryCredentialId) {
        var entity = registryCredentialLookupService
                .lookupRegistryCredentialByAccountAndIdOrThrow(ncaId, registryCredentialId);
        return registryFunctionMapperService.toRegistryCredentialDetailsDto(entity);
    }

    private void validateRegistryCredential(
            String ncaId,
            ArtifactTypeEnum artifactTypeEnum,
            String hostname,
            String secret) {
        var registryCreds = registryCredentialLookupService
                .getRegistryCredentialDtos(ncaId, Set.of(artifactTypeEnum));
        assertThat(registryCreds).hasSize(1);
        var registryCredDto = registryCreds.getFirst();
        assertThat(registryCredDto.registryHostname()).isEqualTo(hostname);

        var regCredId = registryCredDto.registryCredentialId();
        var registryCredSecret = registryCredentialEssService
                .getRegistryCredentialSecret(ncaId, regCredId);
        assertThat(registryCredSecret).isPresent();

        var secretValue = registryCredSecret.get().value().asString();
        assertThat(secretValue).isBase64();

        var rawSecret = new String(Base64.getDecoder().decode(secretValue));
        assertThat(rawSecret).contains(secret);

        if (artifactTypeEnum == ArtifactTypeEnum.MODEL
                || artifactTypeEnum == ArtifactTypeEnum.RESOURCE) {
            assertThat(registryCredSecret.get().name())
                    .isEqualTo("ngc-model-resource-registry-credential");
        } else {
            assertThat(registryCredSecret.get().name())
                    .isEqualTo("ngc-%s-registry-credential"
                                       .formatted(artifactTypeEnum.toString().toLowerCase()));
        }
    }

    public void invalidateCache() {
        registryCredentialEssService.invalidateCache();
        registryCredentialLookupService.invalidateCache();
    }

    private void verifyBase64EncodedSecretInEss(String ncaId) {
        var regCreds = registryCredentialLookupService
                .getRegistryCredentialDtos(ncaId,
                                           Set.of(ArtifactTypeEnum.values()));
        assertThat(regCreds).hasSize(3);
        regCreds.forEach(regCred -> {
            var regId = regCred.registryCredentialId();
            var secret = registryCredentialEssService.getRegistryCredentialSecret(ncaId, regId);
            var secretValue = secret.map(s -> s.value().asString())
                    .orElseThrow(() -> new RuntimeException("Missing secret"));
            assertThat(secretValue).isBase64();
        });
    }
}
