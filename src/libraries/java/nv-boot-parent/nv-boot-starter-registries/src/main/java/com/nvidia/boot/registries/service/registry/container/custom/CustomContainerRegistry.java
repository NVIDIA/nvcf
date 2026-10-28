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

package com.nvidia.boot.registries.service.registry.container.custom;

import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import lombok.extern.slf4j.Slf4j;

// NCPs can add container registries under nvcf.registries.recognized or
// nvct.registries.recognized that are not known to NVCF. For such registries,
// artifact validation and credential validation will not be supported. As a
// result, the fail-fast will not be supported. Also, NVCF/NVCT will not send
// any credentials to the Worker as the NCP is expected to configure
// image-pull-secrets on their own.
@Slf4j
public class CustomContainerRegistry implements ContainerRegistry {

    private static final String MESG_UNSUPPORTED_OPERATION =
            "Unsupported operation for container registry '%s' with hostname '%s'";

    private final String name;
    private final String hostname;

    public CustomContainerRegistry(String name, String hostname) {
        this.name = name;
        this.hostname = hostname;
    }

    @Override
    public String getName() {
        return name;
    }

    @Override
    public String getHostname() {
        return hostname;
    }

    @Override
    public void validateArtifact(String containerImageUrl, String secret) {
        log.warn(MESG_UNSUPPORTED_OPERATION.formatted(name, hostname));
    }

    @Override
    public void validateCredential(String hostname, String secret) {
        log.warn(MESG_UNSUPPORTED_OPERATION.formatted(name, hostname));
    }

    @Override
    public void invalidateCache() {
        log.warn(MESG_UNSUPPORTED_OPERATION.formatted(name, hostname));
    }
}
