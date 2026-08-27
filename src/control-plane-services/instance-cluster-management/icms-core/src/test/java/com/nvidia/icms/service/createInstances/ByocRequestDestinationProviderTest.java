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

import static com.nvidia.icms.util.TestUtil.DUMMY_ATTRIBUTES;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CREATION_QUEUE_URL;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_REGION;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.ComputePlatform;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.account.GpuUsageFilter;
import com.nvidia.icms.service.byoc.ByocValidationService;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.List;
import java.util.Set;
import java.util.UUID;

import static com.nvidia.icms.service.platform.ComputePlatformTestFixtures.PLATFORM_CLUSTER_GROUP_NAME;

@ExtendWith(MockitoExtension.class)
class ByocRequestDestinationProviderTest extends CreateInstancesTestBase {

    private static final String NVCA_CLUSTER_NAME = "nvca-cluster";
    private static final String NVCA_CLUSTER_1_NAME = "nvca-cluster-1";
    private static final String NVCA_CLUSTER_2_NAME = "nvca-cluster-2";
    private static final String CLUSTER_1_NAME = "cluster-1";
    private static final String CLUSTER_2_NAME = "cluster-2";
    private static final String GROUP_1_ID = "group-1";
    private static final String GROUP_2_ID = "group-2";
    private static final String WILDCARD_GROUP_ID = "wildcard-group";
    private static final String DIFFERENT_GPU = "different-gpu";
    private static final String DIFFERENT_INSTANCE_TYPE = "different-instance-type";

    @Mock private ByocValidationService byocValidationService;
    @Mock private NvcaClusterRepository nvcaClusterRepository;
    @Mock private DestinationCreator destinationCreator;

    private ByocRequestDestinationProvider provider;
    private SpotInstanceRequestSchema instanceRequest;
    private ClusterByGroupIdAndIdEntity clusterEntity;
    private GpuV5Udt gpuV5;

    @BeforeEach
    void setUp() {
        provider = new ByocRequestDestinationProvider(byocValidationService, nvcaClusterRepository, destinationCreator, new ComputePlatformService(List.of()));
        instanceRequest = createInstanceRequest();
        clusterEntity = createClusterEntity();
        gpuV5 = createGpuV5();

        // Lenient: isClusterAllowed tests never reach destinationCreator, so a strict stub
        // in setUp would cause UnnecessaryStubbingException in those tests.
        lenient().when(destinationCreator.addAvailableDestinations(any(), any(), any(), any()))
                .thenReturn(List.of());
    }

    // -------------------------------------------------------------------------
    // isClusterAllowed
    // -------------------------------------------------------------------------

    @Test
    void isClusterAllowed_withValidCluster_returnsTrue() {
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .regionNames(Set.of(DUMMY_REGION))
                .clusterNames(Set.of(DUMMY_BYOC_CLUSTER_NAME))
                .attributes(DUMMY_ATTRIBUTES)
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();
        when(byocValidationService.validateClustersStatusAndGetProviderForNvca(anyString()))
                .thenReturn(CloudProvider.AWS);

        assertTrue(provider.isClusterAllowed(clusterEntity, filter));
    }

    @Test
    void isClusterAllowed_whenRegionMismatch_returnsFalse() {
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .regionNames(Set.of("different-region"))
                .build();

        assertFalse(provider.isClusterAllowed(clusterEntity, filter));
    }

    @Test
    void isClusterAllowed_whenClusterNameMismatch_returnsFalse() {
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .regionNames(Set.of(DUMMY_REGION))
                .clusterNames(Set.of("different-cluster"))
                .build();

        assertFalse(provider.isClusterAllowed(clusterEntity, filter));
    }

    @Test
    void isClusterAllowed_whenAttributeMismatch_returnsFalse() {
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .regionNames(Set.of(DUMMY_REGION))
                .clusterNames(Set.of(DUMMY_BYOC_CLUSTER_NAME))
                .attributes(Set.of("different-attribute"))
                .build();

        assertFalse(provider.isClusterAllowed(clusterEntity, filter));
    }

    @Test
    void isClusterAllowed_whenCustomAttributeMatches_returnsTrue() {
        clusterEntity.setCustomAttributes(Set.of("custom-attr"));
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .regionNames(Set.of(DUMMY_REGION))
                .clusterNames(Set.of(DUMMY_BYOC_CLUSTER_NAME))
                .attributes(Set.of("custom-attr"))
                .build();
        when(byocValidationService.validateClustersStatusAndGetProviderForNvca(anyString()))
                .thenReturn(CloudProvider.AWS);

        assertTrue(provider.isClusterAllowed(clusterEntity, filter));
    }

    @Test
    void isClusterAllowed_whenProviderValidationReturnsNull_returnsFalse() {
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .regionNames(Set.of(DUMMY_REGION))
                .clusterNames(Set.of(DUMMY_BYOC_CLUSTER_NAME))
                .attributes(DUMMY_ATTRIBUTES)
                .build();
        when(byocValidationService.validateClustersStatusAndGetProviderForNvca(anyString()))
                .thenReturn(null);

        assertFalse(provider.isClusterAllowed(clusterEntity, filter));
    }

