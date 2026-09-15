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
package com.nvidia.icms.service.byoc;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroupResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.ClustersResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.GpuResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.InstanceTypeResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.ClusterAuthorizationService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.util.GsonCompatMapper;
import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

import jakarta.annotation.Nullable;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.TreeMap;
import java.util.stream.Collectors;

import static com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum.toNodeTypeEnum;
import static com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository.WILDCARD;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.getNodeTypeEnum;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;

@Service
@AllArgsConstructor
@Slf4j
public class ClustersService {

    private final ClusterRepository clusterRepository;

    private final NvcaClusterRepository nvcaClusterRepository;

    private final NvcaConfigurationProperties nvcaConfigurationProperties;

    private final InstanceServiceHelper instanceServiceHelper;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final ClusterTargetingHelper clusterTargetingHelper;

    private final ComputePlatformService computePlatformService;

    private final ClusterAuthorizationService clusterAuthorizationService;

    /**
     * This is a static class to store READY cluster information per GPU from a cluster
     */
    // TODO: Move this class and related functions to ClusterGpuInfoService
    // TODO: Rename this class to ClusterGpuInfo as it stores GPU info per cluster
    @Builder
    @Data
    public static class ReadyClusterInfo {

        String clusterId;
        String clusterName;
        String name;
        String k8sVersion;
        String gpu;
        Set<InstanceTypeV5Udt> instanceTypes;
        /*
         In a cluster any "gpu" can have multiple "instanceTypes" and each instance type will have its "nodeType"
         This field will store union of all "nodeType" supported by that "gpu" in the cluster
         */
        Set<NodeTypeEnum> supportedNodeTypes;
        ClusterStatusEnum clusterStatus;
        String clusterGroupId;
        String clusterGroupName;
        String primaryNcaId;
        Set<String> authorizedNcaIds;
        String region;
        Set<String> attributes;
        ClusterProviderEnum clusterProvider;
    }

    @Observed
    public ClusterGroupResponse getRegisteredClustersForNcaId(String ncaId,
                                                              @NonNull InstanceTypeUsageEnum instanceTypeUsageEnum) {

        Set<ClusterGroups> clusterGroupsSet = new HashSet<>();

        clusterGroupsSet.addAll(fetchClusterGroupsForNvca(ncaId, instanceTypeUsageEnum));

        if (clusterAuthorizationService.isDetailedTargetingFlowEnabled()) {
            // NEW: non-BYOC with authorization filtering using hardcoded cluster group ID.
            ClusterGroups nonByocClusterGroup = clusterAuthorizationService
                    .fetchAuthorizedClusterGroup(ncaId, instanceTypeUsageEnum);
            if (nonByocClusterGroup != null) {
                clusterGroupsSet.add(nonByocClusterGroup);
            }
        } else {
            log.info("Using BART flow to fetch clusterGroup details for non-BYOC, ncaId: {}", ncaId);
            // OLD: non-BYOC from BART tables
            clusterGroupsSet.addAll(fetchClusterGroupsForBart(ncaId));
        }

        return ClusterGroupResponse.builder()
                .clusterGroup(clusterGroupsSet)
                .build();
    }

    /**
     *  All BYOC clusters are migrated to NVCA flow
     *  Only non-BYOC manually registered clusters are present with BART flow and added by this function in response
     *  For non-BYOC manually registered clusters all the instance types will have nodeType as "SINGLE"
     *  We won't need to do instanceTypeUsage filtering for non-BYOC manually registered clusters as SINGLE nodeType will always be included in response
     */
    private Set<ClusterGroups> fetchClusterGroupsForBart(String ncaId) {
        // Adding cluster groups from WILDCARD(*)
        Set<ClusterGroups> clusterGroupsSet = getClusterGroupsForBart(WILDCARD);

        // Fetching info of all the clusters available for given ncaId
        if (ncaId != null && !ncaId.equals(WILDCARD)) {
            clusterGroupsSet.addAll(getClusterGroupsForBart(ncaId));
        }

        filterRestrictedGpusForComputePlatformGroups(clusterGroupsSet, ncaId);
        return clusterGroupsSet;
    }

