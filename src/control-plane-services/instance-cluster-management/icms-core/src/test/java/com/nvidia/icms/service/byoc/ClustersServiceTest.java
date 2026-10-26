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
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroupResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.GpuResponse;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.ClusterAuthorizationService;
import com.nvidia.icms.service.gating.GpuGatingService;
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
 * Covers authorized non-BYOC discovery alongside NVCA discovery and GPU gating.
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
    private static final String GATED_NCA = "GatedNca1aBcDeF";
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
                clusterAuthorizationService,
                new GpuGatingService(icmsConfigurationProperties, null));

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(WILDCARD)).thenReturn(List.of());
    }

    @Test
    void getRegisteredClustersForNcaId_returnsNoPlatformGroupWhenNotAuthorized() {
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DISALLOWED_NCA))
                .thenReturn(List.of());

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(DISALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);

        assertTrue(response.getClusterGroup().isEmpty());
        verify(clusterAuthorizationService).fetchAuthorizedClusterGroup(
                DISALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);
    }

    @Test
    void getRegisteredClustersForNcaId_keepsAuthorizedPlatformGpus() {
        stubAuthorizedGroup(PLATFORM_GROUP_ID, PLATFORM_GROUP_NAME,
                Set.of(RESTRICTED_GPU, OTHER_GPU), ALLOWED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(ALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);

        ClusterGroups platformGroup = findGroup(response, PLATFORM_GROUP_NAME);
        assertEquals(Set.of(RESTRICTED_GPU, OTHER_GPU), gpuNames(platformGroup),
                "discovery returns the authorized provider's GPU set");
        verify(clusterAuthorizationService).fetchAuthorizedClusterGroup(
                ALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);
    }

    @Test
    void getRegisteredClustersForNcaId_doesNotGateByocGroup_evenForDisallowedNca() {
        icmsConfigurationProperties.setGpuAllowedNcaIds(Map.of(RESTRICTED_GPU, List.of(ALLOWED_NCA)));
        stubAuthorizedGroup(BYOC_GROUP_ID, BYOC_GROUP_NAME,
                Set.of(RESTRICTED_GPU, OTHER_GPU), DISALLOWED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(DISALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);

        ClusterGroups byocGroup = findGroup(response, BYOC_GROUP_NAME);
        assertEquals(Set.of(RESTRICTED_GPU, OTHER_GPU), gpuNames(byocGroup), "BYOC groups are never gated");
    }

    @Test
    void getRegisteredClustersForNcaId_dropsGatedGpusAndInstanceTypes_forGatedNca() {
        icmsConfigurationProperties.setGpuGating(Map.of(
                GATED_NCA, Map.of(OTHER_GPU, List.of(instanceTypeName(OTHER_GPU, 0)))));
        stubAuthorizedGroup(BYOC_GROUP_ID, BYOC_GROUP_NAME,
                Set.of(RESTRICTED_GPU, OTHER_GPU), GATED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(GATED_NCA, InstanceTypeUsageEnum.DEFAULT);

        ClusterGroups byocGroup = findGroup(response, BYOC_GROUP_NAME);
        assertEquals(Set.of(OTHER_GPU), gpuNames(byocGroup),
                "gpu gating applies to BYOC groups, unlike gpu-allowed-nca-ids");
        assertEquals(Set.of(instanceTypeName(OTHER_GPU, 0)), instanceTypeNames(byocGroup, OTHER_GPU),
                "only the allowed instance type of the allowed GPU survives");
    }

    @Test
    void getRegisteredClustersForNcaId_dropsWholeGroup_whenGatingRemovesEveryGpu() {
        icmsConfigurationProperties.setGpuGating(Map.of(
                GATED_NCA, Map.of("dummy-gpu-not-in-any-group", List.of("dummy-instance-type"))));
        stubAuthorizedGroup(BYOC_GROUP_ID, BYOC_GROUP_NAME,
                Set.of(RESTRICTED_GPU, OTHER_GPU), GATED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(GATED_NCA, InstanceTypeUsageEnum.DEFAULT);

        assertTrue(response.getClusterGroup().isEmpty(),
                "cluster group must be dropped entirely when gating removes all of its GPUs");
    }

    /** A GPU whose every instance type is gated out is dropped, not returned empty. */
    @Test
    void getRegisteredClustersForNcaId_dropsGpu_whenAllItsInstanceTypesAreGatedOut() {
        icmsConfigurationProperties.setGpuGating(Map.of(
                GATED_NCA, Map.of(OTHER_GPU, List.of("dummy-instance-type-not-on-this-gpu"))));
        stubAuthorizedGroup(BYOC_GROUP_ID, BYOC_GROUP_NAME, Set.of(OTHER_GPU), GATED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(GATED_NCA, InstanceTypeUsageEnum.DEFAULT);

        assertTrue(response.getClusterGroup().isEmpty(),
                "a GPU left with no instance types must be dropped, taking the empty group with it");
    }

    @Test
    void getRegisteredClustersForNcaId_leavesUngatedNcaUntouched() {
        icmsConfigurationProperties.setGpuGating(Map.of(
                GATED_NCA, Map.of(OTHER_GPU, List.of(instanceTypeName(OTHER_GPU, 0)))));
        stubAuthorizedGroup(BYOC_GROUP_ID, BYOC_GROUP_NAME,
                Set.of(RESTRICTED_GPU, OTHER_GPU), ALLOWED_NCA);

        ClusterGroupResponse response =
                clustersService.getRegisteredClustersForNcaId(ALLOWED_NCA, InstanceTypeUsageEnum.DEFAULT);

        ClusterGroups byocGroup = findGroup(response, BYOC_GROUP_NAME);
        assertEquals(Set.of(RESTRICTED_GPU, OTHER_GPU), gpuNames(byocGroup),
                "an account without a gating entry is unaffected");
        assertEquals(Set.of(instanceTypeName(OTHER_GPU, 0), instanceTypeName(OTHER_GPU, 1)),
                instanceTypeNames(byocGroup, OTHER_GPU));
    }

    private void stubAuthorizedGroup(String groupId, String groupName, Set<String> gpuNames,
                                     String requestingNcaId) {
        ClusterGroups group = ClusterGroups.builder()
                .id(groupId)
                .name(groupName)
                .ncaId(WILDCARD)
                .authorizedNcaIds(Set.of())
                .gpus(gpuNames.stream().map(name -> GpuResponse.builder().name(name)
                        .instanceTypes(Set.of(
                                ClusterGroups.InstanceTypeResponse.builder()
                                        .name(instanceTypeName(name, 0))
                                        .nodeType(NodeTypeEnum.SINGLE).build(),
                                ClusterGroups.InstanceTypeResponse.builder()
                                        .name(instanceTypeName(name, 1))
                                        .nodeType(NodeTypeEnum.SINGLE).build()))
                        .build()).collect(Collectors.toSet()))
                .build();
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(requestingNcaId)).thenReturn(List.of());
        when(clusterAuthorizationService.fetchAuthorizedClusterGroup(
                requestingNcaId, InstanceTypeUsageEnum.DEFAULT)).thenReturn(group);
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

    private Set<String> instanceTypeNames(ClusterGroups clusterGroup, String gpuName) {
        return clusterGroup.getGpus().stream()
                .filter(gpu -> gpuName.equals(gpu.getName()))
                .flatMap(gpu -> gpu.getInstanceTypes().stream())
                .map(ClusterGroups.InstanceTypeResponse::getName)
                .collect(Collectors.toSet());
    }

    private static String instanceTypeName(String gpuName, int index) {
        return gpuName + ".instance-type-" + index;
    }
}
