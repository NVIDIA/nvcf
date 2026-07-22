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

import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import com.google.common.collect.Sets;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.account.dto.RegistryCredentialDto;
import com.nvidia.nvct.service.registry.dto.DockerConfigJsonAuthDto;
import com.nvidia.nvct.service.registry.dto.DockerConfigJsonDto;
import com.nvidia.nvct.service.registry.dto.K8sSecretsDto;
import java.net.URI;
import java.util.ArrayList;
import java.util.Collection;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.logging.log4j.util.Strings;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Service
@Slf4j
public class RegistryCredentialService {
    private static final String MESG_COMMON_ERROR_PREFIX =
            "Account '%s', Task '%s': ";
    private static final String MESG_MISSING_REGISTRY =
            MESG_COMMON_ERROR_PREFIX + "Missing %s registry for hostname '%s'";
    private static final String MESG_FAIL_TO_ENCODE_SECRETS =
            MESG_COMMON_ERROR_PREFIX + "Failed to encode secrets";
    private static final String MESG_INVALID_REGISTRY_URL = "Invalid registry URL: %s";

    private final AccountService accountService;
    private final RegistryMapperService registryMapperService;
    private final RegistryTaskMapperService registryTaskMapperService;
    private final JsonMapper jsonMapper;
    private final String sidecarImagePullSecret;
    private final String sidecarRegistryHostname;

    public RegistryCredentialService(
            AccountService accountService,
            RegistryMapperService registryMapperService,
            RegistryTaskMapperService registryTaskMapperService,
            JsonMapper jsonMapper,
            @Value("${nvct.sidecars.image-pull-secret}")
            String sidecarImagePullSecret,
            @Value("${nvct.sidecars.hostname}")
            String sidecarRegistryHostname) {
        this.accountService = accountService;
        this.registryTaskMapperService = registryTaskMapperService;
        this.registryMapperService = registryMapperService;
        this.jsonMapper = jsonMapper;
        this.sidecarImagePullSecret = sidecarImagePullSecret;
        this.sidecarRegistryHostname = sidecarRegistryHostname;
    }

    public String getBase64EncodedSidecarRegistryImagePullSecret(TaskEntity taskEntity) {
        var dockerConfigJsonDto = DockerConfigJsonDto.builder()
                .auths(Map.of(
                        this.sidecarRegistryHostname,
                        DockerConfigJsonAuthDto.builder()
                                .auth(this.sidecarImagePullSecret)
                                .build()
                             ))
                .build();
        return base64Encode(taskEntity, dockerConfigJsonDto);
    }

    public List<RegistryCredentialDto> getRegistryCredentials(TaskEntity task,
                                                              String hostname,
                                                              ArtifactTypeEnum artifactTypeEnum) {
        var account = accountService.getAccount(task.getNcaId());
        if (CollectionUtils.isEmpty(account.registryCredentials())) {
            return Collections.emptyList();
        }
        var normalizedHostname = registryMapperService.toNormalizedHostname(hostname);
        return account.registryCredentials().stream()
                .filter(rc -> rc.artifactTypes().contains(artifactTypeEnum) &&
                        rc.registryHostname().equals(normalizedHostname))
                .toList();
    }

    private List<RegistryCredentialDto> getRegistryCredentials(
            TaskEntity task,
            Set<ArtifactTypeEnum> artifactTypeEnums) {
        var account = accountService.getAccount(task.getNcaId());
        if (CollectionUtils.isEmpty(account.registryCredentials())) {
            return Collections.emptyList();
        }
        return account.registryCredentials().stream()
                .filter(regCred -> !Sets
                        .intersection(regCred.artifactTypes(), artifactTypeEnums)
                        .isEmpty())
                .toList();
    }

    public String getRegistryHostname(String artifactUrl) {
        // WAR for using URI lib to parse url host, since image url won't include protocol prefix.
        var normalizedArtifactUrl =
                !artifactUrl.startsWith("http") ? "https://" + artifactUrl : artifactUrl;
        return Optional.ofNullable(URI.create(normalizedArtifactUrl).getHost())
                .orElseThrow(() -> new IllegalStateException(
                        MESG_INVALID_REGISTRY_URL.formatted(artifactUrl)));
    }