    /**
     * Restricts limited GPUs to dedicated NGC orgs on compute-platform cluster
     * groups. No-op when {@code icms.gpu-allowed-nca-ids} is empty or the GPU is not listed
     * (existing behavior); BYOC groups are never gated.
     */
    private void filterRestrictedGpusForComputePlatformGroups(Set<ClusterGroups> clusterGroupsSet,
                                                              String ncaId) {
        clusterGroupsSet.removeIf(clusterGroup -> {
            if (!computePlatformService.isPlatformCluster(clusterGroup.getName())) {
                return false;
            }
            if (clusterGroup.getGpus() != null) {
                clusterGroup.getGpus().removeIf(gpu ->
                        !icmsConfigurationProperties.isNcaAllowedForGpu(gpu.getName(), ncaId));
            }
            return clusterGroup.getGpus() == null || clusterGroup.getGpus().isEmpty();
        });
    }

    private Set<ClusterGroups> fetchClusterGroupsForNvca(String ncaId,
                                                         @NonNull InstanceTypeUsageEnum instanceTypeUsageEnum) {
        // Adding cluster groups from WILDCARD(*)
        Map<String, ClusterGroups> groupIdToClusterGroupRespMap = new HashMap<>();

        getClusterGroupsForNvca(WILDCARD, groupIdToClusterGroupRespMap, instanceTypeUsageEnum);

        // Fetching info of all the clusters available for given ncaId
        // TODO: To reduce API response time cache response for WILDCARD
        if (ncaId != null && !ncaId.equals(WILDCARD)) {
            getClusterGroupsForNvca(ncaId, groupIdToClusterGroupRespMap, instanceTypeUsageEnum);
        }

        return new HashSet<>(groupIdToClusterGroupRespMap.values());
    }

    private Set<ClusterGroups> getClusterGroupsForBart(String ncaId) {

        Set<ClusterGroups> clusterGroupsSet = new HashSet<>();

        List<ClusterGroupsByAuthorizedAccountsEntity>
                clusterGroupsByAuthorizedAccountsEntities =
                clusterRepository.getAllClusterGroupsInAuthorizedAccount(ncaId);

        for (ClusterGroupsByAuthorizedAccountsEntity entity : clusterGroupsByAuthorizedAccountsEntities) {
            Set<ClustersResponse> clustersResponses =
                    getClustersFromClusterGroupForBart(entity.getKey().getClusterGroupId())
                            .stream()
                            .map(ClustersService::toClustersResponse)
                            .collect(Collectors.toSet());

            // Don't include cluster group if no cluster is Ready
            if (clustersResponses.isEmpty()) {
                continue;
            }
            ClusterGroups clusterGroups = ClusterGroups.builder()
                    .id(entity.getKey().getClusterGroupId())
                    .name(entity.getKey().getClusterGroupName())
                    .gpus(toGpuResponse(entity.getGpus()))
                    .authorizedNcaIds(entity.getAuthorizedNcaIds())
                    .ncaId(entity.getNcaId())
                    .clusters(clustersResponses)
                    .build();
            clusterGroupsSet.add(clusterGroups);
        }

        return clusterGroupsSet;
    }

    public Set<ClusterByGroupIdAndIdEntity> getClustersFromClusterGroupForBart(
            String clusterGroupId) {
        Set<ClusterByGroupIdAndIdEntity> entities = new HashSet<>();

        List<ClusterByGroupIdAndIdEntity>
                clustersFromDb = clusterRepository.getClustersFromClusterGroup(clusterGroupId);

        for (ClusterByGroupIdAndIdEntity entity : clustersFromDb) {
            // Don't include cluster either if it is not Ready or it is not Healthy
            if (entity.getClusterStatus() != ClusterStatusEnum.READY) {
                log.info(
                        "Cluster with id {} name {} from cluster group id {} name {} is not READY hence avoiding populating in response for BART",
                        entity.getKey().getClusterId(), entity.getClusterName(),
                        entity.getClusterGroupName(), entity.getKey().getClusterGroupId());
                continue;
            }

            // Don't include NVCA migrated clusters
            if (entity.getNvcaVersion() != null) {
                log.info(
                        "Cluster with id {} name {} from cluster group id {} name {} is migrated to NVCA hence avoiding populating in response for BART",
                        entity.getKey().getClusterId(), entity.getClusterName(),
                        entity.getClusterGroupName(), entity.getKey().getClusterGroupId());
                continue;
            }

            entities.add(entity);
        }

        return entities;
    }

