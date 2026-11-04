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

import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.Mockito.doThrow;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistryService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistryService;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class RegistryCredentialValidationServiceTest {

    @Mock
    private ContainerRegistryService containerRegistryService;

    @Mock
    private HelmRegistryService helmRegistryService;

    private RegistryCredentialValidationService createService(String exceptionHandling) {
        return new RegistryCredentialValidationService(
                containerRegistryService,
                helmRegistryService,
                exceptionHandling);
    }

    @Test
    void shouldThrowWhenExceptionHandlingIsThrow() {
        var service = createService("throw");

        doThrow(new BadRequestException("Invalid credentials"))
                .when(containerRegistryService).validateCredentials(any(), anyList());

        assertThatThrownBy(() -> service.validateRegistryCredentials(
                "docker.io", Set.of(ArtifactTypeEnum.CONTAINER), List.of("secret")))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("Invalid credentials");
    }

    @Test
    void shouldLogAndContinueWhenExceptionHandlingIsLog() {
        var service = createService("log");

        doThrow(new BadRequestException("Invalid credentials"))
                .when(containerRegistryService).validateCredentials(any(), anyList());

        assertThatCode(() -> service.validateRegistryCredentials(
                "docker.io", Set.of(ArtifactTypeEnum.CONTAINER), List.of("secret")))
                .doesNotThrowAnyException();
    }
}
