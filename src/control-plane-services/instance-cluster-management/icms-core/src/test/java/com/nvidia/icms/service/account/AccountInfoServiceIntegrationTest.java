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
package com.nvidia.icms.service.account;

import static com.nvidia.icms.service.platform.ComputePlatformTestFixtures.PLATFORM_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.InstanceTypeDetails;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterRegion;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.util.TestUtil;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

class AccountInfoServiceIntegrationTest extends IntegrationTest {

    @Autowired
    private AccountInfoService accountInfoService;

    @Autowired
    private NvcaClusterRepository nvcaClusterRepository;

    @Autowired
    private CloudHealthRepository cloudHealthRepository;

    @Autowired
    private ClusterTargetingHelper clusterTargetingHelper;

    @Autowired
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Autowired
    private ClusterRepository clusterRepository;

    String dummyGpu3 = "DUMMY_GPU_3";
    String dummyGpu2 = "DUMMY_GPU_2";
    String nonByocInstanceType = "dummy_gpu_3.xlarge";
    String nvcaInstanceType = "AZURE.GPU.DUMMY_GPU_3_1x";

    String nonByocClusterId1 = "nonbyoc-cluster-1";
    String nonByocCluster1Region = ClusterRegion.US_EAST_1.toString();
    String nonByocCluster1Attribute = "nonbyoc-cluster-1-attr";

    String nonByocClusterId2 = "nonbyoc-cluster-2";
    String nonByocCluster2Region = ClusterRegion.EU_EAST_1.toString();
    String nonByocCluster2Attribute = "nonbyoc-cluster-2-attr";

    String nvcaClusterId1 = "nvca-cluster-1";
    String nvcaCluster1Region = ClusterRegion.AP_EAST_1.toString();
    String nvcaCluster1Attribute = "nvca-cluster-1-attr";

    String ncaId = DUMMY_BYOC_NCA_ID;