    private void getClusterGroupsForNvca(
            String ncaId,
            Map<String, ClusterGroups> groupIdToClusterGroupRespMap,
            @NonNull InstanceTypeUsageEnum instanceTypeUsageEnum) {

        /*
         1. Fetch allClusters available for given ncaId, Skip non-BYOC TARGETED clusters
         2. Fetch all READY clusters for given clusterGroupId
         3. Validate clusterHealthStatus if not READY then ignore
         4. Find allowed GPUs for given ncaId
            a) if same GPU is present in another cluster then ncaId must be part of allowedNcaIds (authorizedNcaId + primary ncaId)
         5. Filter allowed GPUs against instanceTypeUsage filter
         6. Take intersection of authorizedNcaId for all cluster in that cluster group
         7. Generate clusterGroupResponse
            a) If clusterGroup response present then add to existing response
            b) If clusterGroup response not present then create new response
         */

        // 1. Fetch allClusters available for given ncaId
        List<ClustersByAuthorizedAccountsEntity> clustersInAuthorizedAccount = nvcaClusterRepository.getAllClustersInAuthorizedAccount(
                ncaId);

        // Replacing WILDCARD with * for comparison
        ncaId = getNcaIdForWildCard(ncaId);

        Map<String, Set<ReadyClusterInfo>> groupIdToReadyClustersInfoCache = new HashMap<>();

        for (ClustersByAuthorizedAccountsEntity entity : clustersInAuthorizedAccount) {
            // If cluster belongs to a first-party compute platform cluster group then skip it
            if (computePlatformService.isPlatformCluster(entity.getClusterGroupName())) {
                continue;
            }
            // 2. Fetch all READY clusters for given clusterGroupId
            fetchReadyClustersInGroupForNVCA(entity.getClusterGroupId(),
                                             groupIdToReadyClustersInfoCache);
            Set<ReadyClusterInfo> readyClusterSet = groupIdToReadyClustersInfoCache.get(
                    entity.getClusterGroupId());
            ReadyClusterInfo readyClusterInfo = getClusterInfo(entity.getKey().getClusterId(),
                                                               readyClusterSet);

            // 3. Validate clusterHealthStatus if not READY then ignore
            if (readyClusterInfo == null) {
                continue;
            }

            // 4. Find allowed GPUs for given ncaId
            Set<GpuV5Udt> allowedGpusFromEntityForNcaId = getAllowedGpus(
                    NvcaConverter.getGpusV5(entity), ncaId, readyClusterSet);

            // 5. Filter allowed GPUs against instanceTypeUsage filter
            Set<GpuResponse> filteredGpuResposneSet = applyInstanceTypeUsageFilter(
                    allowedGpusFromEntityForNcaId, instanceTypeUsageEnum);

            // If no gpu available after applying instanceTypeUsage filter then ignoring that cluster
            if (filteredGpuResposneSet.isEmpty()) {
                log.info(
                        "cluster with id {} don't have any GPUs for instanceTypeUsageFilter {}, ignoring cluster from response",
                        entity.getKey().getClusterId(), instanceTypeUsageEnum);

                continue;
            }

            // 6. Take intersection of authorizedNcaId for all cluster in that cluster group
            Set<String> intersectedAuthorizedNcaIds = getIntersectionOfAuthorizedNcaIds(
                    entity.getAuthorizedNcaIds(), readyClusterSet);

            // 7. a) If clusterGroup response present then add to existing response
            if (groupIdToClusterGroupRespMap.containsKey(entity.getClusterGroupId())) {

                addToExistingClusterGroupResponse(groupIdToClusterGroupRespMap, readyClusterInfo,
                                                  filteredGpuResposneSet,
                                                  intersectedAuthorizedNcaIds);
            } else {

                // 7. b) If clusterGroup response not present then create new response
                ClusterGroups clusterGroups =
                        getNewClusterGroupResponse(readyClusterInfo, filteredGpuResposneSet,
                                                   intersectedAuthorizedNcaIds);
                groupIdToClusterGroupRespMap.put(entity.getClusterGroupId(), clusterGroups);
            }
        }
    }

