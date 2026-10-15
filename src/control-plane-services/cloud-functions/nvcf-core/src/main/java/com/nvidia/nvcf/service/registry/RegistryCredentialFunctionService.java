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
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.MODEL;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.RESOURCE;

import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.registry.entity.ArtifactType;
import com.nvidia.nvcf.persistence.registry.entity.RegistryCredentialByAccountEntity;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.registry.dto.DockerConfigJsonAuthDto;
import com.nvidia.nvcf.rest.registry.dto.DockerConfigJsonDto;
import com.nvidia.nvcf.rest.registry.dto.K8sSecretsDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import java.net.URI;
import java.util.ArrayList;
import java.util.Collection;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.logging.log4j.util.Strings;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RefreshScope
public class RegistryCredentialFunctionService {

    private static final List<String> SUPPORTED_HELM_CHART_SCHEMES =
            List.of("http", "https", "oci");

    private static final String MESG_COMMON_ERROR_PREFIX =
            "Account '%s', Function '%s', Version '%s': ";
    private static final String MESG_MISSING_REGISTRY =
            MESG_COMMON_ERROR_PREFIX + "Missing %s registry for hostname '%s'";
    private static final String MESG_FAIL_TO_ENCODE_SECRETS =
            MESG_COMMON_ERROR_PREFIX + "Failed to encode secrets";
    private static final String MESG_REGISTRY_CREDENTIAL_IN_USE_CANNOT_DELETE =
            "Registry Credential '%s': Cannot be deleted as it is in use by Function id '%s', " +
                    "version '%s' - Delete the function first to be able to delete the " +
                    "registry credential.";
    private static final String MESG_INVALID_REGISTRY_URL =
            "Invalid registry URL: %s";

    private final JsonMapper jsonMapper;
    private final FunctionMapperService functionMapperService;
    private final FunctionLookupService functionLookupService;
    private final RegistryFunctionMapperService registryFunctionMapperService;
    private final RegistryCredentialLookupService registryCredentialLookupService;
    private final RegistryMapperService registryMapperService;
    private final String sidecarRegistryHostname;
    private final String sidecarImagePullSecret;

    private record RegistryCredentialContext(
            FunctionEntity functionEntity,
            RegistryCredentialByAccountEntity registryCredentialByAccountEntity) {
    }

    public RegistryCredentialFunctionService(
            JsonMapper jsonMapper,
            FunctionMapperService functionMapperService,
            FunctionLookupService functionLookupService,
            RegistryFunctionMapperService registryFunctionMapperService,
            RegistryCredentialLookupService registryCredentialLookupService,
            RegistryMapperService registryMapperService,
            @Value("${nvcf.sidecars.image-pull-secret}")
            String sidecarImagePullSecret,
            @Value("${nvcf.sidecars.hostname}")
            String sidecarRegistryHostname) {
        this.jsonMapper = jsonMapper;
        this.functionMapperService = functionMapperService;
        this.functionLookupService = functionLookupService;
        this.registryFunctionMapperService = registryFunctionMapperService;
        this.registryCredentialLookupService = registryCredentialLookupService;
        this.registryMapperService = registryMapperService;
        this.sidecarImagePullSecret = sidecarImagePullSecret;
        this.sidecarRegistryHostname = sidecarRegistryHostname;
    }