    @Test
    void getAvailableRegions_getAvailableAttributes_getAllGpusForAccount_withoutReservation_considerFallbackCapacity() {
        // Prepare

        // Creating Non BYOC Entity
        // nonByocClusterId1 contains DUMMY_GPU_3 GPU
        GpuV5Udt nonByocGpu3 = getDummyGpuV5(nonByocInstanceType, dummyGpu3);
        ClusterEntity nonByocEntity1 = getNonByocClusterEntity(nonByocClusterId1, nonByocCluster1Region, Set.of(nonByocGpu3), Set.of(nonByocCluster1Attribute));
        CloudHealthEntity nonByocCluster1HealthEntity = getDummyHealthEntity(nonByocClusterId1, Set.of(dummyGpu3));

        // nonByocClusterId2 contains DUMMY_GPU_3 and DUMMY_GPU_2 GPU
        GpuV5Udt nonByocGpu2 = getDummyGpuV5(nonByocInstanceType, dummyGpu2);
        ClusterEntity nonByocEntity2 = getNonByocClusterEntity(nonByocClusterId2, nonByocCluster2Region, Set.of(nonByocGpu3, nonByocGpu2), Set.of(nonByocCluster2Attribute));
        CloudHealthEntity nonByocCluster2HealthEntity = getDummyHealthEntity(nonByocClusterId2, Set.of(dummyGpu3, dummyGpu2));

        // Creating NVCA Entity
        GpuV5Udt nvcaGpu3 = getDummyGpuV5(nvcaInstanceType, dummyGpu3);
        ClusterEntity nvcaEntity1 = getClusterEntity(nvcaClusterId1, nvcaCluster1Region, ncaId,
                                                     Set.of(nvcaGpu3), DUMMY_BYOC_CLUSTER_GROUP_NAME,
                                                     ClusterProviderEnum.AZURE, Set.of("nca_id_1"), Set.of(nvcaCluster1Attribute));
        CloudHealthEntity nvcaCluster1HealthEntity = getDummyHealthEntity(nvcaClusterId1, Set.of(dummyGpu3));


        // Create and insert test data
        saveClusterEntityInDb(Set.of(nonByocEntity1, nonByocEntity2, nvcaEntity1));
        saveHealthEntityInDb(Set.of(nonByocCluster1HealthEntity, nonByocCluster2HealthEntity, nvcaCluster1HealthEntity));

        GpuUsageFilter filter = GpuUsageFilter.builder()
                .instanceTypeUsageFilter(InstanceTypeUsageEnum.DEFAULT)
                .build();

        // ACT
        Map<String, Set<String>> regionResponse = accountInfoService.getAvailableRegions(
                ncaId, filter);

        Map<String, Set<String>> attributeResponse = accountInfoService.getAvailableAttributes(
                ncaId, filter);

        Map<String, Set<String>> gpuResponse = accountInfoService.getAllGpusForAccount(
                ncaId,filter);


        Map<String, Set<InstanceTypeDetails>> instanceTypeResponse = accountInfoService.getAvailableInstanceTypes(
                ncaId, filter);


        /*
        Validating response
        Non BYOC:
         DUMMY_GPU_3:
            reservation status:
                no reservation
            cluster status:
                nonByocClusterId1 will be included for DUMMY_GPU_3 GPU, as it has spot capacity
                nonByocClusterId2 will be included for DUMMY_GPU_3 GPU, as it has spot capacity

          DUMMY_GPU_2:
            reservation status:
              no reservation
           cluster status:
              nonByocClusterId2 will be included as it has DUMMY_GPU_2 GPU

         NVCA:
          DUMMY_GPU_3:
            nvcaClusterId1  will be included as it has DUMMY_GPU_3 GPUs

         Response will contain:
         region and attributes from:
          1. nvcaClusterId1 for DUMMY_GPU_3
          2. nonByocClusterId1 for DUMMY_GPU_3
          3. nonByocClusterId2 for DUMMY_GPU_2
         */
        validateResponse(AccountInfoService.REGIONS_RESPONSE_FIELD_NAME, regionResponse, Set.of(nvcaCluster1Region, nonByocCluster2Region, nonByocCluster1Region), Set.of());
        validateResponse(AccountInfoService.ATTRIBUTES_RESPONSE_FIELD_NAME, attributeResponse, Set.of(nvcaCluster1Attribute, nonByocCluster2Attribute, nonByocCluster1Attribute), Set.of());
        validateResponse(AccountInfoService.GPUS_RESPONSE_FIELD_NAME, gpuResponse, Set.of(dummyGpu3, dummyGpu2), Set.of());
        validateInstanceTypeResponse(instanceTypeResponse, dummyGpu3, Set.of(nvcaInstanceType, nonByocInstanceType), Set.of());
        validateInstanceTypeResponse(instanceTypeResponse, dummyGpu2, Set.of(nonByocInstanceType), Set.of());

        // cleanup
        cleanupClusterEntityFromDb(Set.of(nonByocEntity1, nonByocEntity2, nvcaEntity1));
    }

    @Test
    void getInstanceTypeAvailability_NoCloudHealth_ReturnsEmptyResponse() {
        // Arrange
        // Act
        InstanceTypeAvailabilityResponse response = accountInfoService.getInstanceTypeAvailability(
                ncaId);

        // Assert
        assertNotNull(response);
        assertTrue(response.getGpus().isEmpty());
    }

    @Test
    void getInstanceTypeAvailability_NoGpuUsage_ReturnsEmptyResponse() {
        // Arrange
        CloudHealthEntity cloudHealth = getDummyHealthEntity(nvcaClusterId1, Set.of(dummyGpu3));
        cloudHealth.setGpuUsage(null);

        saveHealthEntityInDb(Set.of(cloudHealth));

        // Act
        InstanceTypeAvailabilityResponse response = accountInfoService.getInstanceTypeAvailability(
                ncaId);

        // Assert
        assertNotNull(response);
        assertTrue(response.getGpus().isEmpty());
    }

