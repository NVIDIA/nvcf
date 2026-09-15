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
package com.nvidia.icms.service.createInstances;

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.service.account.GpuUsageFilter;
import com.nvidia.icms.service.byoc.ByocValidationService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import jakarta.validation.constraints.NotNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static com.nvidia.icms.inbound.rest.model.CloudProvider.getCloudProviderFromClusterProvider;
import static com.nvidia.icms.service.account.GpuUsageFilter.getNullOrSet;

/**
 * Provides BYOC/NVCA-specific destination logic for instance creation.
 *
 * <p>Responsibilities:
 * <ul>
 *   <li>Validating whether a targeted cluster is allowed for a given request.</li>
 *   <li>Generating the list of {@link RequestInstanceDestination} objects for targeted clusters.</li>
 *   <li>Resolving BYOC/NVCA non-targeted destinations from authorized NCA accounts.</li>
 * </ul>
 */
@Service
@Slf4j
@RequiredArgsConstructor
public class ByocRequestDestinationProvider {

    private final ByocValidationService byocValidationService;
    private final NvcaClusterRepository nvcaClusterRepository;
    private final DestinationCreator destinationCreator;
    private final ComputePlatformService computePlatformService;

    /**
     * Returns {@code true} when the cluster passes region, cluster-name, attribute, and
     * provider-validation checks for the given filter.
     *
     * <p>Note: cloud-health filtering is intentionally omitted here — callers apply health and
     * capacity checks (SPOT/RESERVED) independently before invoking this method.
     */
    public boolean isClusterAllowed(
            @NotNull ClusterByGroupIdAndIdEntity cluster,
            @NotNull GpuUsageFilter filter) {

        if (!filter.isRegionNameAllowed(cluster.getRegion())) {
            return false; // skip clusters from other regions
        }

        if (!filter.isClusterNameAllowed(cluster.getClusterName())) {
            return false; // skip clusters not in the requested list
        }

        Set<String> allAttributes = new HashSet<>();
        if (!CollectionUtils.isEmpty(cluster.getAttributes())) {
            allAttributes.addAll(cluster.getAttributes());
        }
        if (!CollectionUtils.isEmpty(cluster.getCustomAttributes())) {
            allAttributes.addAll(cluster.getCustomAttributes());
        }

        if (!filter.areAttributesAllowed(allAttributes)) {
            return false; // skip clusters that do not have all requested attributes
        }

        return byocValidationService.validateClustersStatusAndGetProviderForNvca(
                cluster.getKey().getClusterId()) != null;
    }

    /**
     * Converts the provided set of ready clusters into {@link RequestInstanceDestination} objects,
     * one per GPU / instance-type combination that satisfies {@code filter}.
     */
    public @NotNull Set<RequestInstanceDestination> generateTargetedDestinationList(
            @NotNull Set<ClusterByGroupIdAndIdEntity> clusters,
            @NotNull GpuUsageFilter filter,
            @NotNull SpotInstanceRequestSchema instanceRequest) {

        Set<RequestInstanceDestination> result = new HashSet<>();

        for (ClusterByGroupIdAndIdEntity entity : clusters) {
            Map<String, String> queueUrlByGpuNameMap = new HashMap<>();
            if (entity.getGpusV5() == null) {
                continue;
            }
            for (GpuV5Udt gpu : entity.getGpusV5()) {
                if (filter.isGpuNameAllowed(gpu.getName())) {
                    // See getNonTargetedDestinations for rationale — "" means NATS mode, not missing.
                    String queueUrl = byocValidationService.getCreationQueueForReadyCluster(
                            instanceRequest, entity, gpu.getName());
                    queueUrlByGpuNameMap.put(gpu.getName(), queueUrl == null ? "" : queueUrl);
                }
            }

            CloudProvider cloudProvider = getCloudProviderFromClusterProvider(entity.getClusterProvider());
            if (cloudProvider == null) {
                log.warn("{}: Unable to determine cloud provider for cluster {}",
                         instanceRequest.getLoggingId(), entity.getKey().getClusterId());
            }
            result.addAll(destinationCreator.addAvailableDestinations(
                    new DestinationClusterData(entity, queueUrlByGpuNameMap),
                    filter, cloudProvider, instanceRequest.getInstanceCount()));
        }

        return result;
    }

    /**
     * Returns all BYOC/NVCA destinations available for the given non-targeted instance request.
     * Non-BYOC clusters are excluded from the result.
     */
    public @NotNull Set<RequestInstanceDestination> getNonTargetedDestinations(
            @NotNull SpotInstanceRequestSchema instanceRequest) {

        String clusterGroupName = instanceRequest.getBackend();

        GpuUsageFilter filter = GpuUsageFilter.builder()
                .clusterGroupNames(getNullOrSet(clusterGroupName))
                .gpuNames(getNullOrSet(instanceRequest.getGpu()))
                .instanceTypes(getNullOrSet(instanceRequest.getInstanceType()))
                .build();

        // Fetch clusters for the NCA ID and merge with WILDCARD, excluding non-BYOC clusters
        List<ClustersByAuthorizedAccountsEntity> clustersForNcaId =
                new ArrayList<>(nvcaClusterRepository.getAllClustersInAuthorizedAccount(instanceRequest.getNcaId()));
        List<ClustersByAuthorizedAccountsEntity> availableClusters = new ArrayList<>(clustersForNcaId);
        availableClusters.addAll(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD));
        availableClusters = availableClusters.stream()
                .filter(entity -> !computePlatformService.isPlatformCluster(entity.getClusterGroupName()))
                .toList();

        Set<RequestInstanceDestination> result = new HashSet<>();
        for (ClustersByAuthorizedAccountsEntity entity : availableClusters) {
            Map<String, String> queueUrlByGpuNameMap = new HashMap<>();
            if (entity.getGpusV5() == null) {
                continue;
            }

            for (GpuV5Udt gpu : entity.getGpusV5()) {
                if (filter.isGpuNameAllowed(gpu.getName())) {
                    // Always put the URL (even "") so DestinationCreator can distinguish
                    // "GPU filtered out" (absent key) from "NATS mode, no SQS queue" (empty
                    // value). getByocCreationQueueUrlForGpu throws PreConditionFailedException
                    // for genuine SQS misconfiguration, so reaching this point means the
                    // value is either a real queue URL or "" for NATS mode.
                    String queueUrl = byocValidationService.getByocCreationQueueUrlForGpu(entity, gpu.getName());
                    queueUrlByGpuNameMap.put(gpu.getName(), queueUrl == null ? "" : queueUrl);
                }
            }

            CloudProvider cloudProvider =
                    byocValidationService.validateClustersStatusAndGetProviderForNvca(entity.getKey().getClusterId());
            if (cloudProvider == null) {
                log.warn("ByocRequestDestinationProvider: Unable to determine cloud provider for cluster {},"
                                 + " cluster is probably unhealthy",
                         entity.getKey().getClusterId());
                continue;
            }

            result.addAll(destinationCreator.addAvailableDestinations(
                    new DestinationClusterData(entity, queueUrlByGpuNameMap),
                    filter, cloudProvider, instanceRequest.getInstanceCount()));
        }

        return result;
    }
}
