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

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.RecognizedRegistryConfiguration;
import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.RegistryConfiguration;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import com.nvidia.boot.registries.service.registry.model.ModelRegistry;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistry;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.stream.Collectors;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.util.CollectionUtils;


@Slf4j
@Getter
public class RegistryLookupService {

    private static final String MESG_UNSUPPORTED_REGISTRY_TYPE =
            "Unsupported registry: no %s registries configured";
    private static final String MESG_UNSUPPORTED_REGISTRY_HOSTNAME =
            "Unsupported registry hostname %s for registry type %s";

    private final Map<String, String> resourceRegistryNameToHostname;
    private final Map<String, String> modelRegistryNameToHostname;
    private final Map<String, String> containerRegistryNameToHostname;
    private final Map<String, String> helmRegistryNameToHostname;
    private final Map<String, String> resourceRegistryHostnameToName;
    private final Map<String, String> modelRegistryHostnameToName;
    private final Map<String, String> containerRegistryHostnameToName;
    private final Map<String, String> helmRegistryHostnameToName;
    private final Map<String, RegistryConfiguration> containerRegistryConfigMap;
    private final Map<String, RegistryConfiguration> helmRegistryConfigMap;
    private final Map<String, RegistryConfiguration> modelRegistryConfigMap;
    private final Map<String, RegistryConfiguration> resourceRegistryConfigMap;
    private final RegistryMapperService registryMapperService;

    public RegistryLookupService(
            List<ResourceRegistry> resourceRegistries,
            List<ModelRegistry> modelRegistries,
            List<HelmRegistry> helmRegistries,
            List<ContainerRegistry> containerRegistries,
            RecognizedRegistryConfiguration recognizedRegistryConfig,
            RegistryMapperService registryMapperService) {
        this.resourceRegistryNameToHostname = resourceRegistries.stream().collect(
                Collectors.toMap(ResourceRegistry::getName, ResourceRegistry::getHostname));
        this.modelRegistryNameToHostname = modelRegistries.stream().collect(
                Collectors.toMap(ModelRegistry::getName, ModelRegistry::getHostname));
        this.containerRegistryNameToHostname = containerRegistries.stream().collect(
                Collectors.toMap(ContainerRegistry::getName, ContainerRegistry::getHostname));
        this.helmRegistryNameToHostname = helmRegistries.stream().collect(
                Collectors.toMap(HelmRegistry::getName, HelmRegistry::getHostname));
        this.resourceRegistryHostnameToName = resourceRegistries.stream().collect(
                Collectors.toMap(ResourceRegistry::getHostname, ResourceRegistry::getName));
        this.modelRegistryHostnameToName = modelRegistries.stream().collect(
                Collectors.toMap(ModelRegistry::getHostname, ModelRegistry::getName));
        this.containerRegistryHostnameToName = containerRegistries.stream().collect(
                Collectors.toMap(ContainerRegistry::getHostname, ContainerRegistry::getName));
        this.helmRegistryHostnameToName = helmRegistries.stream().collect(
                Collectors.toMap(HelmRegistry::getHostname, HelmRegistry::getName));
        this.registryMapperService = registryMapperService;

        this.containerRegistryConfigMap = recognizedRegistryConfig != null
                ? buildRegistryConfigMap(recognizedRegistryConfig.getContainer()) : Map.of();
        this.helmRegistryConfigMap = recognizedRegistryConfig != null
                ? buildRegistryConfigMap(recognizedRegistryConfig.getHelm()) : Map.of();
        this.modelRegistryConfigMap = recognizedRegistryConfig != null
                ? buildRegistryConfigMap(recognizedRegistryConfig.getModel()) : Map.of();
        this.resourceRegistryConfigMap = recognizedRegistryConfig != null
                ? buildRegistryConfigMap(recognizedRegistryConfig.getResource()) : Map.of();
    }

    private static Map<String, RegistryConfiguration> buildRegistryConfigMap(
            Map<String, RegistryConfiguration> configByRegistryKey) {
        if (CollectionUtils.isEmpty(configByRegistryKey)) {
            return Map.of();
        }
        return configByRegistryKey.values().stream()
                .filter(Objects::nonNull)
                .filter(config -> config.getHostname() != null)
                .collect(Collectors.toMap(
                        RegistryConfiguration::getHostname, config -> config,
                        (existing, duplicate) -> existing, HashMap::new));
    }

