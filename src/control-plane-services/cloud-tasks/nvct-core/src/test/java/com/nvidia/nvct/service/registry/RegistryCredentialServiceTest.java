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
package com.nvidia.nvct.service.registry;

import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.CONTAINER;
import static com.nvidia.nvct.util.TestConstants.TEST_GFN_GPU_SPEC;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.account.dto.AccountDto;
import com.nvidia.nvct.service.account.dto.RegistryCredentialDto;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class RegistryCredentialServiceTest {

    private static final String NCA_ID = "test-nca-id";
    private static final String CONTAINER_IMAGE = "docker.io/library/nginx:latest";
    private static final String CONTAINER_HOSTNAME = "docker.io";
    private static final UUID REGISTRY_CREDENTIAL_ID =
            UUID.fromString("00000000-0000-0000-0000-000000000001");
    private static final String ESS_SECRET_VALUE = "ess-base64-secret";
    private static final String INLINE_SECRET_VALUE = "inline-base64-secret";

    @Mock
    private AccountService accountService;

    @Mock
    private RegistryCredentialEssService registryCredentialEssService;

    private RegistryCredentialService createService() {
        var registryMapperService = new RegistryMapperService(
                "stg.nvcr.io", "api.stg.ngc.nvidia.com", "helm.stg.ngc.nvidia.com");
        var registryTaskMapperService = new RegistryTaskMapperService(registryMapperService);
        return new RegistryCredentialService(
                accountService,
                registryMapperService,
                registryTaskMapperService,
                registryCredentialEssService,
                JsonMapper.builder().build(),
                "sidecar-image-pull-secret",
                "sidecar.registry.example.com");
    }

    private static TaskEntity containerTask() {
        return TaskEntity.builder()
                .taskId(UUID.randomUUID())
                .ncaId(NCA_ID)
                .name("test-task")
                .status(TaskStatus.QUEUED)
                .gpuSpec(TEST_GFN_GPU_SPEC)
                .maxQueuedDuration(Duration.ofHours(1))
                .terminalGracePeriodDuration(Duration.ofHours(1))
                .containerImage(CONTAINER_IMAGE)
                .build();
    }

    private void mockAccountWith(RegistryCredentialDto credential) {
        var account = AccountDto.builder()
                .ncaId(NCA_ID)
                .name("test-account")
                .registryCredentials(List.of(credential))
                .lastUpdatedAt(Instant.now())
                .maxTasksAllowed(1)
                .build();
        when(accountService.getAccount(NCA_ID)).thenReturn(account);
    }

    @Test
    void shouldResolveSecretFromEssWhenInlineSecretAbsent() {
        var service = createService();
        var credential = RegistryCredentialDto.builder()
                .registryCredentialId(REGISTRY_CREDENTIAL_ID)
                .registryHostname(CONTAINER_HOSTNAME)
                .artifactTypes(Set.of(CONTAINER))
                .build();
        mockAccountWith(credential);
        when(registryCredentialEssService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID))
                .thenReturn(Optional.of(SecretDto.builder()
                        .name("docker-container-registry-credential")
                        .value(new StringNode(ESS_SECRET_VALUE))
                        .build()));

        var values = service.getContainerRegistryCredentialsValue(containerTask());

        assertThat(values).containsExactly(ESS_SECRET_VALUE);
        verify(registryCredentialEssService)
                .getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID);
    }

    @Test
    void shouldUseInlineSecretWhenPresentAndNotCallEss() {
        var service = createService();
        var credential = RegistryCredentialDto.builder()
                .registryHostname(CONTAINER_HOSTNAME)
                .secret(SecretDto.builder()
                        .name("docker-container-registry-credential")
                        .value(new StringNode(INLINE_SECRET_VALUE))
                        .build())
                .artifactTypes(Set.of(CONTAINER))
                .build();
        mockAccountWith(credential);

        var values = service.getContainerRegistryCredentialsValue(containerTask());

        assertThat(values).containsExactly(INLINE_SECRET_VALUE);
        verify(registryCredentialEssService, never())
                .getRegistryCredentialSecret(any(), any());
    }

    @Test
    void shouldThrowWhenSecretMissingFromEss() {
        var service = createService();
        var credential = RegistryCredentialDto.builder()
                .registryCredentialId(REGISTRY_CREDENTIAL_ID)
                .registryHostname(CONTAINER_HOSTNAME)
                .artifactTypes(Set.of(CONTAINER))
                .build();
        mockAccountWith(credential);
        when(registryCredentialEssService.getRegistryCredentialSecret(NCA_ID, REGISTRY_CREDENTIAL_ID))
                .thenReturn(Optional.empty());

        var task = containerTask();
        assertThatThrownBy(() -> service.getContainerRegistryCredentialsValue(task))
                .isInstanceOf(NotFoundException.class)
                .hasMessageContaining("Secret not found in ESS");
    }
}
