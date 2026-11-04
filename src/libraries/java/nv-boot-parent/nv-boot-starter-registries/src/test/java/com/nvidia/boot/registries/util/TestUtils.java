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

package com.nvidia.boot.registries.util;

import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.RegistryConfiguration;
import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.ValidationProperties;
import lombok.experimental.UtilityClass;

@UtilityClass
public final class TestUtils {

    public static RegistryConfiguration registryConfig(String hostname) {
        return registryConfig(hostname, hostname);
    }

    public static RegistryConfiguration registryConfig(String name, String hostname) {
        var config = new RegistryConfiguration();
        config.setName(name);
        config.setHostname(hostname);
        return config;
    }

    public static RegistryConfiguration registryConfig(
            String hostname, boolean credentialValidation, boolean artifactValidation) {
        var config = registryConfig(hostname);
        var credentialValidationConfig = new ValidationProperties();
        credentialValidationConfig.setEnabled(credentialValidation);
        config.setCredentialValidation(credentialValidationConfig);

        var artifactValidationConfig = new ValidationProperties();
        artifactValidationConfig.setEnabled(artifactValidation);
        config.setArtifactValidation(artifactValidationConfig);
        return config;
    }
}