    // If it is a container-based function, then return just the container registry credentials
    // that match the hostname specified in the function definition. If it is a helm-based
    // function, then return all the container registry credentials associated with the account.
    public List<RegistryCredentialDetailsDto> getContainerRegistryCredentialDetails(
            FunctionEntity function) {
        var ncaId = function.getNcaId();
        if (Strings.isNotBlank(function.getContainerImage())) {
            var containerRegistryHostname = getRegistryHostname(function.getContainerImage());
            // Container-based function. Return container registry credentials whose hostname
            // matches the hostname specified in the function definition.
            return registryCredentialLookupService
                    .getRegistryCredentialDtos(ncaId, containerRegistryHostname, CONTAINER)
                    .stream()
                    .map(dto -> {
                        if (RegistryMapperService.isCanaryHostname(containerRegistryHostname)) {
                            return registryFunctionMapperService
                                    .toRegistryCredentialDetailsWithCanaryHostname(dto);
                        }
                        return dto;
                    })
                    .toList();
        }

        // Helm-based function. Return all the container registry credentials associated with the
        // account and duplicated NGC registry with canary alias hostname, since we don't know
        // which registry hostname is in use by the helm chart.
        return registryCredentialLookupService
                .getRegistryCredentialDtos(ncaId, Set.of(CONTAINER))
                .stream()
                .flatMap(this::expandRegistryCredentialWithCanaryHostname)
                .toList();
    }

    private Stream<RegistryCredentialDetailsDto> expandRegistryCredentialWithCanaryHostname(
            RegistryCredentialDetailsDto registryDetailsDto) {
        var originalHostname = registryDetailsDto.registryHostname();
        var canaryHostname = registryMapperService.toCanaryHostname(originalHostname);

        if (canaryHostname.equals(originalHostname)) {
            return Stream.of(registryDetailsDto);
        }
        return Stream.of(
                registryDetailsDto,
                registryFunctionMapperService
                        .toRegistryCredentialDetailsWithCanaryHostname(registryDetailsDto));
    }

    public List<RegistryCredentialDetailsDto> getHelmRegistryCredentialDetails(
            FunctionEntity function) {
        var ncaId = function.getNcaId();

        if (Strings.isNotBlank(function.getHelmChart())) {
            // Helm-based Function. Return helm registry credentials whose hostname matches
            // the one specified in the function definition.
            var helmRegistryHostname = getHelmRegistryHostname(function.getHelmChart());

            return registryCredentialLookupService
                    .getRegistryCredentialDtos(ncaId, helmRegistryHostname, HELM)
                    .stream()
                    .map(dto -> {
                        if (RegistryMapperService.isCanaryHostname(helmRegistryHostname)) {
                            return registryFunctionMapperService
                                    .toRegistryCredentialDetailsWithCanaryHostname(dto);
                        }
                        return dto;
                    })
                    .toList();
        }

        // Container-based function.
        return Collections.emptyList();
    }

    public Map<String, List<RegistryCredentialDetailsDto>> getModelRegistryCredentialsMap(
            FunctionEntity function) {
        return getModelRegistryCredentialsMap(function,
                functionMapperService.toFunctionModels(function.getModelSpecs()));
    }

    public Map<String, List<RegistryCredentialDetailsDto>> getModelRegistryCredentialsMap(
            FunctionEntity function, List<FunctionModelDto> models) {
        var ncaId = function.getNcaId();
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var normalizedModelRegistriesHostnames = getModelRegistriesHostnames(models);
        if (normalizedModelRegistriesHostnames.isEmpty()) {
            return Collections.emptyMap();
        }
        var modelRegistryCredentials = normalizedModelRegistriesHostnames
                .stream()
                .map(hostname -> {
                    var dtos = registryCredentialLookupService.getRegistryCredentialDtos(ncaId,
                                                                                         hostname,
                                                                                         MODEL);
                    if (CollectionUtils.isEmpty(dtos)) {
                        var mesg = MESG_MISSING_REGISTRY.formatted(ncaId, functionId, versionId,
                                                                   MODEL, hostname);
                        log.error(mesg);
                        throw new IllegalStateException(mesg);
                    }
                    return dtos;
                })
                .flatMap(Collection::stream)
                .toList();

        return modelRegistryCredentials.stream()
                .flatMap(this::expandRegistryCredentialWithCanaryHostname)
                .collect(Collectors.groupingBy(RegistryCredentialDetailsDto::registryHostname));
    }