    @Test
    void getInstanceTypeAvailability_NoCapacity_ReturnsEmptyResponse() {
        // Arrange
        CloudHealthEntity cloudHealth = getDummyHealthEntity(nvcaClusterId1, Set.of("A100"));
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        GpuCapacity capacity = new GpuCapacity();
        capacity.setAvailable(0);
        capacity.setCapacity(0);
        gpuUsage.put("A100", capacity);
        cloudHealth.setGpuUsage(gpuUsage);

        saveHealthEntityInDb(Set.of(cloudHealth));

        // Act
        InstanceTypeAvailabilityResponse response = accountInfoService.getInstanceTypeAvailability(
                ncaId);

        // Assert
        assertNotNull(response);
        assertTrue(response.getGpus().isEmpty());
    }

    @Test
    void getInstanceTypeAvailability_ZeroCapacity_ReturnsEmptyResponse() {
        // Arrange
        CloudHealthEntity cloudHealth = getDummyHealthEntity(nvcaClusterId1, Set.of("A100"));
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        GpuCapacity capacity = new GpuCapacity();
        capacity.setAvailable(0);
        capacity.setCapacity(0);
        gpuUsage.put("A100", capacity);
        cloudHealth.setGpuUsage(gpuUsage);

        saveHealthEntityInDb(Set.of(cloudHealth));

        // Act
        InstanceTypeAvailabilityResponse response = accountInfoService.getInstanceTypeAvailability(
                ncaId);

        // Assert
        assertNotNull(response);
        assertTrue(response.getGpus().isEmpty());
    }

    @Test
    void getInstanceTypeAvailability_NoInstanceTypes_ReturnsEmptyResponse() {
        // Arrange
        CloudHealthEntity cloudHealth = getDummyHealthEntity(nvcaClusterId1, Set.of("A100"));
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        GpuCapacity capacity = new GpuCapacity();
        capacity.setAvailable(10);
        capacity.setCapacity(10);
        gpuUsage.put("A100", capacity);
        cloudHealth.setGpuUsage(gpuUsage);
        saveHealthEntityInDb(Set.of(cloudHealth));

        saveHealthEntityInDb(Set.of(cloudHealth));

        // Act
        InstanceTypeAvailabilityResponse response = accountInfoService.getInstanceTypeAvailability(
                ncaId);

        // Assert
        assertNotNull(response);
        assertTrue(response.getGpus().isEmpty());
    }

