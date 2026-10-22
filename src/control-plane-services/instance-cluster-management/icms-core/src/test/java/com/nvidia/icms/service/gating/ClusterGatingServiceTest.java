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
package com.nvidia.icms.service.gating;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.ClustersResponse;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.uec.IcmsUnifiedError;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import org.junit.jupiter.api.Test;

/**
 * Covers {@code icms.cluster-gating} on all five surfaces it is wired into. The opening block of
 * no-op tests is the backward-compatibility contract: an account without a gating entry must come
 * back untouched and must not trigger the owned-cluster lookup.
 */
class ClusterGatingServiceTest {

    private static final String GATED_NCA_ID = "gated-nca-id";
    private static final String OTHER_NCA_ID = "other-nca-id";

    private static final String CLUSTER_ALLOWED = "cluster-allowed";
    private static final String CLUSTER_DENIED = "cluster-denied";
    private static final String CLUSTER_OWNED = "cluster-owned";

    private static final String GPU = "L40";
    private static final String INSTANCE_TYPE = "DGX-CLOUD.GPU.L40_1x";
    private static final String REGION = "us-west-2";

    private ClusterRepository clusterRepository;

    /**
     * Builds a service gating {@code ncaId} to {@code allowedClusters}, with the repository
     * reporting {@code ownedClusterIds} as clusters the caller registered itself.
     */
    private ClusterGatingService gatingFor(String ncaId, List<String> allowedClusters,
                                           String... ownedClusterIds) {
        IcmsConfigurationProperties properties = new IcmsConfigurationProperties();
        properties.setClusterGating(new HashMap<>(Map.of(ncaId, allowedClusters)));
        return gatingFor(properties, ownedClusterIds);
    }

    private ClusterGatingService gatingFor(IcmsConfigurationProperties properties,
                                           String... ownedClusterIds) {
        clusterRepository = mock(ClusterRepository.class);
        // Stubbed only when the test actually owns clusters, so the no-op tests can assert the
        // repository was never touched without a stubbing call muddying the verification.
        if (ownedClusterIds.length > 0) {
            when(clusterRepository.getAllClustersInAnAccount(any()))
                    .thenReturn(Arrays.stream(ownedClusterIds)
                                        .map(clusterId -> ClusterEntity.builder()
                                                .clusterId(clusterId)
                                                .build())
                                        .collect(Collectors.toList()));
        }
        return new ClusterGatingService(properties, null, clusterRepository);
    }

    private ClusterGatingService gatingForAllowedCluster() {
        return gatingFor(GATED_NCA_ID, List.of(CLUSTER_ALLOWED));
    }

    private static InstanceTypeAvailabilityResponse availability(String... clusterIds) {
        InstanceTypeAvailabilityResponse.Region region = InstanceTypeAvailabilityResponse.Region
                .builder()
                .regionName(REGION)
                .clusters(Arrays.stream(clusterIds)
                                  .map(clusterId -> InstanceTypeAvailabilityResponse.Cluster
                                          .builder()
                                          .clusterId(clusterId)
                                          .clusterName(clusterId)
                                          .build())
                                  .collect(Collectors.toCollection(LinkedHashSet::new)))
                .build();

        InstanceTypeAvailabilityResponse.InstanceType instanceType =
                InstanceTypeAvailabilityResponse.InstanceType.builder()
                        .instanceName(INSTANCE_TYPE)
                        .regions(new LinkedHashSet<>(Set.of(region)))
                        .build();

        InstanceTypeAvailabilityResponse.Gpu gpu = InstanceTypeAvailabilityResponse.Gpu.builder()
                .gpuName(GPU)
                .instanceTypes(new LinkedHashSet<>(Set.of(instanceType)))
                .build();

        InstanceTypeAvailabilityResponse result = new InstanceTypeAvailabilityResponse();
        result.setGpus(new LinkedHashSet<>(Set.of(gpu)));
        return result;
    }

    private static Set<String> availabilityClusterIds(InstanceTypeAvailabilityResponse result) {
        return result.getGpus().stream()
                .flatMap(gpu -> gpu.getInstanceTypes().stream())
                .flatMap(instanceType -> instanceType.getRegions().stream())
                .flatMap(region -> region.getClusters().stream())
                .map(InstanceTypeAvailabilityResponse.Cluster::getClusterId)
                .collect(Collectors.toSet());
    }

    private static ClusterGroups clusterGroup(String groupName, String... clusterIds) {
        return ClusterGroups.builder()
                .id(groupName + "-id")
                .name(groupName)
                .clusters(Arrays.stream(clusterIds)
                                  .map(clusterId -> ClustersResponse.builder()
                                          .id(clusterId)
                                          .name(clusterId)
                                          .build())
                                  .collect(Collectors.toCollection(LinkedHashSet::new)))
                .build();
    }

