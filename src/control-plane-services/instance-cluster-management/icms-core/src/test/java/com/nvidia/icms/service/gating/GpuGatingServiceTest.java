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
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.GpuResponseSchema;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.InstanceTypeResponseSchema;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
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
 * Covers the cluster-listing and GPU-usage read paths. The account-info, cluster-group and
 * create-instance paths are covered by the tests of the services that own them.
 */
class GpuGatingServiceTest {

    private static final String GATED_NCA_ID = "gated-nca-id";
    private static final String OTHER_NCA_ID = "other-nca-id";
    private static final String OWNER_NCA_ID = "owner-nca-id";

    private static final String GPU_ALLOWED = "L40";
    private static final String GPU_DENIED = "B200";
    private static final String INSTANCE_TYPE_ALLOWED = "DGX-CLOUD.GPU.L40_1x";
    private static final String INSTANCE_TYPE_DENIED = "DGX-CLOUD.GPU.L40_8x";

    private static GpuGatingService gatingFor(String ncaId,
                                              Map<String, List<String>> allowedInstanceTypesByGpu) {
        IcmsConfigurationProperties properties = new IcmsConfigurationProperties();
        properties.setGpuGating(new HashMap<>(Map.of(ncaId, allowedInstanceTypesByGpu)));
        return new GpuGatingService(properties, null);
    }

    private static GpuGatingService gatingForAllowedTypes() {
        return gatingFor(GATED_NCA_ID, Map.of(GPU_ALLOWED, List.of(INSTANCE_TYPE_ALLOWED)));
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

    @Test
    void removeGatedAuthorizedClusters_whenNcaIsNotGated_keepsEverything() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID,
                        gpu(GPU_ALLOWED, INSTANCE_TYPE_ALLOWED, INSTANCE_TYPE_DENIED),
                        gpu(GPU_DENIED, "some-b200-type"))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, OTHER_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals(Set.of(GPU_ALLOWED, GPU_DENIED), gpuNames(clusters.getFirst()));
    }

    @Test
    void removeGatedAuthorizedClusters_whenNcaIsGated_removesDeniedGpusAndInstanceTypes() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID,
                        gpu(GPU_ALLOWED, INSTANCE_TYPE_ALLOWED, INSTANCE_TYPE_DENIED),
                        gpu(GPU_DENIED, "some-b200-type"))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals(Set.of(GPU_ALLOWED), gpuNames(clusters.getFirst()));
        assertEquals(Set.of(INSTANCE_TYPE_ALLOWED),
                     instanceTypeNames(clusters.getFirst(), GPU_ALLOWED));
    }

    /**
     * A cluster offering nothing the NCA may allocate must disappear rather than be returned with
     * an empty GPU list, matching how cluster-group gating already behaves.
     */
    @Test
    void removeGatedAuthorizedClusters_whenEveryGpuIsGatedOut_dropsCluster() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID, gpu(GPU_DENIED, "some-b200-type")),
                cluster("cluster-2", OTHER_NCA_ID, gpu(GPU_ALLOWED, INSTANCE_TYPE_ALLOWED))));

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
                cluster("cluster-1", OTHER_NCA_ID, gpu(GPU_ALLOWED, INSTANCE_TYPE_DENIED))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertTrue(clusters.isEmpty());
    }

    /**
     * Gating governs what an org may borrow from others. Hiding a cluster the org registered
     * itself would leave it unable to see or manage its own cluster.
     */
    @Test
    void removeGatedAuthorizedClusters_whenClusterIsOwnedByCaller_keepsItUnfiltered() {
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("own-cluster", GATED_NCA_ID, gpu(GPU_DENIED, "some-b200-type"))));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(1, clusters.size());
        assertEquals(Set.of(GPU_DENIED), gpuNames(clusters.getFirst()));
    }

    @Test
    void removeGatedAuthorizedClusters_removesCapacityEntriesForGatedGpus() {
        GetClusterResponse borrowed = cluster("cluster-1", OTHER_NCA_ID,
                                              gpu(GPU_ALLOWED, INSTANCE_TYPE_ALLOWED));
        borrowed.setGpuUsage(new HashMap<>(Map.of(
                GPU_ALLOWED, GpuCapacity.builder().capacity(8).build(),
                GPU_DENIED, GpuCapacity.builder().capacity(4).build())));
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(borrowed));

        gatingForAllowedTypes().removeGatedAuthorizedClusters(clusters, GATED_NCA_ID);

        assertEquals(Set.of(GPU_ALLOWED), clusters.getFirst().getGpuUsage().keySet());
    }

    @Test
    void removeGatedAuthorizedClusters_whenGpuListedWithNoInstanceTypes_dropsCluster() {
        GpuGatingService gatingService = gatingFor(GATED_NCA_ID, Map.of(GPU_ALLOWED, List.of()));
        List<GetClusterResponse> clusters = new ArrayList<>(List.of(
                cluster("cluster-1", OTHER_NCA_ID, gpu(GPU_ALLOWED, INSTANCE_TYPE_ALLOWED))));

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
    void removeGatedGpusFromGpuUsage_whenNcaIsNotGated_keepsEverything() {
        List<GpuUsageResponse.Gpu> gpus = new ArrayList<>(List.of(
                usageGpu(GPU_ALLOWED, INSTANCE_TYPE_ALLOWED, INSTANCE_TYPE_DENIED),
                usageGpu(GPU_DENIED, "some-b200-type")));

        gatingForAllowedTypes().removeGatedGpusFromGpuUsage(gpus, OTHER_NCA_ID);

        assertEquals(2, gpus.size());
    }

    @Test
    void removeGatedGpusFromGpuUsage_whenNcaIsGated_removesDeniedGpusAndInstanceTypes() {
        List<GpuUsageResponse.Gpu> gpus = new ArrayList<>(List.of(
                usageGpu(GPU_ALLOWED, INSTANCE_TYPE_ALLOWED, INSTANCE_TYPE_DENIED),
                usageGpu(GPU_DENIED, "some-b200-type")));

        gatingForAllowedTypes().removeGatedGpusFromGpuUsage(gpus, GATED_NCA_ID);

        assertEquals(1, gpus.size());
        assertEquals(GPU_ALLOWED, gpus.getFirst().getGpuName());
        assertEquals(List.of(INSTANCE_TYPE_ALLOWED),
                     gpus.getFirst().getInstances().stream()
                             .map(GpuUsageResponse.Instance::getInstanceName)
                             .toList());
    }

    @Test
    void removeGatedGpusFromGpuUsage_whenAllInstancesOfGpuGatedOut_dropsGpu() {
        List<GpuUsageResponse.Gpu> gpus = new ArrayList<>(List.of(
                usageGpu(GPU_ALLOWED, INSTANCE_TYPE_DENIED)));

        gatingForAllowedTypes().removeGatedGpusFromGpuUsage(gpus, GATED_NCA_ID);

        assertTrue(gpus.isEmpty());
    }

    @Test
    void removeGatedGpusFromGpuUsage_whenGpusAreNull_doesNotThrow() {
        gatingForAllowedTypes().removeGatedGpusFromGpuUsage(null, GATED_NCA_ID);
    }
}
