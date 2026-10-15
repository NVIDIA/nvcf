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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.factory.InstanceRequestEntityFactory;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.account.DeploymentGpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.reservation.ReservationRepository;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;
import com.nvidia.icms.service.extensions.impl.NoOpClusterAuthorizationService;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.byoc.ClustersService;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

import com.nvidia.icms.service.extensions.api.ReservationProcessor;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class GpuUsageEventServiceTest {

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    @Mock
    private InstanceV2Repository instanceV2Repository;

    @Mock
    private ClusterTargetingHelper clusterTargetingHelper;

    private GpuUsageService gpuUsageService;

    private ClusterGpuInfoHelper clusterGpuInfoHelper;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private ReservationRepository reservationRepository;

    @Mock
    private ReservationProcessor reservationProcessor;

    @Mock
    private InstanceLifecycleHelper instanceLifecycleHelper;

    @BeforeEach
    void setUp() {
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(icmsConfigurationProperties,
                                                        clusterTargetingHelper,
                                                        reservationProcessor,
                                                        instanceLifecycleHelper,
                                                        ComputePlatformTestFixtures.nonByocComputePlatformService(),
                                                        new NoOpClusterAuthorizationService());

        gpuUsageService = new GpuUsageService(
                instanceRequestV2Repository,
                instanceV2Repository,
                icmsConfigurationProperties,
                clusterGpuInfoHelper,
                ComputePlatformTestFixtures.nonByocComputePlatformService());
    }

    @Test
    void getGpuUsage_NoInstances_ReturnsEmptyResponse() {
        // Arrange
        String ncaId = "test-nca-id";
        when(instanceRequestV2Repository.findRequestsPerNcaId(ncaId)).thenReturn(new ArrayList<>());
        when(instanceV2Repository.findAllInstancesByCustomerAndRequestIds(any(),
                                                                              any(),
                                                                              anyBoolean())).thenReturn(
                new HashMap<>());

        // Act
        GpuUsageResponse response = gpuUsageService.getGpuUsage(ncaId);

        // Assert
        assertNotNull(response);
        assertTrue(response.getGpus().isEmpty());
    }

    @Test
    void getGpuUsage_WithInstances_ReturnsCorrectResponse() {
        // Arrange
        String ncaId = "test-nca-id";
        String requestId = "test-request-id";
        InstanceRequestV2Entity request = InstanceRequestEntityFactory.createDefaultInstanceRequestV2(requestId, null, null, null);
        List<InstanceRequestV2Entity> requests = new ArrayList<>();
        requests.add(request);
        Set<String> requestIds = new HashSet<>(Collections.singletonList(requestId));

        UUID clusterId1 = UUID.randomUUID();
        UUID clusterId2 = UUID.randomUUID();

        InstanceV2Entity runningInstance = createInstance("gpu1", "us-west-1",
                                                              clusterId1.toString(), "instance1",
                                                              SpotInstanceInternalState.RUNNING);
        InstanceV2Entity pendingInstance = createInstance("gpu1", "us-west-1",
                                                              clusterId1.toString(), "instance2",
                                                              SpotInstanceInternalState.STARTING);
        InstanceV2Entity otherRegionInstance = createInstance("gpu1", "us-east-1",
                                                                  clusterId2.toString(),
                                                                  "instance3",
                                                                  SpotInstanceInternalState.RUNNING);
        InstanceV2Entity otherGpuInstance = createInstance("gpu2", "us-west-1",
                                                               clusterId1.toString(), "instance4",
                                                               SpotInstanceInternalState.RUNNING);

        Map<String, List<InstanceV2Entity>> instances = new HashMap<>();
        instances.put(requestId,
                      Arrays.asList(runningInstance, pendingInstance, otherRegionInstance,
                                    otherGpuInstance));

        when(instanceRequestV2Repository.findRequestsPerNcaId(ncaId)).thenReturn(requests);
        when(instanceV2Repository.findAllInstancesByCustomerAndRequestIds(any(),
                                                                              eq(requestIds),
                                                                              anyBoolean())).thenReturn(
                instances);

        // Act
        GpuUsageResponse response = gpuUsageService.getGpuUsage(ncaId);

        // Assert
        assertNotNull(response);
        assertEquals(2, response.getGpus().size());

        // Verify GPU1 data
        GpuUsageResponse.Gpu gpu1 = response.getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu1.getInstances().size());
        assertEquals(2, gpu1.getInstances().get(0).getRegions().size());

        // Verify us-west-1 region for GPU1
        GpuUsageResponse.Region usWest1 = gpu1.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, usWest1.getClusters().size());
        assertEquals(clusterId1.toString(), usWest1.getClusters().get(0).getClusterId());
        assertEquals(1, usWest1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(1, usWest1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify us-east-1 region for GPU1
        GpuUsageResponse.Region usEast1 = gpu1.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-east-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, usEast1.getClusters().size());
        assertEquals(clusterId2.toString(), usEast1.getClusters().get(0).getClusterId());
        assertEquals(1, usEast1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, usEast1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify GPU2 data
        GpuUsageResponse.Gpu gpu2 = response.getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu2"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu2.getInstances().size());
        assertEquals(1, gpu2.getInstances().get(0).getRegions().size());
        assertEquals(1, gpu2.getInstances().get(0).getRegions().get(0).getClusters().size());
        assertEquals(clusterId1.toString(),
                     gpu2.getInstances().get(0).getRegions().get(0).getClusters().get(0)
                             .getClusterId());
        assertEquals(1,
                     gpu2.getInstances().get(0).getRegions().get(0).getClusters().get(0).getStatus()
                             .getActiveInstances());
        assertEquals(0,
                     gpu2.getInstances().get(0).getRegions().get(0).getClusters().get(0).getStatus()
                             .getPendingInstances());
    }

    @Test
    void getGpuUsage_WithMultipleInstancesAndStates_ReturnsCorrectResponse() {
        // Arrange
        String ncaId = "test-nca-id";
        String requestId = "test-request-id";
        InstanceRequestV2Entity request = InstanceRequestEntityFactory.createDefaultInstanceRequestV2(requestId, null, null, null);
        List<InstanceRequestV2Entity> requests = new ArrayList<>();
        requests.add(request);
        Set<String> requestIds = new HashSet<>(Collections.singletonList(requestId));

        UUID clusterId1 = UUID.randomUUID();
        UUID clusterId2 = UUID.randomUUID();
        UUID clusterId3 = UUID.randomUUID();

        // Multiple instances in different states for GPU1
        InstanceV2Entity runningInstance1 = createInstance("gpu1", "us-west-1",
                                                               clusterId1.toString(), "instance1",
                                                               SpotInstanceInternalState.RUNNING);
        InstanceV2Entity runningInstance2 = createInstance("gpu1", "us-west-1",
                                                               clusterId1.toString(), "instance2",
                                                               SpotInstanceInternalState.RUNNING);
        InstanceV2Entity pendingInstance = createInstance("gpu1", "us-west-1",
                                                              clusterId1.toString(), "instance3",
                                                              SpotInstanceInternalState.STARTING);
        InstanceV2Entity otherRegionInstance = createInstance("gpu1", "us-east-1",
                                                                  clusterId2.toString(),
                                                                  "instance4",
                                                                  SpotInstanceInternalState.RUNNING);
        InstanceV2Entity thirdRegionInstance = createInstance("gpu1", "eu-west-1",
                                                                  clusterId3.toString(),
                                                                  "instance5",
                                                                  SpotInstanceInternalState.RUNNING);

        // Multiple instances for GPU2
        InstanceV2Entity gpu2Instance1 = createInstance("gpu2", "us-west-1",
                                                            clusterId1.toString(), "instance6",
                                                            SpotInstanceInternalState.RUNNING);
        InstanceV2Entity gpu2Instance2 = createInstance("gpu2", "us-west-1",
                                                            clusterId1.toString(), "instance7",
                                                            SpotInstanceInternalState.STARTING);
        InstanceV2Entity gpu2Instance3 = createInstance("gpu2", "us-east-1",
                                                            clusterId2.toString(), "instance8",
                                                            SpotInstanceInternalState.RUNNING);

        // Multiple instances for GPU3
        InstanceV2Entity gpu3Instance1 = createInstance("gpu3", "us-west-1",
                                                            clusterId1.toString(), "instance9",
                                                            SpotInstanceInternalState.RUNNING);
        InstanceV2Entity gpu3Instance2 = createInstance("gpu3", "eu-west-1",
                                                            clusterId3.toString(), "instance10",
                                                            SpotInstanceInternalState.RUNNING);

        Map<String, List<InstanceV2Entity>> instances = new HashMap<>();
        instances.put(requestId, Arrays.asList(
                runningInstance1, runningInstance2, pendingInstance, otherRegionInstance,
                thirdRegionInstance,
                gpu2Instance1, gpu2Instance2, gpu2Instance3,
                gpu3Instance1, gpu3Instance2
        ));

        when(instanceRequestV2Repository.findRequestsPerNcaId(ncaId)).thenReturn(requests);
        when(instanceV2Repository.findAllInstancesByCustomerAndRequestIds(any(),
                                                                              eq(requestIds),
                                                                              anyBoolean())).thenReturn(
                instances);

        // Act
        GpuUsageResponse response = gpuUsageService.getGpuUsage(ncaId);

        // Assert
        assertNotNull(response);
        assertEquals(3, response.getGpus().size());

        // Verify GPU1 data
        GpuUsageResponse.Gpu gpu1 = response.getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu1.getInstances().size());
        assertEquals(3, gpu1.getInstances().get(0).getRegions().size());

        // Verify us-west-1 region for GPU1
        GpuUsageResponse.Region usWest1 = gpu1.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, usWest1.getClusters().size());
        assertEquals(clusterId1.toString(), usWest1.getClusters().get(0).getClusterId());
        assertEquals(2, usWest1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(1, usWest1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify us-east-1 region for GPU1
        GpuUsageResponse.Region usEast1 = gpu1.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-east-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, usEast1.getClusters().size());
        assertEquals(clusterId2.toString(), usEast1.getClusters().get(0).getClusterId());
        assertEquals(1, usEast1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, usEast1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify eu-west-1 region for GPU1
        GpuUsageResponse.Region euWest1 = gpu1.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("eu-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, euWest1.getClusters().size());
        assertEquals(clusterId3.toString(), euWest1.getClusters().get(0).getClusterId());
        assertEquals(1, euWest1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, euWest1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify GPU2 data
        GpuUsageResponse.Gpu gpu2 = response.getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu2"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu2.getInstances().size());
        assertEquals(2, gpu2.getInstances().get(0).getRegions().size());

        // Verify us-west-1 region for GPU2
        GpuUsageResponse.Region gpu2UsWest1 = gpu2.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu2UsWest1.getClusters().size());
        assertEquals(clusterId1.toString(), gpu2UsWest1.getClusters().get(0).getClusterId());
        assertEquals(1, gpu2UsWest1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(1, gpu2UsWest1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify us-east-1 region for GPU2
        GpuUsageResponse.Region gpu2UsEast1 = gpu2.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-east-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu2UsEast1.getClusters().size());
        assertEquals(clusterId2.toString(), gpu2UsEast1.getClusters().get(0).getClusterId());
        assertEquals(1, gpu2UsEast1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, gpu2UsEast1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify GPU3 data
        GpuUsageResponse.Gpu gpu3 = response.getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu3"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu3.getInstances().size());
        assertEquals(2, gpu3.getInstances().get(0).getRegions().size());

        // Verify us-west-1 region for GPU3
        GpuUsageResponse.Region gpu3UsWest1 = gpu3.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu3UsWest1.getClusters().size());
        assertEquals(clusterId1.toString(), gpu3UsWest1.getClusters().get(0).getClusterId());
        assertEquals(1, gpu3UsWest1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, gpu3UsWest1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify eu-west-1 region for GPU3
        GpuUsageResponse.Region gpu3EuWest1 = gpu3.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("eu-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu3EuWest1.getClusters().size());
        assertEquals(clusterId3.toString(), gpu3EuWest1.getClusters().get(0).getClusterId());
        assertEquals(1, gpu3EuWest1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, gpu3EuWest1.getClusters().get(0).getStatus().getPendingInstances());
    }

    @Test
    void getDeploymentGpuUsage_NoInstances_ReturnsEmptyResponse() {
        // Arrange
        String ncaId = "test-nca-id";
        String deploymentId = "123e4567-e89b-12d3-a456-426614174000"; // Valid UUID string
        when(instanceRequestV2Repository.findRequestsPerNcaIdAndDeploymentId(eq(ncaId),
                                                                           any())).thenReturn(
                new ArrayList<>());
        when(instanceV2Repository.findAllInstancesByCustomerAndRequestIds(any(),
                                                                              any(),
                                                                              anyBoolean())).thenReturn(
                new HashMap<>());

        // Act
        DeploymentGpuUsageResponse response = gpuUsageService.getDeploymentGpuUsage(ncaId,
                                                                                    deploymentId);

        // Assert
        assertNotNull(response);
        assertEquals(1, response.getDeployments().size());
        assertTrue(response.getDeployments().get(0).getGpus().isEmpty());
    }

    @Test
    void getDeploymentGpuUsage_WithInstances_ReturnsCorrectResponse() {
        // Arrange
        String ncaId = "test-nca-id";
        String deploymentId = "123e4567-e89b-12d3-a456-426614174000"; // Valid UUID string
        String requestId = "test-request-id";
        InstanceRequestV2Entity request = InstanceRequestEntityFactory.createDefaultInstanceRequestV2(requestId, null, null, null);
        List<InstanceRequestV2Entity> requests = new ArrayList<>();
        requests.add(request);
        Set<String> requestIds = new HashSet<>(Collections.singletonList(requestId));

        UUID clusterId = UUID.randomUUID();

        InstanceV2Entity runningInstance = createInstance("gpu1", "us-west-1",
                                                              clusterId.toString(), "instance1",
                                                              SpotInstanceInternalState.RUNNING);
        InstanceV2Entity pendingInstance = createInstance("gpu1", "us-west-1",
                                                              clusterId.toString(), "instance2",
                                                              SpotInstanceInternalState.STARTING);
        InstanceV2Entity otherGpuInstance = createInstance("gpu2", "us-west-1",
                                                               clusterId.toString(), "instance3",
                                                               SpotInstanceInternalState.RUNNING);

        Map<String, List<InstanceV2Entity>> instances = new HashMap<>();
        instances.put(requestId, Arrays.asList(runningInstance, pendingInstance, otherGpuInstance));

        when(instanceRequestV2Repository.findRequestsPerNcaIdAndDeploymentId(eq(ncaId),
                                                                           any())).thenReturn(requests);
        when(instanceV2Repository.findAllInstancesByCustomerAndRequestIds(any(),
                                                                              eq(requestIds),
                                                                              anyBoolean())).thenReturn(
                instances);

        // Act
        DeploymentGpuUsageResponse response = gpuUsageService.getDeploymentGpuUsage(ncaId,
                                                                                    deploymentId);

        // Assert
        assertNotNull(response);
        assertEquals(1, response.getDeployments().size());
        assertEquals(UUID.fromString(deploymentId),
                     response.getDeployments().get(0).getDeploymentId());
        assertEquals(2, response.getDeployments().get(0).getGpus().size());

        // Verify GPU1 data
        GpuUsageResponse.Gpu gpu1 = response.getDeployments().get(0).getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu1.getInstances().size());
        assertEquals(1, gpu1.getInstances().get(0).getRegions().size());
        assertEquals(1, gpu1.getInstances().get(0).getRegions().get(0).getClusters().size());
        assertEquals(clusterId.toString(),
                     gpu1.getInstances().get(0).getRegions().get(0).getClusters().get(0)
                             .getClusterId());
        assertEquals(1,
                     gpu1.getInstances().get(0).getRegions().get(0).getClusters().get(0).getStatus()
                             .getActiveInstances());
        assertEquals(1,
                     gpu1.getInstances().get(0).getRegions().get(0).getClusters().get(0).getStatus()
                             .getPendingInstances());

        // Verify GPU2 data
        GpuUsageResponse.Gpu gpu2 = response.getDeployments().get(0).getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu2"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu2.getInstances().size());
        assertEquals(1, gpu2.getInstances().get(0).getRegions().size());
        assertEquals(1, gpu2.getInstances().get(0).getRegions().get(0).getClusters().size());
        assertEquals(clusterId.toString(),
                     gpu2.getInstances().get(0).getRegions().get(0).getClusters().get(0)
                             .getClusterId());
        assertEquals(1,
                     gpu2.getInstances().get(0).getRegions().get(0).getClusters().get(0).getStatus()
                             .getActiveInstances());
        assertEquals(0,
                     gpu2.getInstances().get(0).getRegions().get(0).getClusters().get(0).getStatus()
                             .getPendingInstances());
    }

    @Test
    void getDeploymentGpuUsage_WithMultipleInstancesAndRegions_ReturnsCorrectResponse() {
        // Arrange
        String ncaId = "test-nca-id";
        String deploymentId = "123e4567-e89b-12d3-a456-426614174000"; // Valid UUID string
        String requestId = "test-request-id";
        InstanceRequestV2Entity request = InstanceRequestEntityFactory.createDefaultInstanceRequestV2(requestId, null, null, null);
        List<InstanceRequestV2Entity> requests = new ArrayList<>();
        requests.add(request);
        Set<String> requestIds = new HashSet<>(Collections.singletonList(requestId));

        UUID clusterId1 = UUID.randomUUID();
        UUID clusterId2 = UUID.randomUUID();
        UUID clusterId3 = UUID.randomUUID();

        // Multiple instances for GPU1 across different regions
        InstanceV2Entity gpu1UsWest1 = createInstance("gpu1", "us-west-1",
                                                          clusterId1.toString(), "instance1",
                                                          SpotInstanceInternalState.RUNNING);
        InstanceV2Entity gpu1UsWest1Pending = createInstance("gpu1", "us-west-1",
                                                                 clusterId1.toString(), "instance2",
                                                                 SpotInstanceInternalState.STARTING);
        InstanceV2Entity gpu1UsEast1 = createInstance("gpu1", "us-east-1",
                                                          clusterId2.toString(), "instance3",
                                                          SpotInstanceInternalState.RUNNING);
        InstanceV2Entity gpu1EuWest1 = createInstance("gpu1", "eu-west-1",
                                                          clusterId3.toString(), "instance4",
                                                          SpotInstanceInternalState.RUNNING);

        // Multiple instances for GPU2
        InstanceV2Entity gpu2UsWest1 = createInstance("gpu2", "us-west-1",
                                                          clusterId1.toString(), "instance5",
                                                          SpotInstanceInternalState.RUNNING);
        InstanceV2Entity gpu2UsEast1 = createInstance("gpu2", "us-east-1",
                                                          clusterId2.toString(), "instance6",
                                                          SpotInstanceInternalState.RUNNING);
        InstanceV2Entity gpu2UsEast1Pending = createInstance("gpu2", "us-east-1",
                                                                 clusterId2.toString(), "instance7",
                                                                 SpotInstanceInternalState.STARTING);

        Map<String, List<InstanceV2Entity>> instances = new HashMap<>();
        instances.put(requestId, Arrays.asList(
                gpu1UsWest1, gpu1UsWest1Pending, gpu1UsEast1, gpu1EuWest1,
                gpu2UsWest1, gpu2UsEast1, gpu2UsEast1Pending
        ));

        when(instanceRequestV2Repository.findRequestsPerNcaIdAndDeploymentId(eq(ncaId),
                                                                           any())).thenReturn(
                requests);
        when(instanceV2Repository.findAllInstancesByCustomerAndRequestIds(any(),
                                                                              eq(requestIds),
                                                                              anyBoolean())).thenReturn(
                instances);

        // Act
        DeploymentGpuUsageResponse response = gpuUsageService.getDeploymentGpuUsage(ncaId,
                                                                                    deploymentId);

        // Assert
        assertNotNull(response);
        assertEquals(1, response.getDeployments().size());
        assertEquals(UUID.fromString(deploymentId),
                     response.getDeployments().get(0).getDeploymentId());
        assertEquals(2, response.getDeployments().get(0).getGpus().size());

        // Verify GPU1 data
        GpuUsageResponse.Gpu gpu1 = response.getDeployments().get(0).getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu1.getInstances().size());
        assertEquals(3, gpu1.getInstances().get(0).getRegions().size());

        // Verify us-west-1 region for GPU1
        GpuUsageResponse.Region usWest1 = gpu1.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, usWest1.getClusters().size());
        assertEquals(clusterId1.toString(), usWest1.getClusters().get(0).getClusterId());
        assertEquals(1, usWest1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(1, usWest1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify us-east-1 region for GPU1
        GpuUsageResponse.Region usEast1 = gpu1.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-east-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, usEast1.getClusters().size());
        assertEquals(clusterId2.toString(), usEast1.getClusters().get(0).getClusterId());
        assertEquals(1, usEast1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, usEast1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify eu-west-1 region for GPU1
        GpuUsageResponse.Region euWest1 = gpu1.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("eu-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, euWest1.getClusters().size());
        assertEquals(clusterId3.toString(), euWest1.getClusters().get(0).getClusterId());
        assertEquals(1, euWest1.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, euWest1.getClusters().get(0).getStatus().getPendingInstances());

        // Verify GPU2 data
        GpuUsageResponse.Gpu gpu2 = response.getDeployments().get(0).getGpus().stream()
                .filter(g -> g.getGpuName().equals("gpu2"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu2.getInstances().size());
        assertEquals(2, gpu2.getInstances().get(0).getRegions().size());

        // Verify us-west-1 region for GPU2
        GpuUsageResponse.Region gpu2UsWest1Region = gpu2.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-west-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu2UsWest1Region.getClusters().size());
        assertEquals(clusterId1.toString(), gpu2UsWest1Region.getClusters().get(0).getClusterId());
        assertEquals(1, gpu2UsWest1Region.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(0, gpu2UsWest1Region.getClusters().get(0).getStatus().getPendingInstances());

        // Verify us-east-1 region for GPU2
        GpuUsageResponse.Region gpu2UsEast1Region = gpu2.getInstances().get(0).getRegions().stream()
                .filter(r -> r.getRegionName().equals("us-east-1"))
                .findFirst()
                .orElseThrow();
        assertEquals(1, gpu2UsEast1Region.getClusters().size());
        assertEquals(clusterId2.toString(), gpu2UsEast1Region.getClusters().get(0).getClusterId());
        assertEquals(1, gpu2UsEast1Region.getClusters().get(0).getStatus().getActiveInstances());
        assertEquals(1, gpu2UsEast1Region.getClusters().get(0).getStatus().getPendingInstances());
    }





    private InstanceV2Entity createInstance(
            String gpu, String region, String zone, String instanceId,
            SpotInstanceInternalState state) {
        return InstanceV2Entity.builder()
                .gpu(gpu)
                .region(region)
                .zone(zone)
                .instanceId(instanceId)
                .instanceStateName(state)
                .instanceType("test-instance-type")
                .build();
    }

    private ClustersService.ReadyClusterInfo createReadyClusterInfo(String clusterId, String gpu) {
        return ClustersService.ReadyClusterInfo.builder()
                .clusterId(clusterId)
                .clusterName(clusterId)
                .gpu(gpu)
                .region("us-west-1")
                .clusterProvider(ClusterProviderEnum.AZURE)
                .clusterGroupName("group1")
                .attributes(Collections.singleton("attr1"))
                .build();
    }

    private InstanceTypeV5Udt createInstanceType(String name, int gpuCount) {
        return InstanceTypeV5Udt.builder()
                .name(name)
                .value("value1")
                .description("description1")
                .cpuCores(4)
                .systemMemory("24G")
                .gpuMemory("40G")
                .gpuCount(gpuCount)
                .os("ubuntu20.04")
                .driverVersion("525.60.13")
                .storage("80G")
                .isDefault(true)
                .build();
    }
}