    // -------------------------------------------------------------------------
    // generateTargetedDestinationList
    // -------------------------------------------------------------------------

    @Test
    void generateTargetedDestinationList_withSingleCluster_callsDestinationCreator() {
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();
        when(byocValidationService.getCreationQueueForReadyCluster(any(), any(), anyString()))
                .thenReturn(DUMMY_CREATION_QUEUE_URL);
        RequestInstanceDestination dest = createDestination(CloudProvider.AWS);
        when(destinationCreator.addAvailableDestinations(any(), any(), any(), any()))
                .thenReturn(List.of(dest));

        Set<RequestInstanceDestination> result =
                provider.generateTargetedDestinationList(Set.of(clusterEntity), filter, instanceRequest);

        assertNotNull(result);
        assertFalse(result.isEmpty());
        verify(destinationCreator).addAvailableDestinations(any(), any(), any(), any());
    }

    @Test
    void generateTargetedDestinationList_withInvalidGpu_doesNotCreateDestination() {
        GpuV5Udt differentGpu = new GpuV5Udt();
        differentGpu.setName("different-gpu");
        differentGpu.setInstanceTypes(Set.of(createInstanceTypeV5()));
        clusterEntity.setGpusV5(Set.of(differentGpu));

        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();

        Set<RequestInstanceDestination> result =
                provider.generateTargetedDestinationList(Set.of(clusterEntity), filter, instanceRequest);

        // No queue URL call, no destination created for mismatched GPU
        verify(byocValidationService, times(0)).getCreationQueueForReadyCluster(any(), any(), anyString());
        assertTrue(result.isEmpty());
    }

    @Test
    void generateTargetedDestinationList_withCustomGpuCount_delegatesToDestinationCreator() {
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();
        instanceType.setGpuCount(2);
        gpuV5.setInstanceTypes(Set.of(instanceType));
        clusterEntity.setGpusV5(Set.of(gpuV5));
        when(byocValidationService.getCreationQueueForReadyCluster(any(), any(), anyString()))
                .thenReturn(DUMMY_CREATION_QUEUE_URL);
        RequestInstanceDestination dest = createDestination(CloudProvider.AWS);
        when(destinationCreator.addAvailableDestinations(any(), any(), any(), any()))
                .thenReturn(List.of(dest));

        Set<RequestInstanceDestination> result =
                provider.generateTargetedDestinationList(Set.of(clusterEntity), filter, instanceRequest);

        verify(destinationCreator).addAvailableDestinations(any(), any(), any(), any());
        assertEquals(1, result.size());
        assertTrue(result.contains(dest));
    }

    @Test
    void generateTargetedDestinationList_withMultipleClusters_processesAll() {
        ClusterByGroupIdAndIdEntity cluster2 = createClusterEntity();
        cluster2.getKey().setClusterId("cluster-2");
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();
        when(byocValidationService.getCreationQueueForReadyCluster(any(), any(), anyString()))
                .thenReturn(DUMMY_CREATION_QUEUE_URL);

        provider.generateTargetedDestinationList(Set.of(clusterEntity, cluster2), filter, instanceRequest);

        verify(destinationCreator, times(2)).addAvailableDestinations(any(), any(), any(), any());
    }

    // -------------------------------------------------------------------------
    // getNonTargetedDestinations — BYOC / NVCA
    // -------------------------------------------------------------------------

