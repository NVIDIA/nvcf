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
package com.nvidia.nvcf.rest.misc;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static com.nvidia.nvcf.util.NvcfUtils.filterBlankStrings;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.icms.allocator.IcmsAllocatorService;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterResponse;
import com.nvidia.nvcf.persistence.function.entity.Backend;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.Gpu;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.rest.function.management.dto.BackendEnum;
import com.nvidia.nvcf.rest.function.management.dto.GpuDto;
import com.nvidia.nvcf.rest.function.management.dto.GpuEnum;
import com.nvidia.nvcf.rest.misc.dto.GoldenImagesResponse;
import com.nvidia.nvcf.rest.misc.dto.GpuPlacementDto;
import com.nvidia.nvcf.rest.misc.dto.GpuUsageDto;
import com.nvidia.nvcf.rest.misc.dto.ListGpuUsageResponse;
import com.nvidia.nvcf.rest.misc.dto.RolloverRequest;
import com.nvidia.nvcf.rest.misc.dto.RolloverSpecificationDto;
import com.nvidia.nvcf.rest.misc.dto.RolloverWorkersResponse;
import com.nvidia.nvcf.rest.misc.dto.SidecarProperties;
import com.nvidia.nvcf.rest.misc.dto.SidecarsDto;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.util.CollectionUtils;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Tag(name = "Cross-Account Miscellaneous API for NVIDIA Super Admins",
        description = """
                Defines miscellaneous endpoints for smooth service operation by NVIDIA Super
                 Admins such as SREs.""")
public class XAccountMiscEndpointsController {

    private static final String MESG_INVALID_ROLLOVER_SPEC =
            "Function '%s', version '%s': Rollover spec with gpu '%s' and instance type '%s' "
                    + "does not match any of the currently deployed specs";
    private static final String MESG_INVALID_NUM_INSTANCES =
            "Function '%s', version '%s': Number of instances '%s' in the rollover spec for " +
                    "gpu '%s' and instance type '%s' cannot be higher than max instances '%s' " +
                    "specified in the corresponding deployed spec";

    private static final String NCA_ID_DESCRIPTION = "NVIDIA Cloud Account Id";
    private static final EnumSet<FunctionStatus> ACTIVE_OR_DEPLOYING =
            EnumSet.of(FunctionStatus.ACTIVE, FunctionStatus.DEPLOYING);

    private static final List<GpuDto> SUPPORTED_GPUS = supportedGpus();

    private final IcmsClient icmsClient;
    private final FunctionLookupService functionLookupService;
    private final IcmsAllocatorService icmsAllocatorService;
    private final SidecarProperties sidecarsConfig;
    private final RateLimiterService rateLimiterService;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;
    private final Tracer tracer;

    private record RolloverContext(
            FunctionEntity function,
            FunctionDeploymentEntity deployment,
            GpuSpecificationEntity gpuDeploymentSpec,
            RolloverSpecificationDto rolloverSpec) {

    }