    /**
     * Returns common authorizedNcaId from all clusters in that cluster group
     *
     * @param authorizedNcaIdSet Authorized ncaId from entity to validate
     * @param readyClusterSet    Ready cluster set from same cluster group
     * @return intersection of authorizedNcaIds
     */
    private Set<String> getIntersectionOfAuthorizedNcaIds(
            Set<String> authorizedNcaIdSet,
            Set<ReadyClusterInfo> readyClusterSet) {
        Set<String> commonAuthorizedNcaIds = new HashSet<>();
        for (String authorizedNcaId : authorizedNcaIdSet) {
            if (isAuthorizedNcaIdPresentInClusters(authorizedNcaId, readyClusterSet)) {
                commonAuthorizedNcaIds.add(authorizedNcaId);
            }
        }
        return commonAuthorizedNcaIds;
    }

    private boolean isAuthorizedNcaIdPresentInClusters(
            String authorizedNcaId,
            Set<ReadyClusterInfo> readyClusterSet) {
        for (ReadyClusterInfo readyClusterInfo : readyClusterSet) {
            if (!readyClusterInfo.getAuthorizedNcaIds().contains(authorizedNcaId)) {

                log.info(
                        "For {} clusterGroupId and {} clusterGroupName: {} authorizedNcaId not present in {} authorizedNcaId of cluster with {} id and {} name",
                        readyClusterInfo.getClusterGroupId(),
                        readyClusterInfo.getClusterGroupName(),
                        authorizedNcaId, readyClusterInfo.getAuthorizedNcaIds(),
                        readyClusterInfo.getClusterId(), readyClusterInfo.getClusterName());

                return false;
            }
        }
        return true;
    }

    private void addToExistingClusterGroupResponse(
            Map<String, ClusterGroups> groupIdToClusterGroupRespMap,
            ReadyClusterInfo readyClusterInfo,
            Set<GpuResponse> filteredGpuResposneSet, Set<String> intersectedAuthorizedNcaIds) {
        ClusterGroups clusterGroups =
                groupIdToClusterGroupRespMap.get(readyClusterInfo.getClusterGroupId());

        // Adding to same cluster group

        addToGpuResponse(clusterGroups.getGpus(), filteredGpuResposneSet,
                         readyClusterInfo.getClusterGroupId(),
                         readyClusterInfo.getClusterGroupName());

        clusterGroups.getClusters().add(toClustersResponse(readyClusterInfo));

        clusterGroups.getAuthorizedNcaIds().addAll(intersectedAuthorizedNcaIds);
    }

    private ClusterGroups getNewClusterGroupResponse(
            ReadyClusterInfo readyClusterInfo,
            Set<GpuResponse> filteredGpuResposneSet,
            Set<String> intersectedAuthorizedNcaIds) {
        return ClusterGroups.builder()
                .id(readyClusterInfo.getClusterGroupId())
                .name(readyClusterInfo.getClusterGroupName())
                .gpus(new HashSet<>(filteredGpuResposneSet))
                .clusters(new HashSet<>(Set.of(toClustersResponse(readyClusterInfo))))
                .ncaId(readyClusterInfo.getPrimaryNcaId())
                .authorizedNcaIds(new HashSet<>(intersectedAuthorizedNcaIds))
                .build();
    }

    /**
     * @param clusterId        id of the cluster
     * @param readyClustersSet ready cluster set
     * @return ReadyClusterInfo if the cluster is READY else returns Null
     */
    @Nullable
    public ReadyClusterInfo getClusterInfo(
            String clusterId, Set<ReadyClusterInfo> readyClustersSet) {
        for (ReadyClusterInfo readyClusterInfo : readyClustersSet) {
            if (readyClusterInfo.getClusterId().equals(clusterId)) {
                return readyClusterInfo;
            }
        }
        return null;
    }