    @Test
    void getNonTargetedDestinations_withValidClusters_returnsMatchingDestinations() {
        ClustersByAuthorizedAccountsEntity cluster1 = createNvcaCluster(GROUP_1_ID, NVCA_CLUSTER_1_NAME, CLUSTER_1_NAME);
        ClustersByAuthorizedAccountsEntity cluster2 = createNvcaCluster(GROUP_2_ID, NVCA_CLUSTER_2_NAME, CLUSTER_2_NAME);

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(List.of(cluster1, cluster2));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD))
                .thenReturn(List.of());
        when(byocValidationService.validateClustersStatusAndGetProviderForNvca(anyString()))
                .thenReturn(CloudProvider.AWS);

        when(destinationCreator.addAvailableDestinations(any(), any(), any(), any()))
                .thenAnswer(inv -> List.of(RequestInstanceDestination.builder()
                        .clusterId(UUID.randomUUID().toString())
                        .cloudProvider(CloudProvider.AWS)
                        .build()));

        Set<RequestInstanceDestination> result = provider.getNonTargetedDestinations(instanceRequest);

        assertNotNull(result);
        assertEquals(2, result.size());
    }

    @Test
    void getNonTargetedDestinations_withNoClusters_returnsEmpty() {
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(anyString())).thenReturn(List.of());

        Set<RequestInstanceDestination> result = provider.getNonTargetedDestinations(instanceRequest);

        assertTrue(result.isEmpty());
    }

    @Test
    void getNonTargetedDestinations_withUnhealthyClusters_returnsEmpty() {
        ClustersByAuthorizedAccountsEntity cluster = createNvcaCluster(DUMMY_CLUSTER_GROUP_ID, NVCA_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_NAME);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(anyString())).thenReturn(List.of(cluster));
        when(byocValidationService.validateClustersStatusAndGetProviderForNvca(anyString())).thenReturn(null);

        Set<RequestInstanceDestination> result = provider.getNonTargetedDestinations(instanceRequest);

        assertTrue(result.isEmpty());
    }

    @Test
    void getNonTargetedDestinations_withWildcardNcaId_includesWildcardClusters() {
        ClustersByAuthorizedAccountsEntity wildcardCluster = createNvcaCluster(
                WILDCARD_GROUP_ID, NVCA_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_NAME);
        wildcardCluster.setNcaId(ClusterRepository.WILDCARD);

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID)).thenReturn(List.of());
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD))
                .thenReturn(List.of(wildcardCluster));
        when(byocValidationService.validateClustersStatusAndGetProviderForNvca(anyString()))
                .thenReturn(CloudProvider.AWS);
        RequestInstanceDestination dest = createDestination(CloudProvider.AWS);
        when(destinationCreator.addAvailableDestinations(any(), any(), any(), any())).thenReturn(List.of(dest));

        Set<RequestInstanceDestination> result = provider.getNonTargetedDestinations(instanceRequest);

        assertEquals(1, result.size());
    }

    @Test
    void getNonTargetedDestinations_withPlatformCluster_excludesPlatformCluster() {
        // Configure a platform whose cluster group matches the cluster below so the
        // platform-cluster filter is actually exercised (default setUp uses an empty registry).
        ByocRequestDestinationProvider platformAwareProvider = new ByocRequestDestinationProvider(
                byocValidationService, nvcaClusterRepository, destinationCreator,
                new ComputePlatformService(List.of(
                        ComputePlatform.builder().name("platform").clusterGroupName(PLATFORM_CLUSTER_GROUP_NAME).build())));

        ClustersByAuthorizedAccountsEntity platformCluster = createNvcaCluster(
                DUMMY_CLUSTER_GROUP_ID, PLATFORM_CLUSTER_GROUP_NAME, DUMMY_BYOC_CLUSTER_NAME);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(anyString()))
                .thenReturn(List.of(platformCluster));

        Set<RequestInstanceDestination> result =
                platformAwareProvider.getNonTargetedDestinations(instanceRequest);

        assertTrue(result.isEmpty());
        // Excluded by the platform filter before any provider validation runs.
        verify(byocValidationService, never()).validateClustersStatusAndGetProviderForNvca(anyString());
    }

    @Test
    void getNonTargetedDestinations_withInvalidGpu_returnsEmpty() {
        ClustersByAuthorizedAccountsEntity cluster = createNvcaClusterWithCustomGpu(
                DUMMY_CLUSTER_GROUP_ID, NVCA_CLUSTER_NAME, DIFFERENT_GPU);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(anyString())).thenReturn(List.of(cluster));

        Set<RequestInstanceDestination> result = provider.getNonTargetedDestinations(instanceRequest);

        assertTrue(result.isEmpty());
    }

    @Test
    void getNonTargetedDestinations_withInvalidInstanceType_returnsEmpty() {
        ClustersByAuthorizedAccountsEntity cluster = createNvcaClusterWithCustomInstanceType(
                DUMMY_CLUSTER_GROUP_ID, NVCA_CLUSTER_NAME, DIFFERENT_INSTANCE_TYPE);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(anyString())).thenReturn(List.of(cluster));

        Set<RequestInstanceDestination> result = provider.getNonTargetedDestinations(instanceRequest);

        assertTrue(result.isEmpty());
    }

    @Test
    void getNonTargetedDestinations_withMultipleGpus_onlyIncludesAllowedGpu() {
        ClustersByAuthorizedAccountsEntity cluster = createNvcaCluster(
                DUMMY_CLUSTER_GROUP_ID, NVCA_CLUSTER_NAME, DUMMY_BYOC_CLUSTER_NAME);
        GpuV5Udt additionalGpu = new GpuV5Udt();
        additionalGpu.setName("gpu-2");
        additionalGpu.setInstanceTypes(Set.of(createInstanceTypeV5()));
        cluster.setGpusV5(Set.of(gpuV5, additionalGpu));

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID)).thenReturn(List.of(cluster));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD)).thenReturn(List.of());
        when(byocValidationService.validateClustersStatusAndGetProviderForNvca(anyString())).thenReturn(CloudProvider.AWS);
        RequestInstanceDestination dest = createDestination(CloudProvider.AWS);
        when(destinationCreator.addAvailableDestinations(any(), any(), any(), any())).thenReturn(List.of(dest));

        Set<RequestInstanceDestination> result = provider.getNonTargetedDestinations(instanceRequest);

        assertEquals(1, result.size());
    }
}
