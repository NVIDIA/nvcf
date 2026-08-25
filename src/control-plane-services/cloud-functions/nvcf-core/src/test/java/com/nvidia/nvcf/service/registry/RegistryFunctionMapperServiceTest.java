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
package com.nvidia.nvcf.service.registry;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.persistence.registry.RegistryCredentialsByAccountRepository;
import com.nvidia.nvcf.persistence.registry.entity.ArtifactType;
import com.nvidia.nvcf.persistence.registry.entity.ProvisionedBy;
import com.nvidia.nvcf.persistence.registry.entity.RegistryCredentialByAccountEntity;
import com.nvidia.nvcf.persistence.registry.entity.RegistryCredentialByAccountKey;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import tools.jackson.databind.node.StringNode;

@ExtendWith(MockitoExtension.class)
class RegistryFunctionMapperServiceTest {

    @Mock
    private RegistryCredentialEssService registryCredentialEssService;

    @Mock
    private RegistryMapperService registryMapperService;

    @Mock
    private RegistryCredentialsByAccountRepository registryCredentialsByAccountRepository;

    @ParameterizedTest
    @MethodSource("keyTypeArguments")
    void shouldSetKeyTypeOnlyForLegacyNgcCredentials(
            String hostname,
            String decodedSecret,
        String expectedKeyType) {
        var entity = registryCredential(hostname);
        if (hostname.endsWith("nvcr.io") || hostname.endsWith("ngc.nvidia.com")) {
            when(registryCredentialEssService.getRegistryCredentialSecret(
                    entity.getKey().getNcaId(), entity.getKey().getRegistryCredentialId()))
                    .thenReturn(Optional.of(secret(decodedSecret)));
        }

        var result = mapper().toRegistryCredentialDetailsDto(entity);

        assertThat(result.keyType()).isEqualTo(expectedKeyType);
    }

    /** Verifies the credential id and secret are populated when the ESS secret is available. */
    @Test
    void shouldPopulateRegistryCredentialIdWhenMappingToTempRegistryCredentialDetailsDto() {
        var registryCredentialId = UUID.randomUUID();
        var ncaId = "account-id";
        var detailsDto = RegistryCredentialDetailsDto.builder()
                .registryCredentialId(registryCredentialId)
                .ncaId(ncaId)
                .registryName("ngc")
                .registryHostname("nvcr.io")
                .registryCredentialName("registry-credential")
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .build();
        when(registryCredentialEssService.getRegistryCredentialSecret(ncaId, registryCredentialId))
                .thenReturn(Optional.of(secret("$oauthtoken:nvapi-key")));

        var result = mapper().toTempRegistryCredentialDetailsDto(detailsDto);

        assertThat(result).isNotNull();
        assertThat(result.registryCredentialId()).isEqualTo(registryCredentialId);
        assertThat(result.registryHostname()).isEqualTo("nvcr.io");
        assertThat(result.secret()).isNotNull();
    }

    /** Verifies mapping returns {@code null} when the ESS secret cannot be retrieved. */
    @Test
    void shouldReturnNullTempRegistryCredentialDetailsDtoWhenEssSecretIsUnavailable() {
        var registryCredentialId = UUID.randomUUID();
        var ncaId = "account-id";
        var detailsDto = RegistryCredentialDetailsDto.builder()
                .registryCredentialId(registryCredentialId)
                .ncaId(ncaId)
                .registryName("ngc")
                .registryHostname("nvcr.io")
                .registryCredentialName("registry-credential")
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .build();
        when(registryCredentialEssService.getRegistryCredentialSecret(ncaId, registryCredentialId))
                .thenReturn(Optional.empty());

        var result = mapper().toTempRegistryCredentialDetailsDto(detailsDto);

        assertThat(result).isNull();
    }

    @Test
    void shouldLeaveKeyTypeNullWhenEssSecretIsUnavailable() {
        var entity = registryCredential("helm.ngc.nvidia.com");
        when(registryCredentialEssService.getRegistryCredentialSecret(
                entity.getKey().getNcaId(), entity.getKey().getRegistryCredentialId()))
                .thenReturn(Optional.empty());

        var result = mapper().toRegistryCredentialDetailsDto(entity);

        assertThat(result.keyType()).isNull();
    }

    private static java.util.stream.Stream<Arguments> keyTypeArguments() {
        return java.util.stream.Stream.of(
                Arguments.of("nvcr.io", "$oauthtoken:legacy-key", "LEGACY"),
                Arguments.of("helm.ngc.nvidia.com", "$oauthtoken:legacy-key", "LEGACY"),
                Arguments.of("api.ngc.nvidia.com", "$oauthtoken:legacy-key", "LEGACY"),
                Arguments.of("helm.ngc.nvidia.com", "$oauthtoken:nvapi-key", null),
                Arguments.of("api.ngc.nvidia.com", "$oauthtoken:nvapi-key", null),
                Arguments.of("docker.io", "$oauthtoken:legacy-key", null),
                Arguments.of("helm.ngc.nvidia.com", "invalid-secret", null));
    }

    private RegistryFunctionMapperService mapper() {
        return new RegistryFunctionMapperService(
                registryCredentialEssService,
                registryMapperService,
                registryCredentialsByAccountRepository);
    }

    private static RegistryCredentialByAccountEntity registryCredential(String hostname) {
        return RegistryCredentialByAccountEntity.builder()
                .key(RegistryCredentialByAccountKey.builder()
                        .ncaId("account-id")
                        .registryCredentialId(UUID.randomUUID())
                        .build())
                .registryName("ngc")
                .registryHostname(hostname)
                .registryCredentialName("registry-credential")
                .artifactTypes(Set.of(ArtifactType.HELM))
                .provisionedBy(ProvisionedBy.USER)
                .build();
    }

    private static SecretDto secret(String decodedSecret) {
        var encodedSecret = Base64.getEncoder()
                .encodeToString(decodedSecret.getBytes(StandardCharsets.UTF_8));
        return SecretDto.builder()
                .name("registry-credential")
                .value(new StringNode(encodedSecret))
                .build();
    }
}
