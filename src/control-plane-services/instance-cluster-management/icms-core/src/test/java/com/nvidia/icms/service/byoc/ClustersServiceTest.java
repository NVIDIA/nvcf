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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroupResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.GpuResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.ClusterAuthorizationService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

/**
 * Focused coverage for the BART-legacy {@code /clusterGroups} GPU-to-NCA restriction added in
 * {@link ClustersService#getRegisteredClustersForNcaId}. Detailed targeting is disabled so the
 * request flows through {@code fetchClusterGroupsForBart} and the new
 * {@code filterRestrictedGpusForComputePlatformGroups} choke point.
 */
@ExtendWith(MockitoExtension.class)
class ClustersServiceTest {

    // Compute-platform group name/id from the shared fixture.
    private static final String PLATFORM_GROUP_ID = ComputePlatformTestFixtures.PLATFORM_CLUSTER_GROUP_ID;
    private static final String PLATFORM_GROUP_NAME = ComputePlatformTestFixtures.PLATFORM_CLUSTER_GROUP_NAME;
    private static final String BYOC_GROUP_ID = "byoc-group-id";
    private static final String BYOC_GROUP_NAME = "byoc-group";

    private static final String RESTRICTED_GPU = "dummy-restricted-gpu";
    private static final String OTHER_GPU = "dummy-gpu";
    private static final String ALLOWED_NCA = "allowed-nca";
    private static final String DISALLOWED_NCA = "disallowed-nca";
    private static final String WILDCARD = ClusterRepository.WILDCARD;

    @Mock
    private ClusterRepository clusterRepository;
    @Mock
    private NvcaClusterRepository nvcaClusterRepository;
    @Mock
    private NvcaConfigurationProperties nvcaConfigurationProperties;
    @Mock
    private InstanceServiceHelper instanceServiceHelper;
    @Mock
    private ClusterTargetingHelper clusterTargetingHelper;
    @Mock
    private ClusterAuthorizationService clusterAuthorizationService;

    private IcmsConfigurationProperties icmsConfigurationProperties;
    private ComputePlatformService computePlatformService;
    private ClustersService clustersService;

