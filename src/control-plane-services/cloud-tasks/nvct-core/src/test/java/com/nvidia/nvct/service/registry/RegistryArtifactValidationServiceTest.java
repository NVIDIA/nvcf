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
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.HELM;
import static com.nvidia.nvct.util.TestConstants.TEST_GFN_GPU_SPEC;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.registries.service.registry.RegistryValidationService;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistryService;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistryService;
import com.nvidia.boot.registries.service.registry.model.ModelRegistryService;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistryService;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.service.account.dto.RegistryCredentialDto;
import java.time.Duration;
import java.util.Collections;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class RegistryArtifactValidationServiceTest {

    @Mock
    private RegistryCredentialService registryCredentialService;

    @Mock
    private RegistryValidationService registryValidationService;

    @Mock
    private ModelRegistryService modelRegistryService;

    @Mock
    private ResourceRegistryService resourceRegistryService;

    @Mock
    private HelmRegistryService helmRegistryService;

    @Mock
    private ContainerRegistryService containerRegistryService;

    private RegistryArtifactValidationService createService(String exceptionHandling) {
        return new RegistryArtifactValidationService(
                registryCredentialService,
                registryValidationService,
                modelRegistryService,
                resourceRegistryService,
                helmRegistryService,
                containerRegistryService,
                exceptionHandling);
    }

    private static TaskEntity containerTask() {
        return TaskEntity.builder()
                .taskId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .name("test-task")
                .status(TaskStatus.QUEUED)
                .gpuSpec(TEST_GFN_GPU_SPEC)
                .maxQueuedDuration(Duration.ofHours(1))
                .terminalGracePeriodDuration(Duration.ofHours(1))
                .containerImage("docker.io/library/nginx:latest")
                .build();
    }

    private static TaskEntity helmTask() {
        return TaskEntity.builder()
                .taskId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .name("test-helm-task")
                .status(TaskStatus.QUEUED)
                .gpuSpec(TEST_GFN_GPU_SPEC)
                .maxQueuedDuration(Duration.ofHours(1))
                .terminalGracePeriodDuration(Duration.ofHours(1))
                .helmChart("oci://registry.example.com/charts/mychart:1.0")
                .build();
    }

    private static TaskEntity blankContainerTask() {
        return TaskEntity.builder()
                .taskId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .name("test-blank-task")
                .status(TaskStatus.QUEUED)
                .gpuSpec(TEST_GFN_GPU_SPEC)
                .maxQueuedDuration(Duration.ofHours(1))
                .terminalGracePeriodDuration(Duration.ofHours(1))
                .build();
    }

    // --- validateArtifacts() top-level flow ---

    @Test
    void shouldThrowWhenArtifactValidationFailsAndExceptionHandlingIsThrow() {
        var service = createService("throw");
        var task = containerTask();

        when(registryCredentialService.getContainerRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(false);
        when(registryCredentialService.getModelRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getResourceRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getContainerRegistryCredentialsValue(any()))
                .thenReturn(Collections.emptyList());
        doThrow(new NotFoundException("Artifact not found"))
                .when(containerRegistryService).validateArtifact(any(), anyList());

        assertThatThrownBy(() -> service.validateArtifacts(task))
                .isInstanceOf(NotFoundException.class)
                .hasMessageContaining("Artifact not found");
    }

    @Test
    void shouldLogAndContinueWhenArtifactValidationFailsAndExceptionHandlingIsLog() {
        var service = createService("log");
        var task = containerTask();

        when(registryCredentialService.getContainerRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(false);
        when(registryCredentialService.getModelRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getResourceRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getContainerRegistryCredentialsValue(any()))
                .thenReturn(Collections.emptyList());
        doThrow(new NotFoundException("Artifact not found"))
                .when(containerRegistryService).validateArtifact(any(), anyList());

        assertThatCode(() -> service.validateArtifacts(task))
                .doesNotThrowAnyException();
    }

    @Test
    void shouldThrowBadRequestWhenCredentialExistenceFailsAndExceptionHandlingIsThrow() {
        var service = createService("throw");
        var task = containerTask();

        when(registryCredentialService.getContainerRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(true);

        assertThatThrownBy(() -> service.validateArtifacts(task))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("Missing CONTAINER registry credential for hostname 'docker.io'");
    }

    @Test
    void shouldLogAndContinueWhenCredentialExistenceFailsAndExceptionHandlingIsLog() {
        var service = createService("log");
        var task = containerTask();

        when(registryCredentialService.getContainerRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(true);

        assertThatCode(() -> service.validateArtifacts(task))
                .doesNotThrowAnyException();
    }

    @Test
    void shouldSucceedWhenCredentialResolutionReturnsEmptyAndArtifactValidationDisabled() {
        var service = createService("throw");
        var task = containerTask();

        when(registryCredentialService.getContainerRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(false);
        when(registryCredentialService.getModelRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getResourceRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getContainerRegistryCredentialsValue(any()))
                .thenReturn(Collections.emptyList());

        assertThatCode(() -> service.validateArtifacts(task))
                .doesNotThrowAnyException();
    }

    // --- validateArtifacts() top-level flow with helm task ---

    @Test
    void shouldThrowWhenHelmArtifactValidationFailsAndExceptionHandlingIsThrow() {
        var service = createService("throw");
        var task = helmTask();

        when(registryCredentialService.getHelmRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getHelmRegistryHostname(
                "oci://registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(false);
        when(registryCredentialService.getModelRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getResourceRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getHelmRegistryCredentialsValue(any()))
                .thenReturn(Collections.emptyList());
        doThrow(new NotFoundException("Helm chart not found"))
                .when(helmRegistryService).validateArtifact(any(), anyList());

        assertThatThrownBy(() -> service.validateArtifacts(task))
                .isInstanceOf(NotFoundException.class)
                .hasMessageContaining("Helm chart not found");
    }

    @Test
    void shouldLogAndContinueWhenHelmArtifactValidationFailsAndExceptionHandlingIsLog() {
        var service = createService("log");
        var task = helmTask();

        when(registryCredentialService.getHelmRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getHelmRegistryHostname(
                "oci://registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(false);
        when(registryCredentialService.getModelRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getResourceRegistryCredentials(any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialService.getHelmRegistryCredentialsValue(any()))
                .thenReturn(Collections.emptyList());
        doThrow(new NotFoundException("Helm chart not found"))
                .when(helmRegistryService).validateArtifact(any(), anyList());

        assertThatCode(() -> service.validateArtifacts(task))
                .doesNotThrowAnyException();
    }

    @Test
    void shouldThrowBadRequestWhenHelmCredentialExistenceFailsAndExceptionHandlingIsThrow() {
        var service = createService("throw");
        var task = helmTask();

        when(registryCredentialService.getHelmRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getHelmRegistryHostname(
                "oci://registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(true);

        assertThatThrownBy(() -> service.validateArtifacts(task))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining(
                        "Missing HELM registry credential for hostname 'registry.example.com'");
    }

    @Test
    void shouldLogAndContinueWhenHelmCredentialExistenceFailsAndExceptionHandlingIsLog() {
        var service = createService("log");
        var task = helmTask();

        when(registryCredentialService.getHelmRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getHelmRegistryHostname(
                "oci://registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(true);

        assertThatCode(() -> service.validateArtifacts(task))
                .doesNotThrowAnyException();
    }

    // --- validateContainerRegistryCredentialsExist() ---

    @Test
    void validateContainerCredentialsShouldThrowWhenValidationEnabledAndCredentialsMissing() {
        var service = createService("throw");
        var task = containerTask();

        when(registryCredentialService.getContainerRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(true);

        assertThatThrownBy(() -> service.validateContainerRegistryCredentialsExist(task))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("Missing CONTAINER registry credential for hostname 'docker.io'");
    }

    @Test
    void validateContainerCredentialsShouldPassWhenValidationDisabledAndCredentialsMissing() {
        var service = createService("throw");
        var task = containerTask();

        when(registryCredentialService.getContainerRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(false);

        assertThatCode(() -> service.validateContainerRegistryCredentialsExist(task))
                .doesNotThrowAnyException();
    }

    @Test
    void validateContainerCredentialsShouldPassWhenCredentialsPresent() {
        var service = createService("throw");
        var task = containerTask();

        var cred = Mockito.mock(RegistryCredentialDto.class);
        when(registryCredentialService.getContainerRegistryCredentials(any()))
                .thenReturn(List.of(cred));
        when(registryCredentialService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(true);

        assertThatCode(() -> service.validateContainerRegistryCredentialsExist(task))
                .doesNotThrowAnyException();
    }

    @Test
    void validateContainerCredentialsShouldSkipWhenContainerImageIsBlank() {
        var service = createService("throw");
        var task = blankContainerTask();

        assertThatCode(() -> service.validateContainerRegistryCredentialsExist(task))
                .doesNotThrowAnyException();
        verifyNoInteractions(registryCredentialService);
    }

    // --- validateHelmRegistryCredentialsExist() ---

    @Test
    void validateHelmCredentialsShouldThrowWhenValidationEnabledAndCredentialsMissing() {
        var service = createService("throw");
        var task = helmTask();

        when(registryCredentialService.getHelmRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getHelmRegistryHostname(
                "oci://registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(true);

        assertThatThrownBy(() -> service.validateHelmRegistryCredentialsExist(task))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining(
                        "Missing HELM registry credential for hostname 'registry.example.com'");
    }

    @Test
    void validateHelmCredentialsShouldPassWhenValidationDisabledAndCredentialsMissing() {
        var service = createService("throw");
        var task = helmTask();

        when(registryCredentialService.getHelmRegistryCredentials(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialService.getHelmRegistryHostname(
                "oci://registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(false);

        assertThatCode(() -> service.validateHelmRegistryCredentialsExist(task))
                .doesNotThrowAnyException();
    }

    @Test
    void validateHelmCredentialsShouldPassWhenCredentialsPresent() {
        var service = createService("throw");
        var task = helmTask();

        var cred = Mockito.mock(RegistryCredentialDto.class);
        when(registryCredentialService.getHelmRegistryCredentials(any()))
                .thenReturn(List.of(cred));
        when(registryCredentialService.getHelmRegistryHostname(
                "oci://registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(true);

        assertThatCode(() -> service.validateHelmRegistryCredentialsExist(task))
                .doesNotThrowAnyException();
    }

    @Test
    void validateHelmCredentialsShouldSkipWhenHelmChartIsBlank() {
        var service = createService("throw");
        var task = containerTask();

        assertThatCode(() -> service.validateHelmRegistryCredentialsExist(task))
                .doesNotThrowAnyException();
        verify(registryCredentialService, Mockito.never())
                .getHelmRegistryCredentials(any());
    }
}