    public RegistryConfiguration getRegistryConfig(ArtifactTypeEnum type, String hostname) {
        var normalizedHostname =
                registryMapperService.toNormalizedRecognizedRegistryHostname(hostname);
        var configMap = switch (type) {
            case CONTAINER -> containerRegistryConfigMap;
            case HELM -> helmRegistryConfigMap;
            case MODEL -> modelRegistryConfigMap;
            case RESOURCE -> resourceRegistryConfigMap;
        };
        if (CollectionUtils.isEmpty(configMap)) {
            var msg = MESG_UNSUPPORTED_REGISTRY_TYPE.formatted(type);
            log.error(msg);
            throw new BadRequestException(msg);
        }
        var config = configMap.get(normalizedHostname);
        if (config == null) {
            var msg = MESG_UNSUPPORTED_REGISTRY_HOSTNAME.formatted(normalizedHostname, type);
            log.error(msg);
            throw new BadRequestException(msg);
        }
        return config;
    }

    public String getResourceRegistryHostnameByName(String registryName) {
        return resourceRegistryNameToHostname.get(registryName);
    }

    public String getModelRegistryHostnameByName(String registryName) {
        return modelRegistryNameToHostname.get(registryName);
    }

    public String getContainerRegistryHostnameByName(String registryName) {
        return containerRegistryNameToHostname.get(registryName);
    }

    public String getHelmRegistryHostnameByName(String registryName) {
        return helmRegistryNameToHostname.get(registryName);
    }

    public String getResourceRegistryNameByHostName(String hostName) {
        return resourceRegistryHostnameToName.get(hostName);
    }

    public String getModelRegistryNameByHostName(String hostName) {
        return modelRegistryHostnameToName.get(hostName);
    }

    public String getContainerRegistryNameByHostName(String hostName) {
        return containerRegistryHostnameToName.get(hostName);
    }

    public String getHelmRegistryNameByHostName(String hostName) {
        return helmRegistryHostnameToName.get(hostName);
    }

    @VisibleForTesting
    public void updateHelmRegistryMap(String name, String hostName) {
        helmRegistryHostnameToName.put(hostName, name);
        helmRegistryNameToHostname.put(name, hostName);
    }

    @VisibleForTesting
    public void updateResourceRegistryMap(String name, String hostName) {
        resourceRegistryHostnameToName.put(hostName, name);
        resourceRegistryNameToHostname.put(name, hostName);
    }

    @VisibleForTesting
    public void updateModelRegistryMap(String name, String hostName) {
        modelRegistryHostnameToName.put(hostName, name);
        modelRegistryNameToHostname.put(name, hostName);
    }

    @VisibleForTesting
    public void updateContainerRegistryMap(String name, String hostName) {
        containerRegistryHostnameToName.put(hostName, name);
        containerRegistryNameToHostname.put(name, hostName);
    }

    @VisibleForTesting
    public void updateContainerRegistryConfigMap(String oldHostName, String newHostName) {
        var config = containerRegistryConfigMap.get(oldHostName);
        if (config != null) {
            containerRegistryConfigMap.put(newHostName, config);
        }
    }

    @VisibleForTesting
    public void updateHelmRegistryConfigMap(String oldHostName, String newHostName) {
        var config = helmRegistryConfigMap.get(oldHostName);
        if (config != null) {
            helmRegistryConfigMap.put(newHostName, config);
        }
    }

    @VisibleForTesting
    public void updateModelRegistryConfigMap(String oldHostName, String newHostName) {
        var config = modelRegistryConfigMap.get(oldHostName);
        if (config != null) {
            modelRegistryConfigMap.put(newHostName, config);
        }
    }

    @VisibleForTesting
    public void updateResourceRegistryConfigMap(String oldHostName, String newHostName) {
        var config = resourceRegistryConfigMap.get(oldHostName);
        if (config != null) {
            resourceRegistryConfigMap.put(newHostName, config);
        }
    }

    @VisibleForTesting
    public void removeHelmRegistryMap(String name) {
        var hostName = helmRegistryNameToHostname.get(name);
        helmRegistryHostnameToName.remove(hostName);
        helmRegistryNameToHostname.remove(name);
    }

    @VisibleForTesting
    public void removeResourceRegistryMap(String name) {
        var hostName = resourceRegistryNameToHostname.get(name);
        resourceRegistryHostnameToName.remove(hostName);
        resourceRegistryNameToHostname.remove(name);
    }

    @VisibleForTesting
    public void removeModelRegistryMap(String name) {
        var hostName = modelRegistryNameToHostname.get(name);
        modelRegistryHostnameToName.remove(hostName);
        modelRegistryNameToHostname.remove(name);
    }

    @VisibleForTesting
    public void removeContainerRegistryMap(String name) {
        var hostName = containerRegistryNameToHostname.get(name);
        containerRegistryHostnameToName.remove(hostName);
        containerRegistryNameToHostname.remove(name);
    }
}
