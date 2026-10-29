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

import com.nvidia.icms.configuration.bean.CapacityGatingProperties;
import com.nvidia.icms.configuration.bean.CapacityGatingProperties.GatingPolicy;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.ClustersResponse;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.GpuResponseSchema;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.InstanceTypeResponseSchema;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
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
 * Covers {@code icms.capacity-gating} on all five surfaces it is wired into. The opening block of
 * no-op tests is the backward-compatibility contract: an account without a gating entry must come
 * back untouched and must not trigger the owned-cluster lookup.
 */
class CapacityGatingServiceTest {

    private static final String GATED_NCA_ID = "gated-nca-id";
    private static final String OTHER_NCA_ID = "other-nca-id";

    private static final String CLUSTER_ALLOWED = "cluster-allowed";
    private static final String CLUSTER_DENIED = "cluster-denied";
    private static final String CLUSTER_OWNED = "cluster-owned";

    private static final String GPU = "L40";
    private static final String GPU_DENIED = "B200";
    private static final String INSTANCE_TYPE = "DGX-CLOUD.GPU.L40_1x";
    private static final String INSTANCE_TYPE_DENIED = "DGX-CLOUD.GPU.L40_8x";
    private static final String REGION = "us-west-2";

    private ClusterRepository clusterRepository;

    /**
     * Builds a service applying {@code policy} to {@code ncaId}, with the repository reporting
     * {@code ownedClusterIds} as clusters the caller registered itself.
     */
    private CapacityGatingService gatingFor(String ncaId, GatingPolicy policy,
                                            String... ownedClusterIds) {
        CapacityGatingProperties properties = new CapacityGatingProperties();
        Map<String, GatingPolicy> entries = new HashMap<>();
        entries.put(ncaId, policy);
        properties.setCapacityGating(entries);
        return gatingFor(properties, ownedClusterIds);
    }

    private CapacityGatingService gatingFor(CapacityGatingProperties properties,
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
        return new CapacityGatingService(properties, null, clusterRepository);
    }

    /** Builds a service whose reporter rethrows, so the error it chose is observable. */
    private CapacityGatingService gatingWithReporter(CapacityGatingProperties properties) {
        return new CapacityGatingService(
                properties, rethrowingReporter(), mock(ClusterRepository.class));
    }



    /** Gates clusters only, leaving every GPU and instance type available. */
    private CapacityGatingService gatingForAllowedCluster(String... ownedClusterIds) {
        return gatingFor(GATED_NCA_ID,
                         GatingPolicy.builder().clusters(List.of(CLUSTER_ALLOWED)).build(),
                         ownedClusterIds);
    }

    /** Gates GPUs only, leaving every cluster available. */
    private CapacityGatingService gatingForAllowedTypes() {
        return gatingFor(GATED_NCA_ID, GatingPolicy.builder()
                .gpus(Map.of(GPU, List.of(INSTANCE_TYPE)))
                .build());
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
                .gpus(new LinkedHashSet<>(Set.of(ClusterGroups.GpuResponse.builder()
                                                         .name(GPU)
                                                         .instanceTypes(new LinkedHashSet<>(Set.of(
                                                                 ClusterGroups.InstanceTypeResponse
                                                                         .builder()
                                                                         .name(INSTANCE_TYPE)
                                                                         .nodeType(NodeTypeEnum
                                                                                           .SINGLE)
                                                                         .build())))
                                                         .build())))
                .clusters(Arrays.stream(clusterIds)
                                  .map(clusterId -> ClustersResponse.builder()
                                          .id(clusterId)
                                          .name(clusterId)
                                          .build())
                                  .collect(Collectors.toCollection(LinkedHashSet::new)))
                .build();
    }

    private static GetClusterResponse cluster(String clusterId, String ownerNcaId,
                                              GpuResponseSchema... gpus) {
        return GetClusterResponse.builder()
                .clusterId(clusterId)
                .clusterName(clusterId)
                .ncaId(ownerNcaId)
                .gpus(new LinkedHashSet<>(Arrays.asList(gpus)))
                .build();
    }