    public String getHelmRegistryHostname(String artifactUrl) {
        var uri = URI.create(artifactUrl);
        var scheme = uri.getScheme();

        // Validate scheme - only allow http, https, oci
        if (scheme == null ||
                !scheme.equals("http") && !scheme.equals("https") && !scheme.equals("oci")) {
            throw new IllegalStateException(
                    MESG_INVALID_REGISTRY_URL.formatted(artifactUrl));
        }

        return Optional.ofNullable(uri.getHost())
                .orElseThrow(() -> new IllegalStateException(
                        MESG_INVALID_REGISTRY_URL.formatted(artifactUrl)));
    }

    @SneakyThrows
    public List<String> getModelRegistriesHostnames(TaskEntity task) {
        var models = task.getModels();
        if (CollectionUtils.isEmpty(models)) {
            return Collections.emptyList();
        }

        return models.stream()
                .map(model -> getRegistryHostname(model.getUrl()))
                .toList();
    }

    @SneakyThrows
    public List<String> getResourceRegistriesHostnames(TaskEntity task) {
        var resources = task.getResources();
        if (CollectionUtils.isEmpty(resources)) {
            return Collections.emptyList();
        }

        return resources.stream()
                .map(resource -> getRegistryHostname(resource.getUrl()))
                .toList();
    }

    private K8sSecretsDto getRegistryImagePullSecrets(
            List<RegistryCredentialDto> registryCredentials) {
        K8sSecretsDto k8SSecretsDto = K8sSecretsDto.builder().k8sSecrets(new ArrayList<>()).build();
        registryCredentials.forEach(registry -> {
            var hostname = registry.registryHostname();
            var secret = registry.secret().value().asString();
            var dockerConfigJsonRegistryCredentialDto = DockerConfigJsonAuthDto
                    .builder()
                    .auth(secret)
                    .build();
            k8SSecretsDto.k8sSecrets().add(
                    DockerConfigJsonDto.builder().auths(
                            Map.of(hostname, dockerConfigJsonRegistryCredentialDto)).build());
        });
        return k8SSecretsDto;
    }

    public List<RegistryCredentialDto> getContainerRegistryCredentials(TaskEntity task) {
        if (Strings.isNotBlank(task.getContainerImage())) {
            var containerRegistryHostname = getRegistryHostname(task.getContainerImage());
            var registryCredentials =
                    getRegistryCredentials(task, containerRegistryHostname, CONTAINER);
            // For Container-based task. Return container registry credentials whose hostname
            // matches the hostname specified in the task definition.
            return registryCredentials.stream()
                    .map(dto -> {
                        if (RegistryMapperService.isCanaryHostname(containerRegistryHostname)) {
                            return registryTaskMapperService
                                    .toRegistryCredentialDtoWithCanaryHostname(dto);
                        }
                        return dto;
                    })
                    .toList();
        }
        // Helm-based task. Return all the container registry credentials associated with the
        // account and duplicated NGC registry with canary alias hostname, since we don't know
        // which registry hostname is in use by the helm chart.
        return getRegistryCredentials(task, Set.of(CONTAINER)).stream()
                .flatMap(registryTaskMapperService::expandRegistryCredentialWithCanaryHostname)
                .toList();
    }

    public List<String> getContainerRegistryCredentialsValue(TaskEntity taskEntity) {
        return getContainerRegistryCredentials(taskEntity)
                .stream().map(x -> x.secret().value().asString())
                .toList();
    }

    public K8sSecretsDto getContainerRegistryImagePullSecrets(TaskEntity task) {
        var containerRegistryCredentials = getContainerRegistryCredentials(task);
        return getRegistryImagePullSecrets(containerRegistryCredentials);
    }

    public List<RegistryCredentialDto> getHelmRegistryCredentials(TaskEntity task) {

        if (Strings.isNotBlank(task.getHelmChart())) {
            var helmRegistryHostname = getHelmRegistryHostname(task.getHelmChart());
            return getRegistryCredentials(task, helmRegistryHostname,
                                         ArtifactTypeEnum.HELM).stream()
                    .map(dto -> {
                        if (RegistryMapperService.isCanaryHostname(helmRegistryHostname)) {
                            return registryTaskMapperService
                                    .toRegistryCredentialDtoWithCanaryHostname(dto);
                        }
                        return dto;
                    })
                    .toList();
        }
        // Container-based function.
        return Collections.emptyList();
    }

    public List<String> getHelmRegistryCredentialsValue(TaskEntity taskEntity) {
        return getHelmRegistryCredentials(taskEntity)
                .stream().map(x -> x.secret().value().asString())
                .toList();
    }

    public K8sSecretsDto getHelmRegistryImagePullSecrets(TaskEntity task) {
        var helmRegistryCredentials = getHelmRegistryCredentials(task);
        return getRegistryImagePullSecrets(helmRegistryCredentials);
    }