    public Map<String, List<RegistryCredentialDetailsDto>> getResourceRegistryCredentialsMap(
            FunctionEntity function) {
        var ncaId = function.getNcaId();
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var normalizedResourceRegistriesHostnames = getResourceRegistriesHostnames(function);
        if (normalizedResourceRegistriesHostnames.isEmpty()) {
            return Collections.emptyMap();
        }

        var resourceRegistries = normalizedResourceRegistriesHostnames
                .stream()
                .map(hostname -> {
                    var dtos = registryCredentialLookupService.getRegistryCredentialDtos(ncaId,
                                                                                         hostname,
                                                                                         RESOURCE);
                    if (CollectionUtils.isEmpty(dtos)) {
                        var mesg = MESG_MISSING_REGISTRY.formatted(ncaId, functionId, versionId,
                                                                   RESOURCE, hostname);
                        log.error(mesg);
                        throw new IllegalStateException(mesg);
                    }
                    return dtos;
                })
                .flatMap(Collection::stream)
                .toList();

        return resourceRegistries.stream()
                .flatMap(this::expandRegistryCredentialWithCanaryHostname)
                .collect(Collectors.groupingBy(RegistryCredentialDetailsDto::registryHostname));
    }

    public String getRegistryHostname(String artifactUrl) {
        // WAR for using URI lib to parse url host, since image url won't include protocol prefix.
        var normalizedArtifactUrl = artifactUrl;
        if (!artifactUrl.startsWith("oci://")) {
            normalizedArtifactUrl = !artifactUrl.startsWith("http") ? "https://" + artifactUrl : artifactUrl;
        }
        return Optional.ofNullable(URI.create(normalizedArtifactUrl).getHost())
                .orElseThrow(() -> new IllegalStateException(
                        MESG_INVALID_REGISTRY_URL.formatted(artifactUrl)));
    }

    public String getHelmRegistryHostname(String artifactUrl) {
        var uri = URI.create(artifactUrl);
        var scheme = uri.getScheme();

        // Validate scheme - only allow http, https, oci
        if (isSchemeInvalid(scheme)) {
            throw new IllegalStateException(
                    MESG_INVALID_REGISTRY_URL.formatted(artifactUrl));
        }

        return Optional.ofNullable(uri.getHost())
                .orElseThrow(() -> new IllegalStateException(
                        MESG_INVALID_REGISTRY_URL.formatted(artifactUrl)));
    }

    @SneakyThrows
    public List<String> getModelRegistriesHostnames(FunctionEntity function) {
        return getModelRegistriesHostnames(
                functionMapperService.toFunctionModels(function.getModelSpecs()));
    }

    @SneakyThrows
    public List<String> getModelRegistriesHostnames(List<FunctionModelDto> models) {
        if (CollectionUtils.isEmpty(models)) {
            return Collections.emptyList();
        }
        return models.stream()
                .filter(m -> m.getUri() != null)
                .map(m -> getRegistryHostname(m.getUri().toString()))
                .toList();
    }

    @SneakyThrows
    public List<String> getResourceRegistriesHostnames(FunctionEntity function) {
        var resources = function.getResources();
        if (CollectionUtils.isEmpty(resources)) {
            return Collections.emptyList();
        }

        return resources.stream()
                .map(resource -> getRegistryHostname(resource.getUrl()))
                .toList();
    }

    private K8sSecretsDto getRegistryImagePullSecrets(
            List<RegistryCredentialDetailsDto> registryCredentials) {
        K8sSecretsDto k8SSecretsDto = K8sSecretsDto.builder().k8sSecrets(new ArrayList<>()).build();
        registryCredentials.forEach(registry -> {
            var registryDto = registryFunctionMapperService.toRegistryCredentialDto(registry);
            if (registryDto != null) {
                var hostname = registryDto.registryHostname();
                var secret = registryDto.secret().value().asString();
                var dockerConfigJsonRegistryCredentialDto = DockerConfigJsonAuthDto
                        .builder()
                        .auth(secret)
                        .build();
                k8SSecretsDto.k8sSecrets().add(
                        DockerConfigJsonDto.builder().auths(
                                Map.of(hostname, dockerConfigJsonRegistryCredentialDto)).build());
            }
        });
        return k8SSecretsDto;
    }

