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

package com.nvidia.boot.registries.configurations;

import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_KEY;

import com.nvidia.boot.registries.service.registry.client.ngc.NgcArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.model.ModelRegistry;
import com.nvidia.boot.registries.service.registry.model.ngc.NgcModelRegistry;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.AutoConfigureAfter;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.web.reactive.function.client.WebClient;

@AutoConfiguration
@AutoConfigureAfter(RegistryConfigurationPropertiesAutoConfiguration.class)
public class ModelRegistryAutoConfiguration {

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "model", key = NGC_PRIVATE_REGISTRY_KEY)
    public ModelRegistry ngcModelRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var ngcConfig = registryConfigurationProperties.getRecognized().getModel()
                .get(NGC_PRIVATE_REGISTRY_KEY);
        var ngcModelArtifactRegistryClient = new NgcArtifactRegistryClient(
                webClientBuilder, ngcConfig.getHostname(), ngcConfig.getCallTimeout(), ngcConfig.getReadTimeout(),
                ngcConfig.getWriteTimeout(), ngcConfig.getConnectionTimeout(),
                ngcConfig.getOauth2().getBaseUrl(), ngcConfig.getOauth2().getGroupScope());
        return new NgcModelRegistry(ngcModelArtifactRegistryClient);
    }
}