    /**
     * @param gpuV5Set        Set of GpuV5Udt
     * @param ncaId           ncaId from request
     * @param readyClusterSet Set of READY clusters
     * @return allowed GPUs from entity for given ncaId
     */
    private Set<GpuV5Udt> getAllowedGpus(
            Set<GpuV5Udt> gpuV5Set,
            String ncaId, Set<ReadyClusterInfo> readyClusterSet) {
        Set<GpuV5Udt> allowedGpus = new HashSet<>();
        for (GpuV5Udt gpuV5 : gpuV5Set) {
            if (isGpuAllowedForNcaId(ncaId, gpuV5.getName(), readyClusterSet)) {
                allowedGpus.add(gpuV5);
            }
        }

        return allowedGpus;
    }

    /**
     * If gpuName is present in another READY cluster then given ncaId must be part of allowedNcaId (authorizedNcaId + primary ncaId)
     *
     * @param ncaId             given ncaId
     * @param gpuName           gpuName to validate
     * @param readyClustersInfo ready clusters present in cluster group
     * @return true if gpuName is allowed for given ncaId else return false
     */
    private boolean isGpuAllowedForNcaId(
            String ncaId, String gpuName,
            Set<ReadyClusterInfo> readyClustersInfo) {
        for (ReadyClusterInfo readyClusterInfo : readyClustersInfo) {

            // if gpuName is same then ncaId must be part of allowedNcaIds(authorizedNcaId + primary ncaId) for that cluster
            Set<String> allowedNcaIds = new HashSet<>(readyClusterInfo.getAuthorizedNcaIds());
            allowedNcaIds.add(readyClusterInfo.getPrimaryNcaId());

            if (readyClusterInfo.getGpu().equals(gpuName) && !allowedNcaIds.contains(ncaId)) {
                log.info(
                        "For {} clusterGroupId and {} clusterGroupName: {} GPU is also present in cluster with {} id and {} name but {} ncaId is not part of {} allowedNcaIds",
                        readyClusterInfo.getClusterGroupId(),
                        readyClusterInfo.getClusterGroupName(),
                        gpuName, readyClusterInfo.getClusterId(), readyClusterInfo.getClusterName(),
                        ncaId, allowedNcaIds);
                return false;
            }
        }
        return true;
    }

    /**
     * Fetches all READY clusters from given clusterGroupId
     *
     * @param groupId                         cluster group id
     * @param groupIdToReadyClustersInfoCache map to cache the response for multiple invocation
     */
    private void fetchReadyClustersInGroupForNVCA(
            String groupId, Map<String, Set<ReadyClusterInfo>> groupIdToReadyClustersInfoCache) {

        // Using cache to return already fetched clusters from clusterGroup
        groupIdToReadyClustersInfoCache.computeIfAbsent(groupId, key -> {

            // If cluster is READY then only it will be used for gpu and allowedNcaId validation
            // Checking if cluster meant for NVCA2.0 (gpuV5 must not null)
            return getAllReadyClusters(groupId)
                    .stream()
                    .flatMap(entity -> toReadyClusterInfo(entity).stream())
                    .collect(Collectors.toSet());
        });
    }

    public Set<ClusterByGroupIdAndIdEntity> getAllReadyClusters(String clusterGroupId) {
        return clusterRepository.getClustersFromClusterGroup(clusterGroupId)
                .stream()
                .filter(entity -> ClusterStatusEnum.READY.equals(entity.getClusterStatus())
                        && !isSetEmptyOrNull(NvcaConverter.getGpusV5(entity)))
                .collect(Collectors.toSet());
    }

    public static Set<ClustersService.ReadyClusterInfo> toReadyClusterInfo(
            Set<ClusterByGroupIdAndIdEntity> clusterByGroupIdAndIdEntities) {
        return clusterByGroupIdAndIdEntities
                .stream()
                .flatMap(entity -> toReadyClusterInfo(entity).stream())
                .collect(Collectors.toSet());
    }