    private static GpuResponseSchema gpu(String name, String... instanceTypeNames) {
        Set<InstanceTypeResponseSchema> instanceTypes = Arrays.stream(instanceTypeNames)
                .map(instanceTypeName -> InstanceTypeResponseSchema.builder()
                        .name(instanceTypeName)
                        .build())
                .collect(Collectors.toCollection(LinkedHashSet::new));

        return GpuResponseSchema.builder()
                .name(name)
                .capacity(8)
                .instanceTypes(instanceTypes)
                .build();
    }

    private static Set<String> gpuNames(GetClusterResponse cluster) {
        return cluster.getGpus().stream()
                .map(GpuResponseSchema::getName)
                .collect(Collectors.toSet());
    }

    private static Set<String> instanceTypeNames(GetClusterResponse cluster, String gpuName) {
        return cluster.getGpus().stream()
                .filter(gpu -> gpu.getName().equals(gpuName))
                .flatMap(gpu -> gpu.getInstanceTypes().stream())
                .map(InstanceTypeResponseSchema::getName)
                .collect(Collectors.toSet());
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

    /** A usage GPU with no regions, for the GPU-dimension tests that ignore clusters. */
    private static GpuUsageResponse.Gpu usageGpu(String gpuName, String... instanceTypeNames) {
        List<GpuUsageResponse.Instance> instances = Arrays.stream(instanceTypeNames)
                .map(instanceTypeName -> GpuUsageResponse.Instance.builder()
                        .instanceName(instanceTypeName)
                        .regions(new ArrayList<>())
                        .build())
                .collect(Collectors.toCollection(ArrayList::new));

        return GpuUsageResponse.Gpu.builder()
                .gpuName(gpuName)
                .instances(instances)
                .build();
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

    /** A reporter that rethrows, so the error the service chose is observable. */
    private static UnifiedErrorReporter rethrowingReporter() {
        UnifiedErrorReporter unifiedErrorReporter = mock(UnifiedErrorReporter.class);
        doAnswer(invocation -> {
            throw (IcmsHttpUnifiedErrorException) invocation.getArgument(0);
        }).when(unifiedErrorReporter).reportAndThrow(any(IcmsHttpUnifiedErrorException.class));
        return unifiedErrorReporter;
    }

    // ---------------------------------------------------------------------------------------
    // No-op contract: an ungated account must be indistinguishable from the pre-gating service.
    // ---------------------------------------------------------------------------------------

    @Test
    void removeGatedFromAccountInfo_whenNcaIsNotGated_keepsEverything() {
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedFromAccountInfo(result, OTHER_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), availabilityClusterIds(result));
        verifyNoInteractions(clusterRepository);
    }

    /**
     * A null map is reachable through a remote config refresh that declares the key with no
     * value, and must be read as ungated rather than as "nothing allowed".
     */
    @Test
    void removeGatedFromAccountInfo_whenGatingMapIsNull_keepsEverything() {
        CapacityGatingProperties properties = new CapacityGatingProperties();
        properties.setCapacityGating(null);
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingFor(properties).removeGatedFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), availabilityClusterIds(result));
        verifyNoInteractions(clusterRepository);
    }

    /**
     * A policy that only names GPUs says nothing about clusters, so every cluster stays and the
     * owned-cluster lookup is never needed.
     */
    @Test
    void removeGatedFromAccountInfo_whenPolicyOmitsClusters_keepsEveryCluster() {
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingForAllowedTypes().removeGatedFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), availabilityClusterIds(result));
        verifyNoInteractions(clusterRepository);
    }

    /**
     * Nulls must stay null rather than being normalised into empty collections, otherwise an
     * ungated response changes shape. The no-arg constructor seeds an empty set, so the null has
     * to be set explicitly to reach the guard.
     */
    @Test
    void removeGatedFromAccountInfo_whenGpusAreNull_leavesResponseAlone() {
        InstanceTypeAvailabilityResponse result = new InstanceTypeAvailabilityResponse();
        result.setGpus(null);

        gatingForAllowedCluster().removeGatedFromAccountInfo(result, GATED_NCA_ID);

        assertNull(result.getGpus());
        verifyNoInteractions(clusterRepository);
    }

    @Test
    void removeGatedFromGpuUsage_whenGpusAreNull_doesNotThrow() {
        gatingForAllowedCluster().removeGatedFromGpuUsage(null, GATED_NCA_ID);
    }

    @Test
    void removeGatedFromGpuUsage_whenNcaIsNotGated_keepsEverything() {
        List<GpuUsageResponse.Gpu> gpus = new ArrayList<>(List.of(
                usageGpu(GPU, INSTANCE_TYPE, INSTANCE_TYPE_DENIED),
                usageGpu(GPU_DENIED, "some-b200-type")));

        gatingForAllowedTypes().removeGatedFromGpuUsage(gpus, OTHER_NCA_ID);

        assertEquals(2, gpus.size());
    }

    @Test
    void removeGatedAuthorizedClusters_whenNcaIsNotGated_keepsEverything() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID,
                        gpu(GPU, INSTANCE_TYPE, INSTANCE_TYPE_DENIED),
                        gpu(GPU_DENIED, "some-b200-type"))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, OTHER_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals(Set.of(GPU, GPU_DENIED), gpuNames(clusters.getFirst()));
    }

    // ---------------------------------------------------------------------------------------
    // Cluster dimension.
    // ---------------------------------------------------------------------------------------

    @Test
    void removeGatedFromAccountInfo_whenNcaIsGated_keepsOnlyAllowedClusters() {
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED), availabilityClusterIds(result));
    }

    /**
     * A region, instance type and GPU left with nothing must disappear rather than be returned
     * empty, so the response never advertises capacity the org cannot reach.
     */
    @Test
    void removeGatedFromAccountInfo_whenEveryClusterIsGatedOut_dropsEmptyParents() {
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedFromAccountInfo(result, GATED_NCA_ID);

        assertTrue(result.getGpus().isEmpty());
    }

    /**
     * The org's own clusters are exempt. This response carries no per-cluster owner, so the
     * exemption has to come from the owned-cluster lookup.
     */
    @Test
    void removeGatedFromAccountInfo_whenClusterIsOwnedByCaller_keepsIt() {
        InstanceTypeAvailabilityResponse result =
                availability(CLUSTER_ALLOWED, CLUSTER_DENIED, CLUSTER_OWNED);

        gatingForAllowedCluster(CLUSTER_OWNED).removeGatedFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_OWNED), availabilityClusterIds(result));
    }

    /** A wildcard cluster list admits clusters the policy never names. */
    @Test
    void removeGatedFromAccountInfo_whenClusterListIsWildcard_keepsEveryCluster() {
        CapacityGatingService gatingService = gatingFor(
                GATED_NCA_ID, GatingPolicy.builder().clusters(List.of("*")).build());
        InstanceTypeAvailabilityResponse result = availability(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingService.removeGatedFromAccountInfo(result, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED, CLUSTER_DENIED), availabilityClusterIds(result));
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
                cluster(CLUSTER_ALLOWED, OTHER_NCA_ID, gpu(GPU, INSTANCE_TYPE)),
                cluster(CLUSTER_DENIED, OTHER_NCA_ID, gpu(GPU, INSTANCE_TYPE))));

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
        CapacityGatingService gatingService = gatingForAllowedCluster();
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster(CLUSTER_OWNED, GATED_NCA_ID)));

        gatingService.removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        verifyNoInteractions(clusterRepository);
    }

    @Test
    void removeGatedFromGpuUsage_whenNcaIsGated_keepsOnlyAllowedClusters() {
        List<GpuUsageResponse.Gpu> gpus = usage(CLUSTER_ALLOWED, CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedFromGpuUsage(gpus, GATED_NCA_ID);

        assertEquals(Set.of(CLUSTER_ALLOWED), usageClusterIds(gpus));
    }

    @Test
    void removeGatedFromGpuUsage_whenEveryClusterIsGatedOut_dropsEmptyParents() {
        List<GpuUsageResponse.Gpu> gpus = usage(CLUSTER_DENIED);

        gatingForAllowedCluster().removeGatedFromGpuUsage(gpus, GATED_NCA_ID);

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
        CapacityGatingService gatingService = gatingForAllowedCluster();
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
        CapacityGatingProperties properties = new CapacityGatingProperties();
        properties.setCapacityGating(new HashMap<>(Map.of(
                GATED_NCA_ID,
                GatingPolicy.builder().clusters(List.of(CLUSTER_ALLOWED)).build())));

        CapacityGatingService gatingService = gatingWithReporter(properties);

        Set<RequestInstanceDestination> destinations = new HashSet<>(Set.of(
                destination(CLUSTER_DENIED, OTHER_NCA_ID)));

        IcmsHttpUnifiedErrorException exception = assertThrows(
                IcmsHttpUnifiedErrorException.class,
                () -> gatingService.removeGatedDestinations(destinations, request()));

        assertEquals(IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS,
                     exception.unifiedError());
    }

    // ---------------------------------------------------------------------------------------
    // GPU and instance type dimension.
    // ---------------------------------------------------------------------------------------

    @Test
    void removeGatedAuthorizedClusters_whenNcaIsGated_removesDeniedGpusAndInstanceTypes() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID,
                        gpu(GPU, INSTANCE_TYPE, INSTANCE_TYPE_DENIED),
                        gpu(GPU_DENIED, "some-b200-type"))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals(Set.of(GPU), gpuNames(clusters.getFirst()));
        assertEquals(Set.of(INSTANCE_TYPE), instanceTypeNames(clusters.getFirst(), GPU));
    }

    /**
     * A cluster offering nothing the NCA may allocate must disappear rather than be returned with
     * an empty GPU list, matching how cluster-group gating already behaves.
     */
    @Test
    void removeGatedAuthorizedClusters_whenEveryGpuIsGatedOut_dropsCluster() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID, gpu(GPU_DENIED, "some-b200-type")),
                cluster("cluster-2", OTHER_NCA_ID, gpu(GPU, INSTANCE_TYPE))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals("cluster-2", clusters.getFirst().getClusterId());
    }

    /**
     * A GPU is allowed but every instance type it offers on this cluster is withheld, so the GPU
     * must not be returned with an empty instance type list.
     */
    @Test
    void removeGatedAuthorizedClusters_whenAllInstanceTypesOfGpuGatedOut_dropsGpu() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID, gpu(GPU, INSTANCE_TYPE_DENIED))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertTrue(clusters.isEmpty());
    }

    /**
     * Gating governs what an org may borrow from others. Hiding a cluster the org registered
     * itself would leave it unable to see or manage its own cluster.
     */
    @Test
    void removeGatedAuthorizedClusters_whenOwnClusterHasOnlyGatedGpus_keepsItUnfiltered() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("own-cluster", GATED_NCA_ID, gpu(GPU_DENIED, "some-b200-type"))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals(Set.of(GPU_DENIED), gpuNames(clusters.getFirst()));
    }

    @Test
    void removeGatedAuthorizedClusters_removesCapacityEntriesForGatedGpus() {
        GetClusterResponse borrowed = cluster("cluster-1", OTHER_NCA_ID, gpu(GPU, INSTANCE_TYPE));
        borrowed.setGpuUsage(new HashMap<>(Map.of(
                GPU, GpuCapacity.builder().capacity(8).build(),
                GPU_DENIED, GpuCapacity.builder().capacity(4).build())));
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(borrowed));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(Set.of(GPU), clusters.getFirst().getGpuUsage().keySet());
    }

    @Test
    void removeGatedAuthorizedClusters_whenAllowedGpuLosesAllInstanceTypes_removesItsCapacity() {
        String b200TypeAllowed = "DGX-CLOUD.GPU.B200_1x";
        String b200TypeWithheld = "DGX-CLOUD.GPU.B200_8x";
        CapacityGatingService gatingService = gatingFor(GATED_NCA_ID, GatingPolicy.builder()
                .gpus(Map.of(GPU, List.of(INSTANCE_TYPE),
                             GPU_DENIED, List.of(b200TypeAllowed)))
                .build());

        GetClusterResponse borrowed = cluster("cluster-1", OTHER_NCA_ID,
                                              gpu(GPU, INSTANCE_TYPE),
                                              gpu(GPU_DENIED, b200TypeWithheld));
        borrowed.setGpuUsage(new HashMap<>(Map.of(
                GPU, GpuCapacity.builder().capacity(8).build(),
                GPU_DENIED, GpuCapacity.builder().capacity(4).build())));
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(borrowed));

        gatingService.removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals(Set.of(GPU), gpuNames(clusters.getFirst()));
        assertEquals(Set.of(GPU), clusters.getFirst().getGpuUsage().keySet());
    }

    @Test
    void removeGatedAuthorizedClusters_whenGpuListedWithNoInstanceTypes_dropsCluster() {
        CapacityGatingService gatingService = gatingFor(GATED_NCA_ID, GatingPolicy.builder()
                .gpus(Map.of(GPU, List.of()))
                .build());
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID, gpu(GPU, INSTANCE_TYPE))));

        gatingService.removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertTrue(clusters.isEmpty());
    }

    @Test
    void removeGatedAuthorizedClusters_whenClusterHasNoGpus_dropsCluster() {
        GetClusterResponse empty = GetClusterResponse.builder()
                .clusterId("cluster-1")
                .ncaId(OTHER_NCA_ID)
                .gpus(new HashSet<>())
                .build();
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(empty));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertTrue(clusters.isEmpty());
    }

    @Test
    void removeGatedFromGpuUsage_whenNcaIsGated_removesDeniedGpusAndInstanceTypes() {
        List<GpuUsageResponse.Gpu> gpus = new ArrayList<>(List.of(
                usageGpu(GPU, INSTANCE_TYPE, INSTANCE_TYPE_DENIED),
                usageGpu(GPU_DENIED, "some-b200-type")));

        gatingForAllowedTypes().removeGatedFromGpuUsage(gpus, GATED_NCA_ID);

        assertEquals(1, gpus.size());
        assertEquals(GPU, gpus.getFirst().getGpuName());
        assertEquals(List.of(INSTANCE_TYPE),
                     gpus.getFirst().getInstances().stream()
                             .map(GpuUsageResponse.Instance::getInstanceName)
                             .toList());
    }

    @Test
    void removeGatedFromGpuUsage_whenAllInstancesOfGpuGatedOut_dropsGpu() {
        List<GpuUsageResponse.Gpu> gpus = new ArrayList<>(List.of(
                usageGpu(GPU, INSTANCE_TYPE_DENIED)));

        gatingForAllowedTypes().removeGatedFromGpuUsage(gpus, GATED_NCA_ID);

        assertTrue(gpus.isEmpty());
    }

    // ---------------------------------------------------------------------------------------
    // Error selection and the account kill switch.
    // ---------------------------------------------------------------------------------------

    /**
     * The reported error names the narrowest rule that actually withheld everything, so the
     * response tells the caller which dimension to ask about rather than always blaming clusters.
     */
    @Test
    void removeGatedDestinations_whenGpuIsDenied_reportsGpuError() {
        CapacityGatingProperties properties = new CapacityGatingProperties();
        properties.setCapacityGating(new HashMap<>(Map.of(
                GATED_NCA_ID,
                GatingPolicy.builder().gpus(Map.of(GPU_DENIED, List.of(INSTANCE_TYPE))).build())));
        CapacityGatingService gatingService = gatingWithReporter(properties);

        Set<RequestInstanceDestination> destinations = new HashSet<>(Set.of(
                destination(CLUSTER_ALLOWED, OTHER_NCA_ID)));

        IcmsHttpUnifiedErrorException exception = assertThrows(
                IcmsHttpUnifiedErrorException.class,
                () -> gatingService.removeGatedDestinations(destinations, request()));

        assertEquals(IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_GPU, exception.unifiedError());
    }

    @Test
    void removeGatedDestinations_whenGpuIsAllowedButInstanceTypeIsNot_reportsInstanceTypeError() {
        CapacityGatingProperties properties = new CapacityGatingProperties();
        properties.setCapacityGating(new HashMap<>(Map.of(
                GATED_NCA_ID,
                GatingPolicy.builder().gpus(Map.of(GPU, List.of(INSTANCE_TYPE))).build())));
        CapacityGatingService gatingService = gatingWithReporter(properties);

        // The destination carries no instance type, so it cannot match the allowed one.
        Set<RequestInstanceDestination> destinations = new HashSet<>(Set.of(
                destination(CLUSTER_ALLOWED, OTHER_NCA_ID)));

        IcmsHttpUnifiedErrorException exception = assertThrows(
                IcmsHttpUnifiedErrorException.class,
                () -> gatingService.removeGatedDestinations(destinations, request()));

        assertEquals(IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE,
                     exception.unifiedError());
    }

    /**
     * {@code clusters: []} is the deliberate account kill switch. It has to outrank the ownership
     * exemption, or an org that registered its own clusters would keep allocating through them
     * after being switched off.
     */
    @Test
    void whenOrgIsDenied_everySurfaceIsEmptiedIncludingOwnClusters() {
        GatingPolicy killSwitch = GatingPolicy.builder()
                .clusters(List.of())
                .gpus(Map.of(GPU, List.of(INSTANCE_TYPE)))
                .build();

        InstanceTypeAvailabilityResponse availability =
                availability(CLUSTER_ALLOWED, CLUSTER_OWNED);
        gatingFor(GATED_NCA_ID, killSwitch, CLUSTER_OWNED)
                .removeGatedFromAccountInfo(availability, GATED_NCA_ID);
        assertTrue(availability.getGpus().isEmpty());

        List<GpuUsageResponse.Gpu> gpuUsage = usage(CLUSTER_ALLOWED, CLUSTER_OWNED);
        gatingFor(GATED_NCA_ID, killSwitch, CLUSTER_OWNED)
                .removeGatedFromGpuUsage(gpuUsage, GATED_NCA_ID);
        assertTrue(gpuUsage.isEmpty());

        Set<ClusterGroups> groups = new LinkedHashSet<>(Set.of(
                clusterGroup("group-1", CLUSTER_ALLOWED, CLUSTER_OWNED)));
        gatingFor(GATED_NCA_ID, killSwitch, CLUSTER_OWNED)
                .removeGatedClusterGroups(groups, GATED_NCA_ID);
        assertTrue(groups.isEmpty());

        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster(CLUSTER_OWNED, GATED_NCA_ID, gpu(GPU, INSTANCE_TYPE))));
        gatingFor(GATED_NCA_ID, killSwitch).removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);
        assertTrue(clusters.isEmpty());
    }


    @Test
    void removeGatedDestinations_whenOrgIsDenied_clearsDestinationsAndThrows() {
        CapacityGatingProperties properties = new CapacityGatingProperties();
        properties.setCapacityGating(new HashMap<>(Map.of(
                GATED_NCA_ID, GatingPolicy.builder().clusters(List.of()).build())));
        CapacityGatingService gatingService = gatingWithReporter(properties);

        Set<RequestInstanceDestination> destinations = new HashSet<>(Set.of(
                destination(CLUSTER_OWNED, GATED_NCA_ID),
                destination(CLUSTER_ALLOWED, OTHER_NCA_ID)));

        IcmsHttpUnifiedErrorException exception = assertThrows(
                IcmsHttpUnifiedErrorException.class,
                () -> gatingService.removeGatedDestinations(destinations, request()));

        assertEquals(IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS,
                     exception.unifiedError());
        assertTrue(destinations.isEmpty());
    }
}