    public K8sSecretsDto getContainerRegistryImagePullSecrets(FunctionEntity function) {
        var containerRegistryCredentials = getContainerRegistryCredentialDetails(function);
        return getRegistryImagePullSecrets(containerRegistryCredentials);
    }

    public K8sSecretsDto getHelmRegistryImagePullSecrets(FunctionEntity function) {
        var helmRegistryCredentials = getHelmRegistryCredentialDetails(function);
        return getRegistryImagePullSecrets(helmRegistryCredentials);
    }

    public String base64Encode(FunctionEntity function, Object secrets) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var ncaId = function.getNcaId();

        try {
            var jsonString = jsonMapper.writeValueAsString(secrets);
            return java.util.Base64.getEncoder().encodeToString(jsonString.getBytes());
        } catch (JacksonException e) {
            var msg = MESG_FAIL_TO_ENCODE_SECRETS.formatted(ncaId, functionId, versionId);
            log.error(msg);
            throw new IllegalArgumentException(msg);
        }
    }

    public String getBase64EncodedContainerRegistryImagePullSecrets(FunctionEntity function) {
        var k8SSecretsDto = getContainerRegistryImagePullSecrets(function);
        return base64Encode(function, k8SSecretsDto);
    }

    public String getBase64EncodedHelmRegistryImagePullSecrets(FunctionEntity function) {
        var k8SSecretsDto = getHelmRegistryImagePullSecrets(function);
        return base64Encode(function, k8SSecretsDto);
    }

    public String getBase64EncodedSidecarRegistryImagePullSecret(FunctionEntity function) {
        var dockerConfigJsonDto = DockerConfigJsonDto.builder()
                .auths(Map.of(
                        this.sidecarRegistryHostname,
                        DockerConfigJsonAuthDto.builder()
                                .auth(this.sidecarImagePullSecret)
                                .build()
                             ))
                .build();
        return base64Encode(function, dockerConfigJsonDto);
    }

    public List<String> validateRegistryCredentialDeletion(
            String ncaId,
            RegistryCredentialByAccountEntity entity) {
        var keys = getRegistryCredentialDependentFunctions(ncaId, entity);
        if (CollectionUtils.isEmpty(keys)) {
            // No function is dependent on the Registry that is to be deleted. Proceed with
            // deletion.
            return Collections.emptyList();
        }

        // There are one or more functions that are using the specified Registry Credential. Unless
        // those function(s) are deleted, the specified Registry Credential cannot be deleted.
        var registryCredentialId = entity.getKey().getRegistryCredentialId();
        return keys.stream().map(key -> toMessage(registryCredentialId, key)).toList();
    }

    private List<FunctionEntity> getRegistryCredentialDependentFunctions(
            String ncaId,
            RegistryCredentialByAccountEntity registryCredentialByAccountEntity) {
        var functions = functionLookupService.lookupEntitiesUsingAccountId(ncaId);
        return functions
                .map(function -> new RegistryCredentialContext(function,
                                                               registryCredentialByAccountEntity))
                .map(this::registryCredentialDependentFunction)
                .filter(Optional::isPresent)
                .map(Optional::get)
                .toList();
    }

    private boolean isMatchingTargetHostname(String hostname, String artifactUrl) {
        if (StringUtils.isBlank(artifactUrl)) {
            return false;
        }
        var artifactHostname = getRegistryHostname(artifactUrl);
        return registryMapperService.toNormalizedHostname(artifactHostname).equals(hostname);
    }

    // Returns FunctionByAccountKey if the function in the context has hostname in any of its
    // URLs(containerImage, helmChart, models, resources) that matches the hostname of the registry
    // credential in the context. Otherwise, returns an empty optional.
    private Optional<FunctionEntity> registryCredentialDependentFunction(
            RegistryCredentialContext context) {

        var credentialEntity = context.registryCredentialByAccountEntity();
        var functionEntity = context.functionEntity();
        var hostname = credentialEntity.getRegistryHostname();

        for (var artifactType : credentialEntity.getArtifactTypes()) {
            if (matchesArtifact(artifactType, functionEntity, hostname)) {
                return Optional.of(functionEntity);
            }
        }
        return Optional.empty();
    }

    private boolean matchesArtifact(
            ArtifactType artifactType,
            FunctionEntity entity,
            String hostname) {
        return switch (artifactType) {
            case CONTAINER -> isMatchingTargetHostname(hostname, entity.getContainerImage());
            case HELM -> isMatchingTargetHostname(hostname, entity.getHelmChart());
            case MODEL -> {
                var models = functionMapperService.toFunctionModels(entity.getModelSpecs());
                yield !models.isEmpty() && models.stream()
                        .filter(m -> m.getUri() != null)
                        .anyMatch(m -> isMatchingTargetHostname(hostname, m.getUri().toString()));
            }
            case RESOURCE -> !CollectionUtils.isEmpty(entity.getResources())
                    && entity.getResources().stream()
                    .anyMatch(resource -> isMatchingTargetHostname(hostname, resource.getUrl()));
        };
    }

    private static String toMessage(UUID registryCredentialId,
                                    FunctionEntity functionEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        return MESG_REGISTRY_CREDENTIAL_IN_USE_CANNOT_DELETE
                .formatted(registryCredentialId, functionId, versionId);
    }

    private boolean isSchemeInvalid(String scheme) {
        return StringUtils.isBlank(scheme)
                || SUPPORTED_HELM_CHART_SCHEMES.stream().noneMatch(scheme::equalsIgnoreCase);
    }

    public Map<String, List<String>> getModelRegistryCredentialValues(FunctionEntity function) {
        return getModelRegistryCredentialValues(function,
                functionMapperService.toFunctionModels(function.getModelSpecs()));
    }

    public Map<String, List<String>> getModelRegistryCredentialValues(
            FunctionEntity function, List<FunctionModelDto> models) {
        return getModelRegistryCredentialsMap(function, models).entrySet()
                .stream()
                .collect(Collectors.toMap(
                        Map.Entry::getKey,
                        x -> x.getValue().stream()
                                .map(registryFunctionMapperService::toRegistryCredentialDto)
                                .filter(Objects::nonNull)
                                .map(registryDto -> registryDto.secret().value().asString())
                                .toList()));
    }

    public Map<String, List<String>> getResourceRegistryCredentialValues(FunctionEntity function) {
        return getResourceRegistryCredentialsMap(function).entrySet()
                .stream()
                .collect(Collectors.toMap(
                        Map.Entry::getKey,
                        x -> x.getValue().stream()
                                .map(registryFunctionMapperService::toRegistryCredentialDto)
                                .filter(Objects::nonNull)
                                .map(registryDto -> registryDto.secret().value().asString())
                                .toList()));
    }

    public List<String> getHelmRegistryCredentialValues(FunctionEntity function) {
        return getHelmRegistryCredentialDetails(function).stream()
                .map(registryFunctionMapperService::toRegistryCredentialDto)
                .filter(Objects::nonNull)
                .map(x -> x.secret().value().asString())
                .toList();
    }

    public List<String> getContainerRegistryCredentialValues(FunctionEntity function) {
        return getContainerRegistryCredentialDetails(function).stream()
                .map(registryFunctionMapperService::toRegistryCredentialDto)
                .filter(Objects::nonNull)
                .map(x -> x.secret().value().asString())
                .toList();
    }
}