    public static Set<ClustersService.ReadyClusterInfo> toReadyClusterInfo(
            ClusterByGroupIdAndIdEntity entity) {
        Set<ClustersService.ReadyClusterInfo> clusterInfo = new HashSet<>();
        for (GpuV5Udt gpuV5 : NvcaConverter.getGpusV5(entity)) {
            ReadyClusterInfo readyClusterInfo = toReadyClusterInfo(gpuV5, entity);
            clusterInfo.add(readyClusterInfo);
        }
        return clusterInfo;
    }

    public static ReadyClusterInfo toReadyClusterInfo(
            GpuV5Udt gpuV5, ClusterByGroupIdAndIdEntity entity) {
        return ReadyClusterInfo.builder()
                .gpu(gpuV5.getName())
                .instanceTypes(gpuV5.getInstanceTypes())
                .supportedNodeTypes(getNodeTypesForInstanceType(gpuV5.getInstanceTypes()))
                .clusterId(entity.getKey().getClusterId())
                .clusterName(entity.getClusterName())
                .name(entity.getClusterName())
                .clusterStatus(entity.getClusterStatus())
                .k8sVersion(entity.getK8sVersion())
                .clusterGroupId(entity.getKey().getClusterGroupId())
                .clusterGroupName(entity.getClusterGroupName())
                .primaryNcaId(entity.getNcaId())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .clusterProvider(entity.getClusterProvider())
                .attributes(
                        unionAttributes(entity.getAttributes(), entity.getCustomAttributes()))
                .region(StringUtils.toRootLowerCase(entity.getRegion()))
                .build();
    }

    private static Set<NodeTypeEnum> getNodeTypesForInstanceType(
            Set<InstanceTypeV5Udt> instanceTypeV5Udts) {
        Set<NodeTypeEnum> supportedNodeTypes = new HashSet<>();
        for (InstanceTypeV5Udt instanceTypeV5Udt : instanceTypeV5Udts) {
            supportedNodeTypes.add(getNodeTypeEnum(instanceTypeV5Udt));
        }
        return supportedNodeTypes;
    }

    private static Set<String> unionAttributes(
            Set<String> attributes, Set<String> customAttributes) {
        Set<String> allAttributes = new HashSet<>(
                Optional.ofNullable(attributes).orElse(new HashSet<>()));
        allAttributes.addAll(Optional.ofNullable(customAttributes).orElse(new HashSet<>()));
        return allAttributes;
    }

    private static ClusterGroups.ClustersResponse toClustersResponse(
            ClusterByGroupIdAndIdEntity entity) {
        return ClusterGroups.ClustersResponse.builder()
                .id(entity.getKey().getClusterId())
                .k8sVersion(entity.getK8sVersion())
                .name(entity.getClusterName())
                .build();
    }


    /**
     * Apply {@link InstanceTypeUsageEnum}-based filtering to the supplied GPU set and
     * convert the survivors into {@link GpuResponse} instances. When filtering is
     * disabled via {@code icms.clusterGroupInstanceTypeUsageFilteringEnabled} the
     * whole set is returned unfiltered.
     */
    public Set<GpuResponse> applyInstanceTypeUsageFilter(
            Set<GpuV5Udt> gpuV5Set,
            @NonNull InstanceTypeUsageEnum instanceTypeUsageFilter) {

        if (!icmsConfigurationProperties.isClusterGroupInstanceTypeUsageFilteringEnabled()) {
            return toGpuV5Response(gpuV5Set);
        }

        Set<GpuResponse> filteredGpuResponse = new HashSet<>();
        for (GpuV5Udt gpuV5Udt : gpuV5Set) {

            // Filtering instanceTypes from gpu using instanceTypeUsageFilter
            Set<InstanceTypeV5Udt> filteredInstanceTypes = getFilteredInstanceTypes(
                    gpuV5Udt.getInstanceTypes(), instanceTypeUsageFilter);

            // If at least one instance types is selected after applying filter then only adding that gpu in response
            if (!filteredInstanceTypes.isEmpty()) {
                GpuResponse gpuResponse = GpuResponse.builder()
                        .name(gpuV5Udt.getName())
                        .instanceTypes(toInstanceTypeV5Response(filteredInstanceTypes))
                        .build();

                filteredGpuResponse.add(gpuResponse);
            }
        }
        return filteredGpuResponse;
    }

