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

package com.nvidia.boot.registries.service.registry;

import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.ValidationProperties;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@RequiredArgsConstructor
public class RegistryValidationService {

    private final RegistryLookupService registryLookupService;

    public boolean isCredentialValidationEnabled(ArtifactTypeEnum type, String hostname) {
        return Optional.ofNullable(registryLookupService.getRegistryConfig(type, hostname)
                                           .getCredentialValidation())
                .map(ValidationProperties::isEnabled)
                .orElse(true);
    }

    public boolean isArtifactValidationEnabled(ArtifactTypeEnum type, String hostname) {
        return Optional.ofNullable(registryLookupService.getRegistryConfig(type, hostname)
                                           .getArtifactValidation())
                .map(ValidationProperties::isEnabled)
                .orElse(true);
    }
}
