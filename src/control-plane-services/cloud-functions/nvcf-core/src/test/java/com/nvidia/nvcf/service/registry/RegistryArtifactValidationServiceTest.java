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

import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.CONTAINER;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.HELM;
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
import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import java.util.Collections;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class RegistryArtifactValidationServiceTest {

    @Mock
    private RegistryCredentialFunctionService registryCredentialFunctionService;

    @Mock
    private FunctionMapperService functionMapperService;

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
                functionMapperService,
                registryCredentialFunctionService,
                registryValidationService,
                modelRegistryService,
                resourceRegistryService,
                helmRegistryService,
                containerRegistryService,
                exceptionHandling);
    }

    private static FunctionEntity containerFunction() {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .functionName("test-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v2/models/test/infer")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .containerImage("docker.io/library/nginx:latest")
                .build();
    }

    private static FunctionEntity helmFunction() {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .functionName("test-helm-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v2/models/test/infer")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .helmChart("registry.example.com/charts/mychart:1.0")
                .build();
    }

    private static FunctionEntity blankContainerFunction() {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .functionName("test-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v2/models/test/infer")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .build();
    }

    // --- validateArtifacts() tests (top-level flow) ---

    @Test
    void shouldThrowWhenArtifactValidationFailsAndExceptionHandlingIsThrow() {
        var service = createService("throw");
        var function = containerFunction();

        when(registryCredentialFunctionService.getContainerRegistryCredentialValues(any()))
                .thenReturn(Collections.emptyList());
        doThrow(new NotFoundException("Artifact not found"))
                .when(containerRegistryService).validateArtifact(any(), anyList());

        assertThatThrownBy(() -> service.validateArtifacts(function))
                .isInstanceOf(NotFoundException.class)
                .hasMessageContaining("Artifact not found");
    }

    @Test
    void shouldLogAndContinueWhenArtifactValidationFailsAndExceptionHandlingIsLog() {
        var service = createService("log");
        var function = containerFunction();

        when(registryCredentialFunctionService.getContainerRegistryCredentialValues(any()))
                .thenReturn(Collections.emptyList());
        doThrow(new NotFoundException("Artifact not found"))
                .when(containerRegistryService).validateArtifact(any(), anyList());

        assertThatCode(() -> service.validateArtifacts(function))
                .doesNotThrowAnyException();
    }

    @Test
    void shouldThrowBadRequestWhenCredentialExistenceFailsAndExceptionHandlingIsThrow() {
        var service = createService("throw");
        var function = containerFunction();

        when(registryCredentialFunctionService.getContainerRegistryCredentialDetails(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialFunctionService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(true);

        assertThatThrownBy(() -> service.validateArtifacts(function))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("Missing CONTAINER registry credential for hostname 'docker.io'");
    }

    @Test
    void shouldLogAndContinueWhenCredentialExistenceFailsAndExceptionHandlingIsLog() {
        var service = createService("log");
        var function = containerFunction();

        when(registryCredentialFunctionService.getContainerRegistryCredentialDetails(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialFunctionService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(true);

        assertThatCode(() -> service.validateArtifacts(function))
                .doesNotThrowAnyException();
    }

    @Test
    void shouldSucceedWhenCredentialResolutionReturnsEmptyAndArtifactValidationDisabled() {
        var service = createService("throw");
        var function = containerFunction();

        when(registryCredentialFunctionService.getContainerRegistryCredentialDetails(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialFunctionService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(false);
        when(registryCredentialFunctionService.getModelRegistryCredentialsMap(any(), any()))
                .thenReturn(Collections.emptyMap());
        when(registryCredentialFunctionService.getResourceRegistryCredentialsMap(any()))
                .thenReturn(Collections.emptyMap());

        when(registryCredentialFunctionService.getContainerRegistryCredentialValues(any()))
                .thenReturn(Collections.emptyList());

        assertThatCode(() -> service.validateArtifacts(function))
                .doesNotThrowAnyException();
    }

    // --- validateContainerRegistryCredentialsExist() wrapper tests ---

    @Test
    void containerWrapperShouldThrowIllegalStateWhenEnabledAndMissing() {
        var service = createService("throw");
        var function = containerFunction();

        when(registryCredentialFunctionService.getContainerRegistryCredentialDetails(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialFunctionService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(true);

        assertThatThrownBy(() -> service.validateContainerRegistryCredentialsExist(function))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("Missing CONTAINER registry credential for hostname 'docker.io'");
    }

    @Test
    void containerWrapperShouldPassWhenDisabledAndMissing() {
        var service = createService("throw");
        var function = containerFunction();

        when(registryCredentialFunctionService.getContainerRegistryCredentialDetails(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialFunctionService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(false);

        assertThatCode(() -> service.validateContainerRegistryCredentialsExist(function))
                .doesNotThrowAnyException();
    }

    @Test
    void containerWrapperShouldPassWhenCredentialsPresent() {
        var service = createService("throw");
        var function = containerFunction();

        var cred = org.mockito.Mockito.mock(
                com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto.class);
        when(registryCredentialFunctionService.getContainerRegistryCredentialDetails(any()))
                .thenReturn(List.of(cred));
        when(registryCredentialFunctionService.getRegistryHostname("docker.io/library/nginx:latest"))
                .thenReturn("docker.io");
        when(registryValidationService.isArtifactValidationEnabled(CONTAINER, "docker.io"))
                .thenReturn(true);

        assertThatCode(() -> service.validateContainerRegistryCredentialsExist(function))
                .doesNotThrowAnyException();
    }

    @Test
    void containerWrapperShouldSkipWhenContainerImageIsBlank() {
        var service = createService("throw");
        var function = blankContainerFunction();

        assertThatCode(() -> service.validateContainerRegistryCredentialsExist(function))
                .doesNotThrowAnyException();
        verifyNoInteractions(registryCredentialFunctionService);
    }

    // --- validateHelmRegistryCredentialsExist() wrapper tests ---

    @Test
    void helmWrapperShouldThrowIllegalStateWhenEnabledAndMissing() {
        var service = createService("throw");
        var function = helmFunction();

        when(registryCredentialFunctionService.getHelmRegistryCredentialDetails(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialFunctionService.getHelmRegistryHostname("registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(true);

        assertThatThrownBy(() -> service.validateHelmRegistryCredentialsExist(function))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("Missing HELM registry credential for hostname 'registry.example.com'");
    }

    @Test
    void helmWrapperShouldPassWhenDisabledAndMissing() {
        var service = createService("throw");
        var function = helmFunction();

        when(registryCredentialFunctionService.getHelmRegistryCredentialDetails(any()))
                .thenReturn(Collections.emptyList());
        when(registryCredentialFunctionService.getHelmRegistryHostname("registry.example.com/charts/mychart:1.0"))
                .thenReturn("registry.example.com");
        when(registryValidationService.isArtifactValidationEnabled(HELM, "registry.example.com"))
                .thenReturn(false);

        assertThatCode(() -> service.validateHelmRegistryCredentialsExist(function))
                .doesNotThrowAnyException();
    }

    @Test
    void helmWrapperShouldSkipWhenHelmChartIsBlank() {
        var service = createService("throw");
        var function = containerFunction();

        assertThatCode(() -> service.validateHelmRegistryCredentialsExist(function))
                .doesNotThrowAnyException();
        verify(registryCredentialFunctionService, org.mockito.Mockito.never())
                .getHelmRegistryCredentialDetails(any());
    }
}