    private static GetClusterResponse cluster(String clusterId, String ownerNcaId) {
        return GetClusterResponse.builder()
                .clusterId(clusterId)
                .clusterName(clusterId)
                .ncaId(ownerNcaId)
                .gpus(new HashSet<>())
                .build();
    }

    private static List<GpuUsageResponse.Gpu> usage(String... clusterIds) {
        GpuUsageResponse.Region region = GpuUsageResponse.Region.builder()
                .regionName(REGION)
                .clusters(Arrays.stream(clusterIds)
                                  .map(clusterId -> GpuUsageResponse.Cluster.builder()
                                          .clusterId(clusterId)
                                          .build())
                                  .collect(Collectors.toCollection(ArrayList::new)))
                .build();

        GpuUsageResponse.Instance instance = GpuUsageResponse.Instance.builder()
                .instanceName(INSTANCE_TYPE)
                .regions(new ArrayList<>(List.of(region)))
                .build();

        return new ArrayList<>(List.of(GpuUsageResponse.Gpu.builder()
                                               .gpuName(GPU)
                                               .instances(new ArrayList<>(List.of(instance)))
                                               .build()));
    }

    private static Set<String> usageClusterIds(List<GpuUsageResponse.Gpu> gpus) {
        return gpus.stream()
                .flatMap(gpu -> gpu.getInstances().stream())
                .flatMap(instance -> instance.getRegions().stream())
                .flatMap(region -> region.getClusters().stream())
                .map(GpuUsageResponse.Cluster::getClusterId)
                .collect(Collectors.toSet());
    }

    private static RequestInstanceDestination destination(String clusterId, String ownerNcaId) {
        return RequestInstanceDestination.builder()
                .clusterId(clusterId)
                .clusterName(clusterId)
                .ncaId(ownerNcaId)
                .gpuName(GPU)
                .build();
    }

    private static SpotInstanceRequestSchema request() {
        SpotInstanceRequestSchema instanceRequest = new SpotInstanceRequestSchema();
        instanceRequest.setNcaId(GATED_NCA_ID);
        return instanceRequest;
    }

    // ---------------------------------------------------------------------------------------
    // No-op contract: an ungated account must be indistinguishable from the pre-gating service.
    // ---------------------------------------------------------------------------------------

