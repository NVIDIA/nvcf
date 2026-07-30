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

import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.nvct.service.account.dto.RegistryCredentialDto;
import java.util.stream.Stream;
import lombok.AllArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@AllArgsConstructor
public class RegistryTaskMapperService {
    private final RegistryMapperService registryMapperService;

    public Stream<RegistryCredentialDto> expandRegistryCredentialWithCanaryHostname(
            RegistryCredentialDto registryCredentialDto) {
        var originalHostname = registryCredentialDto.registryHostname();
        var canaryHostname = registryMapperService.toCanaryHostname(originalHostname);

        if (canaryHostname.equals(originalHostname)) {
            return Stream.of(registryCredentialDto);
        }
        return Stream.of(
                registryCredentialDto,
                toRegistryCredentialDtoWithCanaryHostname(registryCredentialDto));
    }

    public RegistryCredentialDto toRegistryCredentialDtoWithCanaryHostname(
            RegistryCredentialDto registryDetailsDto) {
        return registryDetailsDto.toBuilder()
                .registryHostname(registryMapperService
                                          .toCanaryHostname(registryDetailsDto.registryHostname()))
                .build();
    }
}