  /*  @Test
   void getInstanceTypeAvailability_WithValidData_ReturnsCorrectResponse() {
        // Given
        String gpuName = "A100";
        String instanceTypeName = "g4dn.xlarge";
        String clusterId = "123e4567-e89b-12d3-a456-426614174000";
        String clusterName = "test-cluster";
        String cloudProvider = "AWS";
        String clusterGroup = "test-group";
        String region = "us-west-2";
        int availableCapacity = 10;
        Set<String> attributes = Set.of("test-attr1", "test-attr2");

        // Create cluster entity
        ClusterByGroupIdAndIdEntity clusterEntity = ClusterByGroupIdAndIdEntity.builder()
                .key(ClusterByGroupIdAndIdKey.builder()
                             .clusterId(clusterId)
                             .clusterGroupId(clusterGroup)
                             .build())
                .clusterName(clusterName)
                .clusterProvider(ClusterProviderEnum.AWS)
                .clusterGroupName(clusterGroup)
                .region(region)
                .attributes(attributes)
                .gpusV5(Set.of(GpuV5Udt.builder()
                                       .name(gpuName)
                                       .instanceTypes(Set.of(InstanceTypeV5Udt.builder()
                                                                     .name(instanceTypeName)
                                                                     .value(instanceTypeName)
                                                                     .description(
                                                                             "Test instance type")
                                                                     .cpuCores(4)
                                                                     .systemMemory("16G")
                                                                     .gpuMemory("24G")
                                                                     .gpuCount(1)
                                                                     .os("ubuntu")
                                                                     .cpuArch("x86_64")
                                                                     .storage("80G")
                                                                     .driverVersion("525.85.12")
                                                                     .isDefault(true)
                                                                     .nodeType(
                                                                             NodeTypeEnum.SINGLE.toString())
                                                                     .build()))
                                       .build()))
                .build();

        clusterRepository.saveClusterInfo(clusterEntity);

        // Mock service calls

        CloudHealthEntity cloudHealth = getDummyHealthEntity(nvcaClusterId1, Set.of("A100"));
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        GpuCapacity capacity = new GpuCapacity();
        capacity.setAvailable(10);
        capacity.setCapacity(10);
        gpuUsage.put("A100", capacity);
        cloudHealth.setGpuUsage(gpuUsage);
        Map<String, CloudHealthEntity> cloudHealthMap = new HashMap<>();
        cloudHealthMap.put(clusterId, cloudHealth);

        saveHealthEntityInDb(Set.of(cloudHealth));

        // When
        InstanceTypeAvailabilityResponse response = accountInfoService.getInstanceTypeAvailability(
                ncaId);

        // Then
        assertNotNull(response);
        assertNotNull(response.getGpus());
        assertFalse(response.getGpus().isEmpty());

        InstanceTypeAvailabilityResponse.Gpu gpu = response.getGpus().get(0);
        assertEquals(gpuName, gpu.getGpuName());
        assertEquals("x86_64", gpu.getCpuArch());

        assertNotNull(gpu.getInstanceTypes());
        assertFalse(gpu.getInstanceTypes().isEmpty());

        InstanceTypeAvailabilityResponse.InstanceType instanceType = gpu.getInstanceTypes().get(0);
        assertEquals(instanceTypeName, instanceType.getInstanceName());
        assertEquals(instanceTypeName, instanceType.getValue());
        assertEquals("Test instance type", instanceType.getDescription());
        assertEquals("4", instanceType.getCpuCores());
        assertEquals("16G", instanceType.getSystemMemory());
        assertEquals("24G", instanceType.getGpuMemory());
        assertEquals(1, instanceType.getGpuCount());
        assertEquals("ubuntu", instanceType.getOs());
        assertEquals("525.85.12", instanceType.getDriverVersion());
        assertEquals("80G", instanceType.getStorage());
        assertEquals(InstanceTypeAvailabilityResponse.NodeType.SINGLE, instanceType.getNodeType());

        assertNotNull(instanceType.getRegions());
        assertFalse(instanceType.getRegions().isEmpty());

        InstanceTypeAvailabilityResponse.Region regionResponse = instanceType.getRegions().get(0);
        assertEquals(region, regionResponse.getRegionName());

        assertNotNull(regionResponse.getClusters());
        assertFalse(regionResponse.getClusters().isEmpty());

        InstanceTypeAvailabilityResponse.Cluster cluster = regionResponse.getClusters().get(0);
        assertEquals(clusterId, cluster.getClusterId());
        assertEquals(clusterName, cluster.getClusterName());
        assertEquals(cloudProvider, cluster.getCloudProvider());
        assertEquals(clusterGroup, cluster.getClusterGroup());
        assertTrue(cluster.getIsDefaultInstanceType());
        assertEquals(availableCapacity, cluster.getMaxClusterAvailableCapacity());
    } */

    private void validateResponse(String responseKey, Map<String, Set<String>> response, Set<String> expectedResponse, Set<String> nonExpectedResponse){
        // ASSERT
        assertNotNull(response);
        assertTrue(response.containsKey(responseKey));

        Set<String> valuesFromResponse = response.get(responseKey);
        assertEquals(expectedResponse.size(), valuesFromResponse.size());

        // Validating expected response
        expectedResponse.forEach(region -> assertTrue(valuesFromResponse.contains(region)));

        // Validating non expected response
        nonExpectedResponse.forEach(region -> assertFalse(valuesFromResponse.contains(region)));
    }