    @Test
    void removeGatedClustersFromAccountInfo_whenNcaIsNotGated_keepsEverything() {
        ClusterGatingService gatingService = gatingForAllowedCluster();
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingService.removeGatedClustersFromAccountInfo(result, OTHER_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), availabilityClusterIds(result));
        verifyNoInteractions(clusterRepository);
    }

    @Test
    void removeGatedClusterGroups_whenNcaIsNotGated_keepsEverything() {
        ClusterGatingService gatingService = gatingForAllowedCluster();
        Set<ClusterGroups> groups = new LinkedHashSet<>(Set.of(
                clusterGroup("group-1", CLUSTER_ALLOWED, CLUSTER_DENIED)));

        gatingService.removeGatedClusterGroups(groups, OTHER_NCA_ID);

        assertEquals(1, groups.size());
        assertEquals(2, groups.iterator().next().getClusters().size());
        verifyNoInteractions(clusterRepository);
    }

    @Test
    void removeGatedAuthorizedClusters_whenNcaIsNotGated_keepsEverything() {
        ClusterGatingService gatingService = gatingForAllowedCluster();
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster(CLUSTER_ALLOWED, OTHER_NCA_ID), cluster(CLUSTER_DENIED, OTHER_NCA_ID)));

        gatingService.removeGatedAuthorizedClusters(clusters, OTHER_NCA_ID);

        assertEquals(2, clusters.size());
        verifyNoInteractions(clusterRepository);
    }

    @Test
    void removeGatedClustersFromGpuUsage_whenNcaIsNotGated_keepsEverything() {
        ClusterGatingService gatingService = gatingForAllowedCluster();
        List<GpuUsageResponse.Gpu> gpus = usage(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingService.removeGatedClustersFromGpuUsage(gpus, OTHER_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), usageClusterIds(gpus));
        verifyNoInteractions(clusterRepository);
    }

    @Test
    void removeGatedDestinations_whenNcaIsNotGated_keepsEverything() {
        ClusterGatingService gatingService = gatingForAllowedCluster();
        Set<RequestInstanceDestination> destinations = new HashSet<>(Set.of(
                destination(CLUSTER_ALLOWED, OTHER_NCA_ID),
                destination(CLUSTER_DENIED, OTHER_NCA_ID)));

        SpotInstanceRequestSchema instanceRequest = request();
        instanceRequest.setNcaId(OTHER_NCA_ID);
        gatingService.removeGatedDestinations(destinations, instanceRequest);

        assertEquals(2, destinations.size());
        verifyNoInteractions(clusterRepository);
    }

    /**
     * An explicitly empty {@code icms.cluster-gating} is the default every existing deployment
     * runs with, so it has to leave every surface alone.
     */
    @Test
    void removeGatedClustersFromAccountInfo_whenGatingMapIsEmpty_keepsEverything() {
        ClusterGatingService gatingService = gatingFor(new IcmsConfigurationProperties());
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingService.removeGatedClustersFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), availabilityClusterIds(result));
        verifyNoInteractions(clusterRepository);
    }

    /**
     * A null map is reachable through a remote config refresh that declares the key with no
     * value, and must be read as ungated rather than as "nothing allowed".
     */
    @Test
    void removeGatedClustersFromAccountInfo_whenGatingMapIsNull_keepsEverything() {
        IcmsConfigurationProperties properties = new IcmsConfigurationProperties();
        properties.setClusterGating(null);
        ClusterGatingService gatingService = gatingFor(properties);
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingService.removeGatedClustersFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), availabilityClusterIds(result));
        verifyNoInteractions(clusterRepository);
    }

    /**
     * An NCA ID configured with no clusters is a truncated config, not a request to deny
     * everything, so it stays ungated.
     */
    @Test
    void removeGatedClustersFromAccountInfo_whenEntryHasNoClusters_keepsEverything() {
        ClusterGatingService gatingService = gatingFor(GATED_NCA_ID, List.of());
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingService.removeGatedClustersFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), availabilityClusterIds(result));
        verifyNoInteractions(clusterRepository);
    }

    /**
     * Nulls must stay null rather than being normalised into empty collections, otherwise an
     * ungated response changes shape. The no-arg constructor seeds an empty set, so the null has
     * to be set explicitly to reach the guard.
     */
    @Test
    void removeGatedClustersFromAccountInfo_whenGpusAreNull_leavesResponseAlone() {
        ClusterGatingService gatingService = gatingForAllowedCluster();
        InstanceTypeAvailabilityResponse result = new InstanceTypeAvailabilityResponse();
        result.setGpus(null);

        gatingService.removeGatedClustersFromAccountInfo(result, GATED_NCA_ID);

        assertNull(result.getGpus());
        verifyNoInteractions(clusterRepository);
    }

    @Test
    void removeGatedClustersFromGpuUsage_whenGpusAreNull_doesNotThrow() {
        gatingForAllowedCluster().removeGatedClustersFromGpuUsage(null, GATED_NCA_ID);
    }

    // ---------------------------------------------------------------------------------------
    // Gated behaviour.
    // ---------------------------------------------------------------------------------------

    @Test
    void removeGatedClustersFromAccountInfo_whenNcaIsGated_keepsOnlyAllowedClusters() {
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedClustersFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED), availabilityClusterIds(result));
    }

    /**
     * A region, instance type and GPU left with nothing must disappear rather than be returned
     * empty, so the response never advertises capacity the org cannot reach.
     */
    @Test
    void removeGatedClustersFromAccountInfo_whenEveryClusterIsGatedOut_dropsEmptyParents() {
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedClustersFromAccountInfo(result, GATED_NCA_ID);

        assertTrue(result.getGpus().isEmpty());
    }

    /**
     * The org's own clusters are exempt. This response carries no per-cluster owner, so the
     * exemption has to come from the owned-cluster lookup.
     */
    @Test
    void removeGatedClustersFromAccountInfo_whenClusterIsOwnedByCaller_keepsIt() {
        ClusterGatingService gatingService =
                gatingFor(GATED_NCA_ID, List.of(CLUSTER_ALLOWED), CLUSTER_OWNED);
        InstanceTypeAvailabilityResponse result =
                availability(CLUSTER_ALLOWED, CLUSTER_DENIED, CLUSTER_OWNED);

        gatingService.removeGatedClustersFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_OWNED), availabilityClusterIds(result));
    }

    @Test
    void removeGatedClusterGroups_whenNcaIsGated_keepsOnlyAllowedClusters() {
        Set<ClusterGroups> groups = new LinkedHashSet<>(Set.of(
                clusterGroup("group-1", CLUSTER_ALLOWED, CLUSTER_DENIED)));

        gatingForAllowedCluster().removeGatedClusterGroups(groups, GATED_NCA_ID);

        assertEquals(1, groups.size());
        assertEquals(Set.of(CLUSTER_ALLOWED), groups.iterator().next().getClusters().stream()
                .map(ClustersResponse::getId)
                .collect(Collectors.toSet()));
    }

    @Test
    void removeGatedClusterGroups_whenEveryClusterIsGatedOut_dropsGroup() {
        Set<ClusterGroups> groups = new LinkedHashSet<>(Set.of(
                clusterGroup("group-1", CLUSTER_DENIED),
                clusterGroup("group-2", CLUSTER_ALLOWED)));

        gatingForAllowedCluster().removeGatedClusterGroups(groups, GATED_NCA_ID);

        assertEquals(1, groups.size());
        assertEquals("group-2", groups.iterator().next().getName());
    }

    /**
     * A group that never listed clusters is not something cluster gating can judge, so it has to
     * survive. Dropping it would hide groups for reasons unrelated to the allow list.
     */
    @Test
    void removeGatedClusterGroups_whenGroupHasNoClusters_keepsGroup() {
        Set<ClusterGroups> groups = new LinkedHashSet<>(Set.of(clusterGroup("group-1")));

        gatingForAllowedCluster().removeGatedClusterGroups(groups, GATED_NCA_ID);

        assertEquals(1, groups.size());
    }

    @Test
    void removeGatedAuthorizedClusters_whenNcaIsGated_removesGatedClusters() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster(CLUSTER_ALLOWED, OTHER_NCA_ID), cluster(CLUSTER_DENIED, OTHER_NCA_ID)));

        gatingForAllowedCluster().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals(CLUSTER_ALLOWED, clusters.getFirst().getClusterId());
    }

    /**
     * Gating controls what an org may borrow from others, so a cluster it registered itself stays
     * visible. The response carries the owner, so this must not need the repository at all.
     */
    @Test
    void removeGatedAuthorizedClusters_whenClusterIsOwnedByCaller_keepsItWithoutLookup() {
        ClusterGatingService gatingService = gatingForAllowedCluster();
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster(CLUSTER_OWNED, GATED_NCA_ID)));

        gatingService.removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        verifyNoInteractions(clusterRepository);
    }

    @Test
    void removeGatedClustersFromGpuUsage_whenNcaIsGated_keepsOnlyAllowedClusters() {
        List<GpuUsageResponse.Gpu> gpus = usage(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedClustersFromGpuUsage(gpus, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED), usageClusterIds(gpus));
    }

    @Test
    void removeGatedClustersFromGpuUsage_whenEveryClusterIsGatedOut_dropsEmptyParents() {
        List<GpuUsageResponse.Gpu> gpus = usage(CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedClustersFromGpuUsage(gpus, GATED_NCA_ID);

        assertTrue(gpus.isEmpty());
    }

    @Test
    void removeGatedDestinations_whenNcaIsGated_removesGatedDestinations() {
        Set<RequestInstanceDestination> destinations = new HashSet<>(Set.of(
                destination(CLUSTER_ALLOWED, OTHER_NCA_ID),
                destination(CLUSTER_DENIED, OTHER_NCA_ID)));

        gatingForAllowedCluster().removeGatedDestinations(destinations, request());

        assertEquals(1, destinations.size());
        assertEquals(CLUSTER_ALLOWED, destinations.iterator().next().getClusterId());
    }

    @Test
    void removeGatedDestinations_whenDestinationIsOwnedByCaller_keepsItWithoutLookup() {
        ClusterGatingService gatingService = gatingForAllowedCluster();
        Set<RequestInstanceDestination> destinations = new HashSet<>(Set.of(
                destination(CLUSTER_OWNED, GATED_NCA_ID)));

        gatingService.removeGatedDestinations(destinations, request());

        assertEquals(1, destinations.size());
        verifyNoInteractions(clusterRepository);
    }

    /**
     * Unlike the read paths, an empty destination set is an error: the caller asked for capacity
     * in clusters it may not use, so launching nothing silently would look like a capacity
     * shortage instead of a gating decision.
     */
    @Test
    void removeGatedDestinations_whenEveryDestinationIsGatedOut_throwsConflict() {
        IcmsConfigurationProperties properties = new IcmsConfigurationProperties();
        properties.setClusterGating(new HashMap<>(Map.of(GATED_NCA_ID, List.of(CLUSTER_ALLOWED))));

        UnifiedErrorReporter unifiedErrorReporter = mock(UnifiedErrorReporter.class);
        doAnswer(invocation -> {
            throw (IcmsHttpUnifiedErrorException) invocation.getArgument(0);
        }).when(unifiedErrorReporter).reportAndThrow(any(IcmsHttpUnifiedErrorException.class));

        ClusterGatingService gatingService = new ClusterGatingService(
                properties, unifiedErrorReporter, mock(ClusterRepository.class));

        Set<RequestInstanceDestination> destinations = new HashSet<>(Set.of(
                destination(CLUSTER_DENIED, OTHER_NCA_ID)));

        IcmsHttpUnifiedErrorException exception = assertThrows(
                IcmsHttpUnifiedErrorException.class,
                () -> gatingService.removeGatedDestinations(destinations, request()));

        assertEquals(IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS,
                     exception.unifiedError());
    }
}