    private Set<InstanceTypeV5Udt> getFilteredInstanceTypes(
            Set<InstanceTypeV5Udt> instanceTypesFromDb,
            @NonNull InstanceTypeUsageEnum instanceTypeUsageFilter) {
        Set<NodeTypeEnum> filteredNodeTypes = toNodeTypeEnum(instanceTypeUsageFilter);

        Set<InstanceTypeV5Udt> selectedInstanceTypes = new HashSet<>();

        for (InstanceTypeV5Udt instanceTypeFromDb : instanceTypesFromDb) {
            if (filteredNodeTypes.contains(NvcaConverter.getNodeTypeEnum(instanceTypeFromDb))) {
                // Select instance type if it satisfies instanceTypeUsage filter
                selectedInstanceTypes.add(instanceTypeFromDb);
            }
        }

        return selectedInstanceTypes;
    }

    private void addToGpuResponse(
            Set<GpuResponse> gpusResponse,
            Set<GpuResponse> newGpuSResponse,
            String clusterGroupId, String clusterGroupName) {

        Set<GpuResponse> uniqueGpuResponse = new HashSet<>();

        for (GpuResponse newGpuResponse : newGpuSResponse) {

            // Checking if newGpuResponse(GPU) already present in existingGpus
            for (GpuResponse existingGpuResponse : gpusResponse) {
                if (isGpuAlreadyPresent(existingGpuResponse, newGpuResponse)) {
                    log.info(
                            "For {} clusterGroupId {} clusterGroupName {} GPU is already present hence not adding {} in response",
                            clusterGroupId, clusterGroupName,
                            gpuResponseToJson(existingGpuResponse),
                            gpuResponseToJson(newGpuResponse));
                    continue;
                }
                uniqueGpuResponse.add(newGpuResponse);
            }

            gpusResponse.addAll(uniqueGpuResponse);
            log.info("For {} clusterGroupId {} clusterGroupName adding {} GPU in response",
                     clusterGroupId, clusterGroupName, gpuResponseToJson(uniqueGpuResponse));
        }
    }

    private String gpuResponseToJson(GpuResponse gpuResponse) {
        if (gpuResponse != null) {
            return GsonCompatMapper.toJson(gpuResponse);
        }
        return "";
    }

    private String gpuResponseToJson(Set<GpuResponse> gpuResponseSet) {
        if (!isSetEmptyOrNull(gpuResponseSet)) {
            return GsonCompatMapper.toJson(gpuResponseSet);
        }
        return "";
    }

    private boolean isGpuAlreadyPresent(
            GpuResponse existingGpuResponse,
            GpuResponse newGpuResponse) {

        return existingGpuResponse.getName().equals(newGpuResponse.getName());
    }

    private ClustersResponse toClustersResponse(ReadyClusterInfo readyClusterInfo) {
        return ClustersResponse.builder()
                .name(readyClusterInfo.getName())
                .id(readyClusterInfo.getClusterId())
                .k8sVersion(readyClusterInfo.getK8sVersion())
                .build();
    }

    private Set<GpuResponse> toGpuResponse(Set<GpuUdt> gpusStoredInDb) {

        Set<GpuResponse> gpuResponses = new HashSet<>();
        for (GpuUdt entity : gpusStoredInDb) {
            GpuResponse gpuResponse = GpuResponse.builder()
                    .name(entity.getName())
                    .instanceTypes(toInstanceTypeResponse(entity.getInstanceTypes()))
                    .build();

            gpuResponses.add(gpuResponse);
        }

        return gpuResponses;
    }

    private Set<InstanceTypeResponse> toInstanceTypeResponse(
            Set<InstanceTypeUdt> instanceTypeStoredInDb) {

        Set<InstanceTypeResponse> instanceTypeResponses = new HashSet<>();

        for (InstanceTypeUdt entity : instanceTypeStoredInDb) {
            InstanceTypeResponse instanceTypeResponse =
                    InstanceTypeResponse.builder()
                            .value(entity.getValue())
                            .name(entity.getName())
                            .isDefault(Boolean.TRUE.equals(entity.getIsDefault()) ? true : null)
                            .description(entity.getDescription())

                            /*
                             *  All BYOC clusters are migrated to NVCA flow
                             *  Only non-BYOC manually registered clusters are present with BART flow and added by this function in response
                             *  For non-BYOC manually registered clusters all the instance types will have nodeType as "SINGLE"
                             */
                            .nodeType(NodeTypeEnum.SINGLE)
                            .build();
            instanceTypeResponses.add(instanceTypeResponse);
        }
        return instanceTypeResponses;
    }

