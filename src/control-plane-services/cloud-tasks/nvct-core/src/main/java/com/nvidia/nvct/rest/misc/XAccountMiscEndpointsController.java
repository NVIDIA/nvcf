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
package com.nvidia.nvct.rest.misc;

import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.misc.dto.GpuPlacementDto;
import com.nvidia.nvct.rest.misc.dto.GpuUsageDto;
import com.nvidia.nvct.rest.misc.dto.ListGpuUsageResponse;
import com.nvidia.nvct.rest.task.dto.InstanceUsageTypeEnum;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.icms.IcmsClient;
import com.nvidia.nvct.service.icms.IcmsStubService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.tags.Tag;
import java.util.ArrayList;
import java.util.Collections;
import java.util.EnumSet;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.util.CollectionUtils;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
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

    private static final String NCA_ID_DESCRIPTION = "NVIDIA Cloud Account Id";
    private static final EnumSet<TaskStatus> QUEUED_OR_LAUNCHED_OR_RUNNING =
            EnumSet.of(TaskStatus.RUNNING, TaskStatus.QUEUED, TaskStatus.LAUNCHED);
    private final AccountService accountService;
    private final TaskService taskService;
    private final IcmsClient icmsClient;
    private final Tracer tracer;

    @GetMapping(value = "/v1/nvct/accounts/{ncaId}/usage/gpus")
    @Operation(
            summary = "GPU Max Usage",
            description = """
                    Provides the current max usage of GPUs in the specified NVIDIA Cloud Account
                     based on currently deployed tasks. Requires a bearer token with
                     'admin:launch_task' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAuthority('admin:launch_task')")
    public ListGpuUsageResponse getGpuUsage(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            Authentication authentication) {
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        var gpuToSpec = new HashMap<String, GpuSpecUdt>();
        var gpuToCount = new HashMap<String, Integer>();
        // usage per cluster
        var gpuToClusterCount = new HashMap<String, Integer>();
        // clusters where the gpu could be deployed
        var gpuToClustersFiltered = new HashMap<String, Set<IcmsStubService.ClusterResponse>>();

        var tasks = taskService.fetchTasksByAccount(ncaId)
                .filter(task -> QUEUED_OR_LAUNCHED_OR_RUNNING.contains(task.getStatus()))
                .collect(Collectors.toSet());
        var gpuToClusters = toGpuClusterMapping(
                icmsClient.getClusters(
                        ncaId, getInstanceTypeUsage(tasks)));

        tasks.stream()
                .map(TaskEntity::getGpuSpec)
                .forEach(spec -> {
                    var instanceKey = spec.getGpu() + "/" + spec.getInstanceType();
                    gpuToSpec.put(instanceKey, spec);
                    // we know there is only one instance per spec
                    gpuToCount.put(instanceKey, gpuToCount.getOrDefault(instanceKey, 0) + 1);

                    var clusterResponses =
                            gpuToClusters.computeIfAbsent(instanceKey, k -> new HashSet<>());

                    Stream<IcmsStubService.ClusterResponse> clusterResponseStream =
                            clusterResponses.stream();
                    if (StringUtils.isNotBlank(spec.getBackend())) {
                        clusterResponseStream = clusterResponseStream.filter(
                                cluster -> spec.getBackend()
                                        .equalsIgnoreCase(
                                                convertGfnClusterGroupName(
                                                        cluster.getClusterGroupName())));
                    }
                    if (!CollectionUtils.isEmpty(spec.getClusters())) {
                        clusterResponseStream = clusterResponseStream.filter(
                                cluster -> spec.getClusters()
                                        .contains(cluster.getClusterName()));
                    }
                    if (!CollectionUtils.isEmpty(spec.getRegions())) {
                        clusterResponseStream = clusterResponseStream.filter(
                                cluster -> spec.getRegions().contains(cluster.getRegion()));
                    }

                    clusterResponseStream.forEach(cluster -> {
                        var clusterKey = instanceKey + "/" + cluster.getClusterId();
                        gpuToClusterCount.put(clusterKey, gpuToClusterCount.getOrDefault(clusterKey, 0) + 1);
                        gpuToClustersFiltered
                                .computeIfAbsent(instanceKey, k -> new HashSet<>())
                                .add(cluster);
                    });
                });

        // Build resulting dto
        var gpus = new ArrayList<GpuUsageDto>();
        gpuToCount.keySet()
                .forEach(instanceKey -> {
                    var gpu = gpuToSpec.get(instanceKey).getGpu();
                    var instanceType = gpuToSpec.get(instanceKey).getInstanceType();
                    var specCount = gpuToCount.get(instanceKey);
                    var clusters = gpuToClustersFiltered.computeIfAbsent(
                            instanceKey, k -> Collections.emptySet());
                    var placements = new ArrayList<GpuPlacementDto>();
                    for (IcmsStubService.ClusterResponse cluster : clusters) {
                        var clusterKey = instanceKey + "/" + cluster.getClusterId();
                        if (gpuToClusterCount.containsKey(clusterKey)) {
                            placements.add(toGpuPlacementDto(
                                    cluster, gpuToClusterCount.get(clusterKey),
                                    gpuToClusterCount.get(clusterKey)));
                        }
                    }
                    gpus.add(GpuUsageDto.builder()
                                     .gpu(gpu)
                                     .instanceType(instanceType)
                                     .currentMaxUsage(specCount)
                                     .currentMinUsage(specCount)
                                     .placements(placements)
                                     .build());
                });

        return new ListGpuUsageResponse(gpus);
    }

    /**
     * Converts a ICMS Clusters response to a mapping:
     * key: [gpu/instanceType]
     * value: A Unique set of all clusters where this gpu/instance type is available for ncaId
     *
     * Filter out all clusters with status != READY
     * @param clustersResponse icms /clusters response
     * @return mapping
     */
    private static Map<String, Set<IcmsStubService.ClusterResponse>> toGpuClusterMapping(
            List<IcmsStubService.ClusterResponse> clustersResponse) {
        Map<String, Set<IcmsStubService.ClusterResponse>> result = new HashMap<>();
        clustersResponse
                .stream()
                .filter(clusterResponse ->
                                clusterResponse.getStatus().equalsIgnoreCase("READY"))
                .forEach(clusterResponse ->
                                 clusterResponse.getGpus().forEach(gpu -> {
                                     var gpuName = gpu.getName();
                                     gpu.getInstanceTypes().forEach(instanceType -> {
                                         var instanceTypeName = instanceType.getName();
                                         var key = gpuName + "/" + instanceTypeName;
                                         Set<IcmsStubService.ClusterResponse> setOfLocations =
                                                 result.computeIfAbsent(key, k -> new HashSet<>());
                                         setOfLocations.add(clusterResponse);
                                     });

                                 }));
        return result;
    }

    private static GpuPlacementDto toGpuPlacementDto(
            IcmsStubService.ClusterResponse cluster, Integer currentMaxUsage,
            Integer currentMinUsage) {
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

    // Determines if all the Tasks in the list are either strictly container-based or whether
    // some are helm-based and some are container-based.
    private static InstanceUsageTypeEnum getInstanceTypeUsage(
            Set<TaskEntity> entities) {
        if (entities.stream()
                .noneMatch(entity -> StringUtils.isBlank(entity.getContainerImage()))) {
            return InstanceUsageTypeEnum.CONTAINER;
        }
        return InstanceUsageTypeEnum.DEFAULT;
    }
}
