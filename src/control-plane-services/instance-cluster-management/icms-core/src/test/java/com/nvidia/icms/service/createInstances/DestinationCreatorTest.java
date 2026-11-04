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

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_REGION;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.account.GpuUsageFilter;
import com.nvidia.icms.service.byoc.ByocValidationService;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.List;
import java.util.Map;
import java.util.Set;

@ExtendWith(MockitoExtension.class)
class DestinationCreatorTest extends CreateInstancesTestBase {

    private static final String CUSTOM_INSTANCE_TYPE_VALUE = "custom-value";
    private static final String CLUSTER_GROUP = "cluster-group";
    private static final String NCA_1 = "nca-1";
    private static final String NCA_2 = "nca-2";
    private static final String NCA_3 = "nca-3";

    @Mock private ByocValidationService byocValidationService;
    @Mock private IcmsConfigurationProperties icmsConfigurationProperties;
    @Mock private ByocConfigurationProperties byocConfigurationProperties;

    private DestinationCreator creator;
    private ClusterByGroupIdAndIdEntity clusterEntity;

    @BeforeEach
    void setUp() {
        creator = new DestinationCreator(byocValidationService, icmsConfigurationProperties, byocConfigurationProperties, new ComputePlatformService(List.of()));
        clusterEntity = createClusterEntity();
    }

    // -------------------------------------------------------------------------
    // createDestination — targeted (ClusterByGroupIdAndIdEntity source)
    // -------------------------------------------------------------------------

    @Test
    void createDestination_targeted_withValidInputs_createsCorrectDestination() {
        clusterEntity.setClusterGroupName(DUMMY_BYOC_CLUSTER_NAME);
        clusterEntity.setNcaId(DUMMY_BYOC_NCA_ID);
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();
        instanceType.setGpuCount(1);
        when(byocValidationService.getRequiredGpuCountForInstance(1)).thenReturn(1);

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertNotNull(dest);
        assertEquals(DUMMY_CLUSTER_GROUP_ID, dest.getClusterGroupId());
        assertEquals(DUMMY_BYOC_CLUSTER_NAME, dest.getClusterGroupName());
        assertEquals(DUMMY_NON_BYOC_INSTANCE_TYPE, dest.getInstanceType().getName());
        assertEquals(1, dest.getInstanceType().getGpuCount());
        assertEquals(DUMMY_BYOC_NCA_ID, dest.getNcaId());
        assertEquals(CloudProvider.AWS, dest.getCloudProvider());
        assertEquals(DUMMY_BYOC_CLUSTER_NAME, dest.getClusterName());
        assertEquals(DUMMY_CLUSTER_ID, dest.getClusterId());
        assertEquals(DUMMY_REGION, dest.getRegion());
        assertEquals(DUMMY_GPU, dest.getGpuName());
        assertFalse(dest.isReserved());
        verify(byocValidationService).getRequiredGpuCountForInstance(1);
    }

    @Test
    void createDestination_targeted_withCustomGpuCount_appliesRequiredGpuCount() {
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();
        instanceType.setGpuCount(2);
        when(byocValidationService.getRequiredGpuCountForInstance(2)).thenReturn(4);

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals(4, dest.getInstanceType().getGpuCount());
        verify(byocValidationService).getRequiredGpuCountForInstance(2);
    }

    @Test
    void createDestination_targeted_withCustomInstanceTypeValue_preservesValue() {
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();
        instanceType.setValue(CUSTOM_INSTANCE_TYPE_VALUE);

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals(CUSTOM_INSTANCE_TYPE_VALUE, dest.getInstanceType().getValue());
    }

    @Test
    void createDestination_targeted_withAuthorizedNcaIds_preservesNcaIds() {
        Set<String> authorizedNcaIds = Set.of(DUMMY_BYOC_NCA_ID, "another-nca-id");
        clusterEntity.setAuthorizedNcaIds(authorizedNcaIds);
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals(authorizedNcaIds, dest.getAuthorizedNcaIds());
    }

    @Test
    void createDestination_targeted_whenValidationServiceReturnsZeroGpuCount_setsZeroOnDestination() {
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();
        instanceType.setGpuCount(1);
        when(byocValidationService.getRequiredGpuCountForInstance(1)).thenReturn(0);

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals(0, dest.getInstanceType().getGpuCount());
    }

    @Test
    void createDestination_targeted_withEmptyClusterName_preservesEmptyClusterName() {
        clusterEntity.setClusterName("");
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals("", dest.getClusterName());
    }

    @Test
    void createDestination_targeted_withGcpProvider_setsCorrectProvider() {
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.GCP, DUMMY_GPU, "queueUrl", 1);