    private static Set<InstanceTypeResponse> toInstanceTypeV5Response(
            Set<InstanceTypeV5Udt> instanceTypeStoredInDb) {

        Set<InstanceTypeResponse> instanceTypeResponses = new HashSet<>();

        for (InstanceTypeV5Udt entity : instanceTypeStoredInDb) {
            InstanceTypeResponse instanceTypeResponse =
                    InstanceTypeResponse.builder()
                            .value(entity.getValue())
                            .name(entity.getName())
                            .isDefault(Boolean.TRUE.equals(entity.getIsDefault()) ? true : null)
                            .description(entity.getDescription())
                            .nodeType(getNodeTypeEnum(entity))
                            .build();
            instanceTypeResponses.add(instanceTypeResponse);
        }
        return instanceTypeResponses;
    }


    private Set<GpuResponse> toGpuV5Response(Set<GpuV5Udt> gpusStoredInDb) {

        Set<GpuResponse> gpuResponses = new HashSet<>();
        for (GpuV5Udt entity : gpusStoredInDb) {
            GpuResponse gpuResponse = GpuResponse.builder()
                    .name(entity.getName())
                    .instanceTypes(toInstanceTypeV5Response(entity.getInstanceTypes()))
                    .build();

            gpuResponses.add(gpuResponse);
        }

        return gpuResponses;
    }

    // For WILDCARD we will store * in DB we have to replace it while doing comparision
    private String getNcaIdForWildCard(String ncaId) {
        if (ncaId.equals(ClusterRepository.WILDCARD)) {
            return "*";
        }
        return ncaId;
    }

    /**
     * Deduplicates instance types by name and ensures only one default per GPU.
     * <p>When aggregating instance types across multiple non-BYOC clusters, the same instance type name
     * can appear from different clusters with different isDefault values.
     * Since {@link InstanceTypeResponse} uses Lombok {@code @Data} (equals/hashCode on all fields),
     * these end up as separate entries in the Set. This method resolves both issues:</p>
     *
     * Duplicate names: keeps the entry with isDefault=true.
     * Multiple defaults: alphabetically-first name wins (TreeMap ensures deterministic selection).
     */
    public static Set<InstanceTypeResponse> deduplicateAndEnforceSingleDefault(
            Set<InstanceTypeResponse> instanceTypes) {

        if (instanceTypes == null || instanceTypes.size() <= 1) {
            return instanceTypes;
        }

        // Deduplicate by name using TreeMap (alphabetical order).
        // For duplicate names: keep default=true over non-default; if both same, keep first entry.
        Map<String, InstanceTypeResponse> byName = new TreeMap<>();
        for (InstanceTypeResponse it : instanceTypes) {
            String name = it.getName();
            InstanceTypeResponse existing = byName.get(name);

            if (existing == null
                    || (Boolean.TRUE.equals(it.getIsDefault()) && !Boolean.TRUE.equals(existing.getIsDefault()))) {
                byName.put(name, it);
            }
        }

        // Pick the alphabetically-first default name (deterministic across invocations)
        String defaultName = byName.entrySet().stream()
                .filter(e -> Boolean.TRUE.equals(e.getValue().getIsDefault()))
                .map(Map.Entry::getKey)
                .findFirst()
                .orElse(null);

        // Keep only defaultName as default=true, clear isDefault on all other former defaults
        return byName.values().stream()
                .map(it -> Boolean.TRUE.equals(it.getIsDefault()) && !it.getName().equals(defaultName)
                        ? InstanceTypeResponse.builder()
                                .name(it.getName()).value(it.getValue())
                                .description(it.getDescription()).isDefault(null)
                                .nodeType(it.getNodeType()).build()
                        : it)
                .collect(Collectors.toSet());
    }

}