    public Map<String, List<RegistryCredentialDto>> getModelRegistryCredentials(TaskEntity task) {
        var ncaId = task.getNcaId();
        var taskId = task.getTaskId();
        var normalizedModelRegistriesHostnames = getModelRegistriesHostnames(task);
        if (normalizedModelRegistriesHostnames.isEmpty()) {
            return Collections.emptyMap();
        }
        var modelRegistryCredentials = normalizedModelRegistriesHostnames.stream()
                .map(hostname -> {
                    var registryCredentials =
                            getRegistryCredentials(task, hostname, ArtifactTypeEnum.MODEL);

                    if (CollectionUtils.isEmpty(registryCredentials)) {
                        var mesg = MESG_MISSING_REGISTRY.formatted(ncaId, taskId,
                                                                   ArtifactTypeEnum.MODEL,
                                                                   hostname);
                        log.error(mesg);
                        throw new IllegalStateException(mesg);
                    }
                    return registryCredentials;
                })
                .flatMap(Collection::stream)
                .toList();
        return modelRegistryCredentials.stream()
                .flatMap(registryTaskMapperService::expandRegistryCredentialWithCanaryHostname)
                .collect(Collectors.groupingBy(RegistryCredentialDto::registryHostname));
    }

    public Map<String, List<String>> getModelRegistryCredentialsValue(TaskEntity taskEntity) {
        return getModelRegistryCredentials(taskEntity).entrySet()
                .stream()
                .collect(Collectors.toMap(
                        Map.Entry::getKey,
                        x -> x.getValue().stream()
                                .map(registryCredentialDto ->
                                             registryCredentialDto
                                                     .secret().value().asString())
                                .toList()));
    }

    public Map<String, List<RegistryCredentialDto>> getResourceRegistryCredentials(
            TaskEntity task) {
        var ncaId = task.getNcaId();
        var taskId = task.getTaskId();
        var normalizedResourceRegistriesHostnames = getResourceRegistriesHostnames(task);
        if (normalizedResourceRegistriesHostnames.isEmpty()) {
            return Collections.emptyMap();
        }
        var resourceRegistryCredentials = normalizedResourceRegistriesHostnames.stream()
                .map(hostname -> {
                    var registryCredentials =
                            getRegistryCredentials(task, hostname, ArtifactTypeEnum.RESOURCE);

                    if (CollectionUtils.isEmpty(registryCredentials)) {
                        var mesg = MESG_MISSING_REGISTRY.formatted(ncaId, taskId,
                                                                   ArtifactTypeEnum.RESOURCE,
                                                                   hostname);
                        log.error(mesg);
                        throw new IllegalStateException(mesg);
                    }
                    return registryCredentials;
                })
                .flatMap(Collection::stream)
                .toList();
        return resourceRegistryCredentials.stream()
                .flatMap(registryTaskMapperService::expandRegistryCredentialWithCanaryHostname)
                .collect(Collectors.groupingBy(RegistryCredentialDto::registryHostname));
    }

    public Map<String, List<String>> getResourceRegistryCredentialsValue(
            TaskEntity taskEntity) {
        return getResourceRegistryCredentials(taskEntity).entrySet()
                .stream()
                .collect(Collectors.toMap(
                        Map.Entry::getKey,
                        x -> x.getValue().stream()
                                .map(registryCredentialDto ->
                                             registryCredentialDto
                                                     .secret().value().asString())
                                .toList()));
    }

    public String getBase64EncodedContainerRegistryImagePullSecrets(TaskEntity task) {
        var k8SSecretsDto = getContainerRegistryImagePullSecrets(task);
        return base64Encode(task, k8SSecretsDto);
    }

    public String getBase64EncodedHelmRegistryImagePullSecrets(TaskEntity task) {
        var k8SSecretsDto = getHelmRegistryImagePullSecrets(task);
        return base64Encode(task, k8SSecretsDto);
    }

    private String base64Encode(TaskEntity task, Object secrets) {
        var taskId = task.getTaskId();
        var ncaId = task.getNcaId();

        try {
            var jsonString = jsonMapper.writeValueAsString(secrets);
            return java.util.Base64.getEncoder().encodeToString(jsonString.getBytes());
        } catch (JacksonException e) {
            var msg = MESG_FAIL_TO_ENCODE_SECRETS.formatted(ncaId, taskId);
            log.error(msg);
            throw new IllegalArgumentException(msg);
        }
    }
}