        assertEquals(CloudProvider.GCP, dest.getCloudProvider());
    }

    @Test
    void createDestination_targeted_withCustomClusterName_preservesName() {
        clusterEntity.setClusterName("custom-cluster");
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals("custom-cluster", dest.getClusterName());
    }

    @Test
    void createDestination_targeted_withCustomRegion_preservesRegion() {
        clusterEntity.setRegion("custom-region");
        InstanceTypeV5Udt instanceType = createInstanceTypeV5();

        DestinationClusterData data = new DestinationClusterData(clusterEntity, Map.of(DUMMY_GPU, "queueUrl"));
        RequestInstanceDestination dest = creator.createDestination(
                data, instanceType, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals("custom-region", dest.getRegion());
    }

    // -------------------------------------------------------------------------
    // createDestination — non-targeted (ClustersByAuthorizedAccountsEntity source)
    // -------------------------------------------------------------------------

    @Test
    void createDestination_nonTargeted_withValidInputs_createsCorrectDestination() {
        ClustersByAuthorizedAccountsEntity entity =
                createClusterForByoc(DUMMY_BYOC_NCA_ID, CLUSTER_GROUP, CLUSTER_GROUP, DUMMY_CLUSTER_ID);
        InstanceTypeUdt instanceType = createInstanceTypeUdt();
        DestinationClusterData data = new DestinationClusterData(entity, Map.of(DUMMY_GPU, "queueUrl"));
        InstanceTypeV5Udt instanceTypeV5 = NvcaConverter.toInstanceTypeV5(instanceType);

        when(byocValidationService.getRequiredGpuCountForInstance(1)).thenReturn(1);

        RequestInstanceDestination dest = creator.createDestination(
                data, instanceTypeV5, CloudProvider.AWS, DUMMY_GPU_NAME, "queueUrl", 1);

        assertNotNull(dest);
        assertEquals(CLUSTER_GROUP, dest.getClusterGroupId());
        assertEquals(CLUSTER_GROUP, dest.getClusterGroupName());
        assertEquals(DUMMY_BYOC_NCA_ID, dest.getNcaId());
        assertEquals(CloudProvider.AWS, dest.getCloudProvider());
        assertEquals(DUMMY_GPU_NAME, dest.getGpuName());
        assertFalse(dest.isReserved());
    }

    @Test
    void createDestination_nonTargeted_withCustomGpuCount_appliesRequiredGpuCount() {
        ClustersByAuthorizedAccountsEntity entity =
                createClusterForByoc(DUMMY_BYOC_NCA_ID, CLUSTER_GROUP, CLUSTER_GROUP, DUMMY_CLUSTER_ID);
        InstanceTypeUdt instanceType = createInstanceTypeWithCustomGpuCount(4);
        DestinationClusterData data = new DestinationClusterData(entity, Map.of(DUMMY_GPU, "queueUrl"));
        InstanceTypeV5Udt instanceTypeV5 = NvcaConverter.toInstanceTypeV5(instanceType);

        when(byocValidationService.getRequiredGpuCountForInstance(4)).thenReturn(2);

        RequestInstanceDestination dest = creator.createDestination(
                data, instanceTypeV5, CloudProvider.AWS, DUMMY_GPU_NAME, "queueUrl", 1);

        assertEquals(2, dest.getInstanceType().getGpuCount());
        verify(byocValidationService).getRequiredGpuCountForInstance(4);
    }

    @Test
    void createDestination_nonTargeted_withCustomInstanceTypeValue_preservesValue() {
        ClustersByAuthorizedAccountsEntity entity =
                createClusterForByoc(DUMMY_BYOC_NCA_ID, CLUSTER_GROUP, CLUSTER_GROUP, DUMMY_CLUSTER_ID);
        InstanceTypeUdt instanceType = createInstanceTypeWithCustomValue(CUSTOM_INSTANCE_TYPE_VALUE);
        DestinationClusterData data = new DestinationClusterData(entity, Map.of(DUMMY_GPU, "queueUrl"));
        InstanceTypeV5Udt instanceTypeV5 = NvcaConverter.toInstanceTypeV5(instanceType);

        RequestInstanceDestination dest = creator.createDestination(
                data, instanceTypeV5, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals(CUSTOM_INSTANCE_TYPE_VALUE, dest.getInstanceType().getValue());
    }

    @Test
    void createDestination_nonTargeted_withAuthorizedNcaIds_preservesNcaIds() {
        ClustersByAuthorizedAccountsEntity entity =
                createClusterForByoc(DUMMY_BYOC_NCA_ID, CLUSTER_GROUP, CLUSTER_GROUP, DUMMY_CLUSTER_ID);
        entity.setAuthorizedNcaIds(Set.of(NCA_1, NCA_2, NCA_3));
        DestinationClusterData data = new DestinationClusterData(entity, Map.of(DUMMY_GPU, "queueUrl"));
        InstanceTypeV5Udt instanceTypeV5 = createInstanceTypeV5();

        RequestInstanceDestination dest = creator.createDestination(
                data, instanceTypeV5, CloudProvider.AWS, DUMMY_GPU, "queueUrl", 1);

        assertEquals(Set.of(NCA_1, NCA_2, NCA_3), dest.getAuthorizedNcaIds());
    }

    @Test
    void createDestination_nonTargeted_withGcpProvider_setsCorrectProvider() {
        ClustersByAuthorizedAccountsEntity entity =
                createClusterForByoc(DUMMY_BYOC_NCA_ID, CLUSTER_GROUP, CLUSTER_GROUP, DUMMY_CLUSTER_ID);
        DestinationClusterData data = new DestinationClusterData(entity, Map.of(DUMMY_GPU, "queueUrl"));
        InstanceTypeV5Udt instanceTypeV5 = createInstanceTypeV5();

        RequestInstanceDestination dest = creator.createDestination(
                data, instanceTypeV5, CloudProvider.GCP, DUMMY_GPU, "queueUrl", 1);

        assertEquals(CloudProvider.GCP, dest.getCloudProvider());
    }

    @Test
    void createDestination_nonTargeted_withAzureProvider_setsCorrectProvider() {
        ClustersByAuthorizedAccountsEntity entity =
                createClusterForByoc(DUMMY_BYOC_NCA_ID, CLUSTER_GROUP, CLUSTER_GROUP, DUMMY_CLUSTER_ID);
        DestinationClusterData data = new DestinationClusterData(entity, Map.of(DUMMY_GPU, "queueUrl"));
        InstanceTypeV5Udt instanceTypeV5 = createInstanceTypeV5();

        RequestInstanceDestination dest = creator.createDestination(
                data, instanceTypeV5, CloudProvider.AZURE, DUMMY_GPU, "queueUrl", 1);

        assertEquals(CloudProvider.AZURE, dest.getCloudProvider());
    }

    // -------------------------------------------------------------------------
    // addAvailableDestinations
    // -------------------------------------------------------------------------

    @Test
    void addAvailableDestinations_withMatchingGpuAndInstanceType_returnsDestination() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of(DUMMY_GPU, "queueUrl"));
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();
        when(byocValidationService.getRequiredGpuCountForInstance(1)).thenReturn(1);

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertEquals(1, result.size());
        assertEquals(DUMMY_GPU, result.get(0).getGpuName());
    }

    @Test
    void addAvailableDestinations_whenCloudProviderIsNull_returnsEmpty() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of(DUMMY_GPU, "queueUrl"));
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, null, 1);

        assertTrue(result.isEmpty());
    }

    @Test
    void addAvailableDestinations_whenGpuNameNotAllowed_returnsEmpty() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of(DUMMY_GPU, "queueUrl"));
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of("different-gpu"))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertTrue(result.isEmpty());
    }

    @Test
    void addAvailableDestinations_whenQueueUrlMissing_skipsDestinationAndReturnsEmpty() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        // Queue map does not contain an entry for DUMMY_GPU — get() returns null
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of());
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertTrue(result.isEmpty());
    }

    @Test
    void addAvailableDestinations_whenInstanceTypeNotAllowed_returnsEmpty() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of(DUMMY_GPU, "queueUrl"));
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of("different-instance-type"))
                .build();

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertTrue(result.isEmpty());
    }

    @Test
    void addAvailableDestinations_whenQueueUrlMapIsEmpty_returnsEmpty() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        // Explicitly empty map — no GPU has a queue URL registered
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of());
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertTrue(result.isEmpty());
    }

    @Test
    void addAvailableDestinations_whenClusterGroupNameNotAllowed_returnsEmpty() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        cluster.setClusterGroupName("byoc-group");
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of(DUMMY_GPU, "queueUrl"));
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .clusterGroupNames(Set.of("different-group"))
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertTrue(result.isEmpty());
    }

    @Test
    void addAvailableDestinations_whenGpusV5IsNull_returnsEmpty() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        cluster.setGpusV5(null);
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of());
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(Set.of(DUMMY_GPU))
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertTrue(result.isEmpty());
    }

    @Test
    void addAvailableDestinations_withMultipleMatchingGpus_returnsOneDestinationPerGpu() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        GpuV5Udt gpu2 = new GpuV5Udt();
        gpu2.setName("gpu-2");
        gpu2.setInstanceTypes(Set.of(createInstanceTypeV5()));
        cluster.setGpusV5(Set.of(createGpuV5(), gpu2));

        // Both GPUs have queue URLs; filter allows both
        DestinationClusterData data = new DestinationClusterData(
                cluster, Map.of(DUMMY_GPU, "queue-1", "gpu-2", "queue-2"));
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .instanceTypes(Set.of(DUMMY_NON_BYOC_INSTANCE_TYPE))
                .build();
        when(byocValidationService.getRequiredGpuCountForInstance(1)).thenReturn(1);

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertEquals(2, result.size());
    }

    @Test
    void addAvailableDestinations_withEmptyFilter_returnsDestination() {
        ClusterByGroupIdAndIdEntity cluster = createClusterEntity();
        DestinationClusterData data = new DestinationClusterData(cluster, Map.of(DUMMY_GPU, "queueUrl"));
        // No GPU names or instance types specified — filter imposes no restriction
        GpuUsageFilter filter = GpuUsageFilter.builder().build();
        when(byocValidationService.getRequiredGpuCountForInstance(1)).thenReturn(1);

        List<RequestInstanceDestination> result =
                creator.addAvailableDestinations(data, filter, CloudProvider.AWS, 1);

        assertEquals(1, result.size());
        assertEquals(DUMMY_GPU, result.get(0).getGpuName());
    }
}