    private void validateInstanceTypeResponse(Map<String, Set<InstanceTypeDetails>> instanceTypeResponse,
                                              String gpuName,
                                              Set<String> expectedInstanceTypes,
                                              Set<String> nonExpectedInstanceTypes) {

        assertTrue(instanceTypeResponse.containsKey(gpuName));
        Set<String> instanceTypeFromResponse = instanceTypeResponse.get(gpuName)
                .stream().map(InstanceTypeDetails::getName)
                .collect(Collectors.toSet());

        assertEquals(expectedInstanceTypes.size(), instanceTypeFromResponse.size());
        assertTrue(instanceTypeFromResponse.containsAll(expectedInstanceTypes));
        nonExpectedInstanceTypes.forEach(it -> assertFalse(instanceTypeFromResponse.contains(it)));
    }

    private void cleanupClusterEntityFromDb(Set<ClusterEntity> clusterEntities){
        clusterEntities.forEach(clusterEntity -> nvcaClusterRepository.deleteClusterInfo(clusterEntity));
    }

    private void saveClusterEntityInDb(Set<ClusterEntity> clusterEntities) {
        clusterEntities
                .forEach(clusterEntity -> nvcaClusterRepository.saveClusterInfo(clusterEntity));
    }

    private void saveHealthEntityInDb(Set<CloudHealthEntity> cloudHealthEntities){
        cloudHealthEntities
                .forEach(entity -> cloudHealthRepository.insert(entity, 300));
    }

    private ClusterEntity getNonByocClusterEntity(
            String clusterId, String region, Set<GpuV5Udt> gpusV5, Set<String> attributes) {

        return getClusterEntity(clusterId, region, TestUtil.DUMMY_NON_BYOC_NCA_ID, gpusV5, PLATFORM_CLUSTER_GROUP_NAME,
                                ClusterProviderEnum.OCI, Set.of("*"), attributes);
    }

    private ClusterEntity getClusterEntity(String clusterId, String region, String ncaId, Set<GpuV5Udt> gpusV5,
                                           String clusterGroupName, ClusterProviderEnum clusterProviderEnum,
                                           Set<String> authorizedNcaId, Set<String> attributes) {
        ClusterEntity clusterEntity = new ClusterEntity();
        clusterEntity.setClusterId(clusterId);
        clusterEntity.setClusterGroupId(clusterGroupName + "_id");
        clusterEntity.setClusterName(clusterId);
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);
        clusterEntity.setRegion(region);
        clusterEntity.setNvcaVersion("1.0.0");
        clusterEntity.setClusterGroupName(clusterGroupName);
        clusterEntity.setNcaId(ncaId);
        clusterEntity.setClusterProvider(clusterProviderEnum);
        clusterEntity.setAuthorizedNcaIds(authorizedNcaId);
        clusterEntity.setAllowClusterTargeting(Boolean.TRUE);
        clusterEntity.setGpusV5(gpusV5);
        clusterEntity.setAttributes(attributes);
        return clusterEntity;
    }

    private GpuV5Udt getDummyGpuV5(String instanceTypeName, String gpuName) {
        Set<InstanceTypeV5Udt> instanceTypes = new HashSet<>();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setName(instanceTypeName);
        instanceType.setGpuCount(1);
        instanceTypes.add(instanceType);

        GpuV5Udt gpuV5Udt = new GpuV5Udt();
        gpuV5Udt.setName(gpuName);
        gpuV5Udt.setCapacity(10);
        gpuV5Udt.setInstanceTypes(instanceTypes);

        return gpuV5Udt;
    }

    private CloudHealthEntity getDummyHealthEntity(String clusterId, Set<String> gpuNames) {
        CloudHealthKey healthKey1 = new CloudHealthKey(ResourceProvider.OCI, clusterId);
        CloudHealthEntity health1 = new CloudHealthEntity();
        health1.setKey(healthKey1);
        health1.setStatus(CloudHealthStatus.HEALTHY);

        Map<String, GpuCapacity> gpuUsage1 = new HashMap<>();
        gpuNames.forEach(gpuName -> {
            GpuCapacity gpuCapacity1 = new GpuCapacity();
            gpuCapacity1.setCapacity(10);
            gpuCapacity1.setAllocated(5);
            gpuCapacity1.setAvailable(5);
            gpuUsage1.put(gpuName, gpuCapacity1);
        });
        health1.setGpuUsage(gpuUsage1);
        return health1;
    }

}
