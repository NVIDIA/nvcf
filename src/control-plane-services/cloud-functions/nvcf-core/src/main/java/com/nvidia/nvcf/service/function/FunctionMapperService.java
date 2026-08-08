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
package com.nvidia.nvcf.service.function;

import static com.nvidia.nvcf.persistence.function.entity.Protocol.GRPC;
import static com.nvidia.nvcf.persistence.function.entity.Protocol.HTTP;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_PORT;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_TIMEOUT;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvcf.icms.client.IcmsStubService;
import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.DeploymentHealthUdt;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentKey;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.persistence.function.entity.HealthUdt;
import com.nvidia.nvcf.persistence.function.entity.Protocol;
import com.nvidia.nvcf.persistence.function.entity.RateLimitUdt;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetriesUdt;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentHealthDto;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentDto;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.management.dto.ApiBodyFormatEnum;
import com.nvidia.nvcf.rest.function.management.dto.ArtifactDto;
import com.nvidia.nvcf.rest.function.management.dto.ContainerEnvironmentEntryDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.HealthDto;
import com.nvidia.nvcf.rest.function.management.dto.InstanceDto;
import com.nvidia.nvcf.rest.function.management.dto.LlmInvocationConfigDto;
import com.nvidia.nvcf.rest.function.management.dto.ProtocolEnum;
import com.nvidia.nvcf.rest.function.management.dto.RateLimitDto;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetriesDto;
import jakarta.annotation.Nullable;
import java.net.URI;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import tools.jackson.core.JacksonException;
import tools.jackson.core.type.TypeReference;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionMapperService {

    private static final String MESG_INVALID_API_BODY_FORMAT =
            "Invalid request: 'apiBodyFormat' in create function payload should not be null";
    private static final String MESG_LLM_CONFIG_VERSION_MISSING =
            "Missing or invalid llm_config storage version";
    private static final String MESG_LLM_CONFIG_VERSION_UNSUPPORTED =
            "Unsupported llm_config storage version: %d";
    private static final String MESG_LLM_CONFIG_DESERIALIZE_FAILED =
            "Failed to deserialize function llm_config";
    // Schema version stamped on the stored llm_config JSON. Storage-internal: it is not a member
    // of the typed DTO, so it never reaches the API or the client.
    private static final int LLM_CONFIG_VERSION = 1;
    private static final ListEnvTypeReference LIST_ENV_TYPE_REFERENCE = new ListEnvTypeReference();
    private final JsonMapper jsonMapper;
    private final FunctionDeploymentMapperService functionDeploymentMapperService;

    private record ModelSpecValue(
            String version,
            URI url,
            FunctionModelDto.LlmConfigDto llmConfig) {
    }

    private static class ListEnvTypeReference extends
            TypeReference<List<ContainerEnvironmentEntryDto>> {
    }

    public FunctionDto toFunctionDto(
            FunctionEntity entity,
            Optional<List<InstanceDto>> activeInstances,
            Optional<Set<String>> secrets) {
        Set<ArtifactDto> resources = null;
        if (!CollectionUtils.isEmpty(entity.getResources())) {
            resources = entity.getResources()
                    .stream()
                    .map(this::toArtifactDto)
                    .collect(Collectors.toSet());
        }
        var containerEnvironment = entity.getContainerEnvironment();
        var functionType = switch (entity.getFunctionType()) {
            case null -> FunctionTypeEnum.DEFAULT;
            case DEFAULT -> FunctionTypeEnum.DEFAULT;
            case STREAMING -> FunctionTypeEnum.STREAMING;
            case LLM -> FunctionTypeEnum.LLM;
        };
        var functionModels = toFunctionModels(entity.getModelSpecs());
        var builder = FunctionDto.builder()
                .ncaId(entity.getNcaId())
                .id(entity.getFunctionId())
                .name(entity.getFunctionName())
                .versionId(entity.getFunctionVersionId())
                .status(FunctionStatusEnum.fromText(entity.getFunctionStatus().toString()))
                .inferenceUrl(URI.create(entity.getInferenceUrl()))
                .inferencePort(entity.getInferencePort())
                .models(CollectionUtils.isEmpty(functionModels) ? null : functionModels)
                .apiBodyFormat(ApiBodyFormatEnum.fromText(entity.getApiBodyFormat().toString()))
                .containerArgs(entity.getContainerArgs())
                .createdAt(entity.getCreatedAt())
                .containerEnvironment(deserializeContainerEnvironment(containerEnvironment))
                .activeInstances(activeInstances.orElse(null))
                .resources(resources)
                .functionType(functionType)
                .secrets(secrets.orElse(null));

        if (entity.getRateLimit() != null && !entity.getRateLimit().isEmpty()) {
            builder.rateLimit(RateLimitDto.builder()
                                      .rateLimit(entity.getRateLimit().getRate())
                                      .exemptedNcaIds(entity.getRateLimit().getExemptedNcaIds())
                                      .perNcaIdRate(entity.getRateLimit().getPerNcaIdRate())
                                      .syncCheck(entity.getRateLimit().getSyncCheck())
                                      .perUserRate(entity.getRateLimit().getPerUserRate())
                                      .build());
        }

        if (entity.getContainerImage() != null) {
            builder.containerImage(URI.create(entity.getContainerImage()));
        }

        if (entity.getHelmChart() != null) {
            builder.helmChart(URI.create(entity.getHelmChart()));
            builder.helmChartServiceName(entity.getHelmChartServiceName());
        }

        if (entity.getTelemetries() != null) {
            var telemetries = entity.getTelemetries();
            var telemetriesDto = new TelemetriesDto(
                    telemetries.getLogsTelemetryId(),
                    telemetries.getMetricsTelemetryId(),
                    telemetries.getTracesTelemetryId()
            );
            builder.telemetries(telemetriesDto);
        }

        if (entity.getTags() != null && !entity.getTags().isEmpty()) {
            builder.tags(entity.getTags());
        }

        var llmInvocationConfig = toLlmInvocationConfigDto(entity.getLlmConfig());
        if (llmInvocationConfig != null) {
            builder.llmInvocationConfig(llmInvocationConfig);
        }

        if (isNotBlank(entity.getDescription())) {
            builder.description(entity.getDescription());
        }

        if (entity.getHealth() != null) {
            URI mdHealthUri = DEFAULT_HEALTH_ENDPOINT;
            if (isNotBlank(entity.getHealth().getUri())) {
                mdHealthUri = URI.create(entity.getHealth().getUri());
            }
            var protocol = switch (entity.getHealth().getProtocol()) {
                case GRPC -> ProtocolEnum.GRPC;
                case HTTP -> ProtocolEnum.HTTP;
            };
            builder.health(
                    HealthDto.builder()
                            .port(entity.getHealth().getPort())
                            .protocol(protocol)
                            .uri(mdHealthUri)
                            .timeout(entity.getHealth().getTimeout())
                            .expectedStatusCode(entity.getHealth().getExpectedStatusCode())
                            .build());
            builder.healthUri(mdHealthUri);
        }

        return builder.build();
    }

    public FunctionEntity toFunctionEntity(
            @NonNull String ncaId,
            @NonNull Optional<UUID> optFunctionId,
            @NonNull CreateFunctionRequest request,
            Set<String> functionLevelAuthzIds) {
        if (request.getApiBodyFormat() == null) {
            log.error(MESG_INVALID_API_BODY_FORMAT);
            throw new IllegalStateException(MESG_INVALID_API_BODY_FORMAT);
        }

        var resources = Set.<ResourceUdt>of();
        if (request.getResources() != null) {
            resources = request.getResources()
                    .stream()
                    .map(this::toResourceUdt)
                    .collect(Collectors.toSet());
        }
        var rateLimit = request.getRateLimit() == null ? null : toRateLimitUdt(request.getRateLimit());
        var functionType = switch (request.getFunctionType()) {
            case null -> FunctionType.DEFAULT;
            case DEFAULT -> FunctionType.DEFAULT;
            case STREAMING -> FunctionType.STREAMING;
            case LLM -> FunctionType.LLM;
        };
        var containerEnvironment = request.getContainerEnvironment();
        var builder = FunctionEntity.builder()
                .functionId(optFunctionId.orElseGet(UUID::randomUUID))
                .functionVersionId(UUID.randomUUID())
                .ncaId(ncaId)
                .functionName(request.getName())
                .functionStatus(FunctionStatus.INACTIVE)
                .inferenceUrl(request.getInferenceUrl().toString())
                .inferencePort(request.getInferencePort())
                .apiBodyFormat(ApiBodyFormat.fromText(request.getApiBodyFormat().toString()))
                .containerArgs(request.getContainerArgs())
                .containerEnvironment(serializeContainerEnvironment(containerEnvironment))
                .utilsContainerImage(request.getUtilsContainerImage())
                .createdAt(Instant.now())
                .resources(resources)
                .rateLimit(rateLimit)
                .modelSpecs(toModelSpecs(request.getModels()))
                .llmConfig(toLlmInvocationConfigJson(request.getLlmInvocationConfig()))
                .functionType(functionType)
                .tags(request.getTags())
                .description(Optional.ofNullable(request.getDescription())
                                     .orElse(request.getName()));

        if (request.getContainerImage() != null) {
            builder.containerImage(request.getContainerImage().toString());
        }

        if (request.getHelmChart() != null) {
            builder.helmChart(request.getHelmChart().toString());
            builder.helmChartServiceName(request.getHelmChartServiceName());
        }

        if (request.getHealth() != null) {
            var protocol = switch (request.getHealth().getProtocol()) {
                case ProtocolEnum.GRPC -> GRPC;
                case ProtocolEnum.HTTP -> HTTP;
            };

            builder.health(
                    HealthUdt.builder()
                            .protocol(protocol)
                            .uri(extractHealthUri(request))
                            .port(request.getHealth().getPort())
                            .timeout(request.getHealth().getTimeout())
                            .expectedStatusCode(request.getHealth().getExpectedStatusCode())
                            .build());
        } else {
            Protocol protocol = HTTP;
            if (isNotBlank(request.getInferenceUrl().toString())) {
                protocol = "/grpc".equals(request.getInferenceUrl().toString()) ? GRPC : HTTP;
            }

            // Being consistent with defaults. Since we are setting default values for protocol,
            // port, timeout, and status code fields, uri is also being defaulted. Also, we should
            // have all the clients move off of the deprecated healthUri and maybe delete that
            // field in the request eventually.
            builder.health(
                    HealthUdt.builder()
                            .protocol(protocol)
                            .uri(request.getHealthUri() != null
                                         ? request.getHealthUri().toString() :
                                         DEFAULT_HEALTH_ENDPOINT.toString())
                            .port(request.getInferencePort() != null ? request.getInferencePort()
                                          : DEFAULT_HEALTH_PORT)
                            .timeout(DEFAULT_HEALTH_TIMEOUT)
                            .expectedStatusCode(DEFAULT_HEALTH_EXPECTED_STATUS_CODE)
                            .build());
        }

        if (request.getTelemetries() != null) {
            var telemetryDto = request.getTelemetries();
            builder.telemetries(
                    TelemetriesUdt.builder()
                            .logsTelemetryId(telemetryDto.logsTelemetryId())
                            .metricsTelemetryId(telemetryDto.metricsTelemetryId())
                            .tracesTelemetryId(telemetryDto.tracesTelemetryId())
                            .build()
                               );
        }

        builder.functionLevelAuthorizedAccounts(functionLevelAuthzIds);

        // Set has_secrets based on whether secrets are provided in the request
        builder.hasSecrets(!CollectionUtils.isEmpty(request.getSecrets()));

        return builder.build();
    }

    /**
     * Serializes the function-level LLM invocation config to its stored JSON form, stamping the
     * storage schema version. Returns null only when the config is absent.
     */
    @Nullable
    @SneakyThrows
    public String toLlmInvocationConfigJson(@Nullable LlmInvocationConfigDto llmInvocationConfig) {
        if (llmInvocationConfig == null) {
            return null;
        }
        // Stamp the storage schema version so a future breaking change can branch on it. It stays
        // internal to storage (not on the DTO), and the current single version maps straight back.
        var node = (ObjectNode) jsonMapper.valueToTree(llmInvocationConfig);
        node.put("version", LLM_CONFIG_VERSION);
        return jsonMapper.writeValueAsString(node);
    }

    /**
     * Deserializes the stored llm_config JSON back into a DTO. Returns null when the column is
     * blank. Validates the storage schema version stamped by {@link #toLlmInvocationConfigJson}
     * and throws on a missing, unsupported, or corrupt blob, surfacing the problem rather than
     * silently dropping or misreading the config.
     */
    @Nullable
    public LlmInvocationConfigDto toLlmInvocationConfigDto(@Nullable String llmConfigJson) {
        if (StringUtils.isBlank(llmConfigJson)) {
            return null;
        }
        try {
            var root = jsonMapper.readTree(llmConfigJson);
            var versionNode = root.get("version");
            if (versionNode == null || !versionNode.isInt()) {
                log.error(MESG_LLM_CONFIG_VERSION_MISSING);
                throw new IllegalStateException(MESG_LLM_CONFIG_VERSION_MISSING);
            }
            if (versionNode.intValue() != LLM_CONFIG_VERSION) {
                var mesg = MESG_LLM_CONFIG_VERSION_UNSUPPORTED.formatted(versionNode.intValue());
                log.error(mesg);
                throw new IllegalStateException(mesg);
            }
            // Strip the storage-internal version so it never reaches the typed DTO, regardless of
            // the object mapper's unknown-property setting.
            if (root instanceof ObjectNode objectNode) {
                objectNode.remove("version");
            }
            return jsonMapper.treeToValue(root, LlmInvocationConfigDto.class);
        } catch (JacksonException exception) {
            log.error(MESG_LLM_CONFIG_DESERIALIZE_FAILED, exception);
            throw new IllegalStateException(MESG_LLM_CONFIG_DESERIALIZE_FAILED, exception);
        }
    }

    public RateLimitUdt toRateLimitUdt(RateLimitDto rateLimitDto) {
        return RateLimitUdt.builder()
                .rate(rateLimitDto.rateLimit())
                .exemptedNcaIds(rateLimitDto.exemptedNcaIds())
                .perNcaIdRate(rateLimitDto.perNcaIdRate())
                .syncCheck(rateLimitDto.syncCheck())
                .perUserRate(rateLimitDto.perUserRate())
                .build();
    }

    /**
     * Build deployment entity for create. GPU specs are stored in the gpu_specifications
     * table; use {@link FunctionDeploymentMapperService#toGpuSpecificationEntities} for
     * the list to persist and for any logic that needs GPU spec data.
     */
    public FunctionDeploymentEntity toFunctionDeploymentEntity(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            UUID deploymentId) {
        var key = FunctionDeploymentKey.builder()
                .functionVersionId(functionVersionId)
                .build();
        return FunctionDeploymentEntity.builder()
                .key(key)
                .deploymentId(deploymentId)
                .functionId(functionId)
                .ncaId(ncaId)
                .createdAt(Instant.now())
                .lastUpdatedAt(Instant.now())
                .build();
    }

    public static  Map<UUID, GpuSpecificationDto> buildUuidGpuSpecificationDtoMap(
            FunctionDeploymentRequest deploymentRequest) {
        var gpuSpecDtos = deploymentRequest.deploymentSpecifications();
        return gpuSpecDtos.stream()
                .collect(Collectors.toMap(spec -> UUID.randomUUID(), spec -> spec));
    }

    /**
     * Build DTO from DeploymentWithGpuSpecs.
     */
    public FunctionDeploymentDto toFunctionDeploymentDto(
            FunctionDeploymentContext with,
            FunctionStatus status,
            String functionName,
            Map<String, Set<IcmsStubService.InstanceTypeDetails>> instanceTypes) {
        var entity = with.deployment();
        var gpuSpecDtos = with.gpuSpecs().stream()
                .map(e -> functionDeploymentMapperService.toGpuSpecificationDto(e, instanceTypes))
                .toList();
        var lastUpdatedAt = entity.getLastUpdatedAt() != null
                ? entity.getLastUpdatedAt()
                : entity.getCreatedAt();
        var builder = FunctionDeploymentDto.builder()
                .functionId(entity.getFunctionId())
                .functionVersionId(entity.getKey().getFunctionVersionId())
                .deploymentId(entity.getDeploymentId())
                .functionName(functionName)
                .ncaId(entity.getNcaId())
                .functionStatus(FunctionStatusEnum.fromText(status.name()))
                .deploymentSpecifications(gpuSpecDtos)
                .createdAt(entity.getCreatedAt())
                .lastUpdatedAt(lastUpdatedAt);

        if ((entity.getHealthInfo() != null) && !entity.getHealthInfo().isEmpty()) {
            var healthInfoDtos = entity.getHealthInfo().stream()
                    .map(FunctionMapperService::toDeploymentHealthDto)
                    .toList();
            builder.healthInfo(healthInfoDtos);
        }
        return builder.build();
    }

    public List<FunctionModelDto> toFunctionModels(@Nullable Map<String, String> modelSpecs) {
        if (CollectionUtils.isEmpty(modelSpecs)) {
            return List.of();
        }
        var models = new ArrayList<FunctionModelDto>();
        for (var entry : modelSpecs.entrySet()) {
            ModelSpecValue modelValue;
            try {
                modelValue = jsonMapper.readValue(entry.getValue(), ModelSpecValue.class);
            } catch (JacksonException exception) {
                throw new IllegalStateException("Failed to deserialize function model spec",
                                                exception);
            }
            var llmConfig = modelValue.llmConfig();
            models.add(FunctionModelDto.builder()
                               .name(entry.getKey())
                               .version(modelValue.version())
                               .uri(modelValue.url())
                               .llmConfig(llmConfig != null && llmConfig.hasContent() ? llmConfig
                                       : null)
                               .build());
        }
        return models;
    }

    @Nullable
    @SneakyThrows
    public Map<String, String> toModelSpecs(List<FunctionModelDto> models) {
        if (CollectionUtils.isEmpty(models)) {
            return null;
        }
        var modelSpecs = new LinkedHashMap<String, String>();
        for (var model : models) {
            var llmConfig = model.getLlmConfig();
            modelSpecs.put(model.getName(), jsonMapper.writeValueAsString(new ModelSpecValue(
                    model.getVersion(),
                    model.getUri(),
                    llmConfig != null && llmConfig.hasContent() ? llmConfig : null)));
        }
        return modelSpecs;
    }

    /**
     * Apply partial {@code tokenRateLimit} / {@code routingMethod} overrides to the llmConfig of
     * each matching model in the provided {@code existingModelSpecs}. Models whose names are not
     * in {@code overridesByModelName}, or models without an existing {@code llmConfig}, are left
     * untouched. Fields on the override with {@code null} values are ignored, so callers can send
     * only the fields they want to change.
     */
    @Nullable
    public Map<String, String> applyLlmConfigOverrides(
            @Nullable Map<String, String> existingModelSpecs,
            Map<String, FunctionModelDto.LlmConfigDto> overridesByModelName) {
        if (CollectionUtils.isEmpty(existingModelSpecs)
                || CollectionUtils.isEmpty(overridesByModelName)) {
            return existingModelSpecs;
        }
        var models = toFunctionModels(existingModelSpecs);
        for (var model : models) {
            var override = overridesByModelName.get(model.getName());
            if (override == null || model.getLlmConfig() == null) {
                continue;
            }
            if (override.getTokenRateLimit() != null) {
                model.getLlmConfig().setTokenRateLimit(override.getTokenRateLimit());
            }
            if (override.getRoutingMethod() != null) {
                model.getLlmConfig().setRoutingMethod(override.getRoutingMethod());
            }
        }
        return toModelSpecs(models);
    }

    @VisibleForTesting
    public ArtifactDto toArtifactDto(ResourceUdt resourceUdt) {
        return ArtifactDto.builder()
                .name(resourceUdt.getName())
                .version(resourceUdt.getVersion())
                .uri(URI.create(resourceUdt.getUrl()))
                .build();
    }

    private ResourceUdt toResourceUdt(ArtifactDto artifactDto) {
        return ResourceUdt.builder()
                .name(artifactDto.getName())
                .version(artifactDto.getVersion())
                .url(artifactDto.getUri().toString())
                .build();
    }

    @SneakyThrows
    private String serializeContainerEnvironment(List<ContainerEnvironmentEntryDto> environment) {
        if (Objects.isNull(environment) || environment.isEmpty()) {
            return null;
        }

        var json = jsonMapper.writeValueAsBytes(environment);
        return Base64.getEncoder().encodeToString(json);
    }

    @SneakyThrows
    private List<ContainerEnvironmentEntryDto> deserializeContainerEnvironment(String env) {
        if (StringUtils.isBlank(env)) {
            return null;
        }

        var json = Base64.getDecoder().decode(env);
        return jsonMapper.readValue(json, LIST_ENV_TYPE_REFERENCE);
    }

    private static DeploymentHealthDto toDeploymentHealthDto(DeploymentHealthUdt udt) {
        return DeploymentHealthDto.builder()
                .icmsRequestId(udt.getIcmsRequestId())
                .backend(udt.getBackend())
                .gpu(udt.getGpu())
                .instanceType(udt.getInstanceType())
                .error(udt.getError())
                .build();
    }

    private static String extractHealthUri(CreateFunctionRequest request) {
        if (request.getHealth() != null) {
            return request.getHealth().getUri().toString(); // uri is a mandatory field in HealthDto
        }

        var healthUri = request.getHealthUri();
        return (healthUri != null) ? healthUri.toString() : DEFAULT_HEALTH_ENDPOINT.toString();
    }

}