    @GetMapping("/v2/nvcf/supportedGpus")
    @Operation(
            summary = "Supported Backends and GPUs",
            description = """
                    Deprecated. Please use Cluster Group endpoints. Requires a bearer token
                     with 'register_function' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('register_function', 'admin:register_function')")
    @Deprecated(forRemoval = true)
    public List<GpuDto> getSupportedGpus() {
        return SUPPORTED_GPUS;
    }

    private static List<GpuDto> supportedGpus() {
        var gpus = Arrays.stream(GpuEnum.values()).toList();
        var backends = Arrays.stream(BackendEnum.values());

        return backends.filter(backend -> backend != BackendEnum.UNDEFINED)
                .flatMap(backend -> gpus.stream().map(gpu -> {
                    var backendEntity = Backend.fromText(backend.toString());
                    return backendEntity.getInstanceType(
                                    Gpu.fromText(gpu.toString()))
                            .map(val -> GpuDto.builder()
                                    .gpu(gpu)
                                    .backend(backend)
                                    .build());
                }))
                .flatMap(Optional::stream)
                .toList();
    }

    @GetMapping("/v2/nvcf/sidecars/goldenimages")
    @Operation(
            summary = "Get Golden Images",
            description = """
                    Returns the NVCF Golden Images that are currently promoted.
                    Requires a bearer token with 'admin:deploy_function' scope
                    in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public GoldenImagesResponse getGoldenImages() {
        SidecarsDto sidecars = new SidecarsDto(
                sidecarsConfig.getInferenceContainer(),
                sidecarsConfig.getInitContainer(),
                sidecarsConfig.getUtilsContainerImage(),
                sidecarsConfig.getOtelContainer(),
                sidecarsConfig.getNicllsContainer(),
                sidecarsConfig.getEssAgentContainer(),
                sidecarsConfig.getOtelCollectorContainer()
        );
        return new GoldenImagesResponse(sidecars);
    }


    @DeleteMapping("/v2/nvcf/instances/{instanceId}")
    @Operation(
            summary = "Delete Instance",
            description = """
                    Deletes the specified instance. Requires bearer token with
                     'admin:delete_function' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('admin:delete_function')")
    public void deleteInstance(@PathVariable String instanceId) {
        icmsClient.deleteInstances(Collections.singletonList(instanceId));
    }

    @PutMapping(
            value = "/v2/nvcf/accounts/{ncaId}/rolloverWorkers/functions/{functionId}"
                    + "/versions/{functionVersionId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Rollover Workers",
            description = """
                    Rolls over existing workers by spawning new workers for the specified function.
                     Requires bearer token with 'admin:deploy_function' scope in the HTTP
                     Authorization header.
                    """
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public RolloverWorkersResponse rolloverWorkers(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version id", required = true)
            @PathVariable UUID functionVersionId,
            @Valid @RequestBody(required = false) RolloverRequest rolloverRequest) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, functionVersionId);
        var deploymentContext = functionDeploymentLookupService
                .getDeploymentContextByVersionIdOrThrow(functionVersionId);
        var deployment = deploymentContext.deployment();
        var gpuSpecs = deploymentContext.gpuSpecs();
        var gpuSpecsMap = gpuSpecs.stream()
                .collect(Collectors.toMap(entity -> entity.getKey().getGpuSpecificationId(),
                                          e -> e));

        var rolloverSpecsMap = validateRolloverRequest(rolloverRequest, deployment, gpuSpecsMap);
        var rolloverContexts = gpuSpecs
                .stream()
                .map(spec -> getRolloverContext(function,
                                                deployment,
                                                spec,
                                                rolloverSpecsMap))
                .toList();

        var icmsRequestIds = rolloverContexts.stream()
                .map(rolloverContext -> {
                    // If the request includes rollover spec for the GPU/instance-type combo,
                    // then use the num instances from it. Otherwise, use max instances from the
                    // deployment spec like before.
                    var numInstances = Optional.ofNullable(rolloverContext.rolloverSpec())
                            .map(RolloverSpecificationDto::numInstances)
                            .orElse(rolloverContext.gpuDeploymentSpec().getMaxInstances());
                    return icmsAllocatorService.scheduleNewInstance(
                                            rolloverContext.function(),
                                            deployment.getDeploymentId(),
                                            rolloverContext.gpuDeploymentSpec(),
                                            numInstances);
                }).toList();
        return new RolloverWorkersResponse(icmsRequestIds);
    }

    @GetMapping("/v2/nvcf/accounts/{ncaId}/usage/gpus")
    @Operation(
            summary = "GPU Max Usage",
            description = """
                    Provides the current max usage of GPUs in the specified NVIDIA Cloud Account
                     based on currently deployed functions. Requires a bearer token with
                     'admin:deploy_function' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public ListGpuUsageResponse getGpuUsage(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);

        var gpuClusters = toGpuClusterMapping(icmsClient.getClustersByNcaId(ncaId));

        var activeOrDeployingFuncIds = functionLookupService
                .lookupEntitiesUsingAccountId(ncaId)
                .filter(func -> ACTIVE_OR_DEPLOYING.contains(func.getFunctionStatus()))
                .map(FunctionEntity::getFunctionId)
                .collect(Collectors.toSet());

        var gpuToSpecs = functionDeploymentLookupService.getFunctionDeploymentContextByNcaId(ncaId)
                .filter(deploymentContext -> activeOrDeployingFuncIds.contains(
                        deploymentContext.deployment().getFunctionId()))
                .map(deploymentContext -> deploymentContext.gpuSpecs())
                .flatMap(List::stream)
                .collect(Collectors.groupingBy(
                        spec -> spec.getGpu() + "/" + spec.getInstanceType()));

        // Same gpu could be used in different deployments targeted to a different clusters.
        // Max usage for the same gpu may vary from cluster to cluster.
        // TotalMaxInstances is not sum for all clusters, but sum for all deployments.

        // max usage per cluster
        var gpuToClusterMax = new HashMap<String, Integer>();
        // min usage per cluster
        var gpuToClusterMin = new HashMap<String, Integer>();
        var gpuToClustersFiltered = new HashMap<String, Set<ClusterResponse>>();
        gpuToSpecs.forEach((instanceKey, specs) -> {
            var clusterResponses =
                    gpuClusters.computeIfAbsent(instanceKey, k -> new HashSet<>());

            // For each gpu/instance type we may have multiple gpu-specs and multiple
            // available clusters. For each spec, we find all available clusters
            // and increase cluster level current maxInstance by spec.maxInstance.
            specs.forEach(
                    spec -> {
                        var clusterResponseStream = clusterResponses.stream();
                        if (StringUtils.isNotBlank(spec.getBackend())) {
                            clusterResponseStream = clusterResponseStream.filter(
                                    cluster -> spec.getBackend()
                                            .equalsIgnoreCase(
                                                    convertGfnClusterGroupName(
                                                            cluster.getClusterGroupName())));
                        }

                        var clusters = filterBlankStrings(spec.getClusters());
                        if (!CollectionUtils.isEmpty(clusters)) {
                            clusterResponseStream = clusterResponseStream.filter(
                                    cluster -> clusters.contains(cluster.getClusterName()));
                        }

                        var regions = filterBlankStrings(spec.getRegions());
                        if (!CollectionUtils.isEmpty(regions)) {
                            clusterResponseStream = clusterResponseStream.filter(
                                    cluster -> regions.contains(cluster.getRegion()));
                        }

                        clusterResponseStream.forEach(cluster -> {
                            var clusterKey = instanceKey + "/" + cluster.getClusterId();
                            var currentMax = gpuToClusterMax.computeIfAbsent(clusterKey, k -> 0);
                            gpuToClusterMax.put(clusterKey, currentMax + spec.getMaxInstances());

                            var currentMin = gpuToClusterMin.computeIfAbsent(clusterKey, k -> 0);
                            gpuToClusterMin.put(clusterKey, currentMin + spec.getMinInstances());

                            gpuToClustersFiltered
                                    .computeIfAbsent(instanceKey, k -> new HashSet<>())
                                    .add(cluster);
                        });
                    });
        });

        // Build resulting dto
        var gpus = gpuToSpecs.keySet().stream().map(instanceKey -> {
            var specs = gpuToSpecs.get(instanceKey);
            var aggregatedMaxInstances = specs.stream().mapToInt(
                    GpuSpecificationEntity::getMaxInstances).sum();
            var aggregatedMinInstances = specs.stream().mapToInt(
                    GpuSpecificationEntity::getMinInstances).sum();
            var clusters = gpuToClustersFiltered.computeIfAbsent(instanceKey,
                                                                 k -> Collections.emptySet());
            var placements = new ArrayList<GpuPlacementDto>();
            for (ClusterResponse cluster : clusters) {
                var clusterKey = instanceKey + "/" + cluster.getClusterId();
                if (gpuToClusterMax.containsKey(clusterKey)) {
                    placements.add(
                            toGpuPlacementDto(cluster, gpuToClusterMax.get(clusterKey),
                                              gpuToClusterMin.computeIfAbsent(clusterKey, k -> 0)));
                }
            }
            return GpuUsageDto.builder()
                    .gpu(specs.getFirst().getGpu())
                    .instanceType(specs.getFirst().getInstanceType())
                    .currentMaxUsage(aggregatedMaxInstances)
                    .currentMinUsage(aggregatedMinInstances)
                    .placements(placements)
                    .build();
        }).toList();

        return new ListGpuUsageResponse(gpus);
    }

    private static Map<String, RolloverSpecificationDto> validateRolloverRequest(
            RolloverRequest rolloverRequest,
            FunctionDeploymentEntity deployment,
            Map<UUID, GpuSpecificationEntity> gpuSpecs) {
        var rolloverSpecsMap = getRolloverSpecsMap(rolloverRequest);
        if (!CollectionUtils.isEmpty(rolloverSpecsMap)) {
            var deploymentSpecsMap = gpuSpecs.values().stream()
                    .collect(Collectors.toMap(spec -> "%s:%s".formatted(spec.getGpu(),
                                                                        spec.getInstanceType()),
                                              Function.identity()));
            rolloverSpecsMap.forEach((key, dto) -> {
                var functionId = deployment.getFunctionId();
                var versionId = deployment.getKey().getFunctionVersionId();
                var gpu = dto.gpu();
                var instanceType = dto.instanceType();

                if (!deploymentSpecsMap.containsKey(key)) {
                    var mesg = MESG_INVALID_ROLLOVER_SPEC
                            .formatted(functionId, versionId, gpu, instanceType);
                    log.error(mesg);
                    throw new BadRequestException(mesg);
                }

                var gpuDeploymentSpec = deploymentSpecsMap.get(key);
                var maxInstances = gpuDeploymentSpec.getMaxInstances();
                if (dto.numInstances() > maxInstances) {
                    var mesg = MESG_INVALID_NUM_INSTANCES
                            .formatted(functionId, versionId, dto.numInstances(),
                                       gpu, instanceType, maxInstances);
                    log.error(mesg);
                    throw new BadRequestException(mesg);
                }
            });
        }
        return rolloverSpecsMap;
    }

    private static Map<String, RolloverSpecificationDto> getRolloverSpecsMap(
            RolloverRequest rolloverRequest) {
        return Optional.ofNullable(rolloverRequest)
                .map(request -> request.rollOverSpecifications().stream()
                        .collect(Collectors.toMap(dto -> "%s:%s".formatted(dto.gpu(),
                                                                           dto.instanceType()),
                                                  Function.identity())))
                .orElseGet(Map::of);
    }

    private static RolloverContext getRolloverContext(
            FunctionEntity function,
            FunctionDeploymentEntity deployment,
            GpuSpecificationEntity gpuDeploymentSpec,
            Map<String, RolloverSpecificationDto> rolloverSpecsMap) {
        var gpu = gpuDeploymentSpec.getGpu();
        var instanceType = gpuDeploymentSpec.getInstanceType();
        var key = "%s:%s".formatted(gpu, instanceType);
        var rolloverSpec = rolloverSpecsMap.get(key);
        return new RolloverContext(function, deployment, gpuDeploymentSpec, rolloverSpec);
    }

    /**
     * Converts Clusters response to a mapping:
     * key: [gpu/instanceType]
     * value: A Unique set of all clusters where this gpu/instance type is available for ncaId
     * <p>
     * Filter out all clusters with status != READY
     *
     * @param clustersResponse ICMS /clusters response
     * @return mapping
     */
    private static Map<String, Set<ClusterResponse>> toGpuClusterMapping(
            List<ClusterResponse> clustersResponse) {
        Map<String, Set<ClusterResponse>> result = new HashMap<>();
        clustersResponse
                .stream()
                .filter(clusterResponse ->
                                "READY".equalsIgnoreCase(clusterResponse.getStatus()))
                .forEach(clusterResponse ->
                                 clusterResponse.getGpus().forEach(gpu -> {
                                     var gpuName = gpu.getName();
                                     gpu.getInstanceTypes().forEach(instanceType -> {
                                         var instanceTypeName = instanceType.getName();
                                         var key = gpuName + "/" + instanceTypeName;
                                         Set<ClusterResponse> setOfLocations =
                                                 result.computeIfAbsent(key, k -> new HashSet<>());
                                         setOfLocations.add(clusterResponse);
                                     });

                                 }));
        return result;
    }

    private static GpuPlacementDto toGpuPlacementDto(
            ClusterResponse cluster, Integer currentMaxUsage, Integer currentMinUsage) {
        return GpuPlacementDto.builder()
                .clusterId(cluster.getClusterId())
                .cluster(cluster.getClusterName())
                .clusterGroupId(cluster.getClusterGroupId())
                .clusterGroup(cluster.getClusterGroupName())
                .cloudProvider(cluster.getCloudProvider())
                .region(cluster.getRegion())
                .currentMaxUsage(currentMaxUsage)
                .currentMinUsage(currentMinUsage)
                .build();
    }

    // Due to different flows at ICMS for NVCA and GFN, in /cluster response GFN cluster group
    // name is always GFN_REGION_TARGETING. We need to convert it to expected backend name GFN.
    private static String convertGfnClusterGroupName(String clusterGroupName) {
        if ("GFN_REGION_TARGETING".equalsIgnoreCase(clusterGroupName)) {
            return "GFN";
        }
        return clusterGroupName;
    }
}