    @BeforeEach
    void setUp() {
        icmsConfigurationProperties = new IcmsConfigurationProperties();
        computePlatformService = ComputePlatformTestFixtures.nonByocComputePlatformService();
        clustersService = new ClustersService(
                clusterRepository,
                nvcaClusterRepository,
                nvcaConfigurationProperties,
                instanceServiceHelper,
                icmsConfigurationProperties,
                clusterTargetingHelper,
                computePlatformService,
                clusterAuthorizationService);

        // BART (legacy) flow, not detailed targeting.
        when(clusterAuthorizationService.isDetailedTargetingFlowEnabled()).thenReturn(false);
        // No NVCA groups for the always-queried WILDCARD account; isolate the BART group.
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(WILDCARD)).thenReturn(List.of());
    }

    @Test
    void getRegisteredClustersForNcaId_dropsRestrictedGpuOnPlatformGroup_forDisallowedNca() {
        icmsConfigurationProperties.setGpuAllowedNcaIds(Map.of(RESTRICTED_GPU, List.of(ALLOWED_NCA)));
        stubBartGroup(PLATFORM_GROUP_ID, PLATFORM_GROUP_NAME, Set.of(RESTRICTED_GPU, OTHER_GPU), DISALLOWED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(DISALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);

        ClusterGroups platformGroup = findGroup(response, PLATFORM_GROUP_NAME);
        assertEquals(Set.of(OTHER_GPU), gpuNames(platformGroup),
                "restricted GPU must be dropped, other GPU retained");
    }

    @Test
    void getRegisteredClustersForNcaId_dropsWholePlatformGroup_whenOnlyRestrictedGpuRemains() {
        icmsConfigurationProperties.setGpuAllowedNcaIds(Map.of(RESTRICTED_GPU, List.of(ALLOWED_NCA)));
        stubBartGroup(PLATFORM_GROUP_ID, PLATFORM_GROUP_NAME, Set.of(RESTRICTED_GPU), DISALLOWED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(DISALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);

        assertTrue(response.getClusterGroup().isEmpty(),
                "compute-platform group must be dropped entirely when its only GPU is restricted");
    }

    @Test
    void getRegisteredClustersForNcaId_keepsRestrictedGpuOnPlatformGroup_forAllowedNca() {
        icmsConfigurationProperties.setGpuAllowedNcaIds(Map.of(RESTRICTED_GPU, List.of(ALLOWED_NCA)));
        stubBartGroup(PLATFORM_GROUP_ID, PLATFORM_GROUP_NAME, Set.of(RESTRICTED_GPU, OTHER_GPU), ALLOWED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(ALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);

        ClusterGroups platformGroup = findGroup(response, PLATFORM_GROUP_NAME);
        assertEquals(Set.of(RESTRICTED_GPU, OTHER_GPU), gpuNames(platformGroup),
                "allowed NCA keeps all GPUs");
    }

    @Test
    void getRegisteredClustersForNcaId_doesNotGateByocGroup_evenForDisallowedNca() {
        icmsConfigurationProperties.setGpuAllowedNcaIds(Map.of(RESTRICTED_GPU, List.of(ALLOWED_NCA)));
        stubBartGroup(BYOC_GROUP_ID, BYOC_GROUP_NAME, Set.of(RESTRICTED_GPU, OTHER_GPU), DISALLOWED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(DISALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);

        ClusterGroups byocGroup = findGroup(response, BYOC_GROUP_NAME);
        assertEquals(Set.of(RESTRICTED_GPU, OTHER_GPU), gpuNames(byocGroup), "BYOC groups are never gated");
    }

    /** Wires the BART repository calls so the WILDCARD account exposes a single group with a READY cluster. */
    private void stubBartGroup(String groupId, String groupName, Set<String> gpuNames, String requestingNcaId) {
        ClusterGroupsByAuthorizedAccountsEntity groupEntity = ClusterGroupsByAuthorizedAccountsEntity.builder()
                .key(ClusterGroupsByAuthorizedAccountsKey.builder()
                        .clusterGroupId(groupId)
                        .clusterGroupName(groupName)
                        .ncaIdKey(WILDCARD)
                        .build())
                .ncaId(WILDCARD)
                .authorizedNcaIds(Set.of())
                .gpus(gpuNames.stream()
                        .map(name -> GpuUdt.builder().name(name).instanceTypes(Set.of()).build())
                        .collect(Collectors.toSet()))
                .build();

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(requestingNcaId)).thenReturn(List.of());
        when(clusterRepository.getAllClusterGroupsInAuthorizedAccount(WILDCARD))
                .thenReturn(List.of(groupEntity));
        when(clusterRepository.getAllClusterGroupsInAuthorizedAccount(requestingNcaId))
                .thenReturn(List.of());
        when(clusterRepository.getClustersFromClusterGroup(groupId))
                .thenReturn(List.of(readyCluster(groupId)));
    }

    private ClusterByGroupIdAndIdEntity readyCluster(String groupId) {
        return ClusterByGroupIdAndIdEntity.builder()
                .key(ClusterByGroupIdAndIdKey.builder()
                        .clusterGroupId(groupId)
                        .clusterId("cluster-1")
                        .build())
                .clusterName("cluster-1")
                .clusterStatus(ClusterStatusEnum.READY)
                .build();
    }

    private ClusterGroups findGroup(ClusterGroupResponse response, String name) {
        return response.getClusterGroup().stream()
                .filter(cg -> name.equals(cg.getName()))
                .findFirst()
                .orElseThrow();
    }

    private Set<String> gpuNames(ClusterGroups clusterGroup) {
        return clusterGroup.getGpus().stream()
                .map(GpuResponse::getName)
                .collect(Collectors.toSet());
    }
}
