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
package com.nvidia.icms.service;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.service.extensions.api.CloudHealthEventService;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudHealthUpdateRequest;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.metrics.ClusterGpuUsageMetricsService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import java.util.Map;
import java.util.Optional;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class CloudHealthServiceTest {

    @Mock
    private CloudHealthRepository cloudHealthRepository;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private CloudHealthEventService cloudHealthEventService;

    @Mock
    private ClusterGpuUsageMetricsService clusterGpuUsageMetricsService;

    @Mock
    private ClusterRepository clusterRepository;

    private CloudHealthService cloudHealthService;


    @BeforeEach
    void init() {
        cloudHealthService = new CloudHealthService(cloudHealthRepository,
                                                    icmsConfigurationProperties,
                                                    cloudHealthEventService,
                                                    clusterGpuUsageMetricsService,
                                                    clusterRepository,
                                                    ComputePlatformTestFixtures.nonByocComputePlatformService());
    }

    private static com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity capacity(
            int total, int available, int allocated) {
        com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity capacity =
                new com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity();
        capacity.setCapacity(total);
        capacity.setAvailable(available);
        capacity.setAllocated(allocated);
        return capacity;
    }

    private static com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity clusterWithCpuEntry(
            String zone, String gpuName, String instanceTypeName, int gpuCount) {
        com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt instanceType =
                new com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt();
        instanceType.setName(instanceTypeName);
        instanceType.setGpuCount(gpuCount);
        return com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity.builder()
                .clusterId(zone).clusterName("cluster-name")
                .gpusV5(new java.util.HashSet<>(java.util.List.of(
                        com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt.builder()
                                .name(gpuName)
                                .instanceTypes(java.util.Set.of(instanceType)).build())))
                .build();
    }

    @Test
    void updateCloudHealthStatus_storedCpuShape_goesToCpuGaugesAndNotToGpuGauges() {
        String zone = "cluster-1";
        com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity cluster =
                clusterWithCpuEntry(zone, "CPU", "NCP.CPU.4c16g", 0);
        when(clusterRepository.getClusterInfoByClusterId(zone, true)).thenReturn(Optional.of(cluster));

        cloudHealthService.updateCloudHealthStatus(
                ResourceProvider.OCI, zone, CloudHealthStatus.HEALTHY, null,
                java.util.Map.of("CPU", capacity(10, 7, 3)), 100);

        verify(clusterGpuUsageMetricsService).recordCpuCapacity("cluster-name", "CPU", 10, 7, 3, cluster);
        verify(clusterGpuUsageMetricsService, org.mockito.Mockito.never())
                .recordTotalGpus(any(), any(), any(), any());
        verify(clusterGpuUsageMetricsService, org.mockito.Mockito.never())
                .recordAvailableGpus(any(), any(), any(), any());
        verify(clusterGpuUsageMetricsService, org.mockito.Mockito.never())
                .recordOccupiedGpus(any(), any(), any(), any());
    }

    @Test
    void updateCloudHealthStatus_legacyStoredCpuShape_goesToCpuGauges() {
        String zone = "cluster-3";
        com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt instanceType =
                new com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt();
        instanceType.setName("NCP.CPU.4c16g");
        instanceType.setGpuCount(0);
        com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity cluster =
                com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity.builder()
                        .clusterId(zone).clusterName("cluster-name")
                        .gpus(new java.util.HashSet<>(java.util.List.of(
                                com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt.builder()
                                        .name("CPU").instanceTypes(java.util.Set.of(instanceType)).build())))
                        .build();
        when(clusterRepository.getClusterInfoByClusterId(zone, true)).thenReturn(Optional.of(cluster));

        cloudHealthService.updateCloudHealthStatus(
                ResourceProvider.OCI, zone, CloudHealthStatus.HEALTHY, null,
                java.util.Map.of("CPU", capacity(10, 7, 3)), 100);

        verify(clusterGpuUsageMetricsService).recordCpuCapacity("cluster-name", "CPU", 10, 7, 3, cluster);
        verify(clusterGpuUsageMetricsService, org.mockito.Mockito.never())
                .recordTotalGpus(any(), any(), any(), any());
    }

    @Test
    void updateCloudHealthStatus_gpuNamedCpu_keepsItsGpuGauges() {
        // Unchanged behavior: a real GPU that happens to be called CPU is still reported with
        // the GPU gauges, whatever the feature state is.
        String zone = "cluster-2";
        com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity cluster =
                clusterWithCpuEntry(zone, "CPU", "NCP.GPU.CPU_1x", 1);
        when(clusterRepository.getClusterInfoByClusterId(zone, true)).thenReturn(Optional.of(cluster));

        cloudHealthService.updateCloudHealthStatus(
                ResourceProvider.OCI, zone, CloudHealthStatus.HEALTHY, null,
                java.util.Map.of("CPU", capacity(4, 3, 1)), 100);

        verify(clusterGpuUsageMetricsService).recordTotalGpus("cluster-name", "CPU", 4, cluster);
        verify(clusterGpuUsageMetricsService, org.mockito.Mockito.never())
                .recordCpuCapacity(any(), any(), any(), any(), any(), any());
    }

    @Test
    void updateCloudHealthStatus_forHealthyZones() {
        // Prepare
        String ZONE1 = "zone-1";
        CloudHealthEntity zone1 =
                new CloudHealthEntity(new CloudHealthKey(ResourceProvider.OCI, ZONE1),
                                      CloudHealthStatus.HEALTHY,
                                      null,
                                      null);

        // Act
        CloudHealthUpdateRequest cloudHealthUpdateRequest = new CloudHealthUpdateRequest();
        cloudHealthUpdateRequest.setStatus(CloudHealthStatus.HEALTHY);
        cloudHealthService.updateCloudHealthStatus(ResourceProvider.OCI, ZONE1,
                                                   cloudHealthUpdateRequest, 100);

        // Assert
        verifyNoInteractions(cloudHealthEventService);
        verify(cloudHealthRepository).insert(zone1, 100);
    }

    @Test
    void updateCloudHealthStatus_forUnHealthyZones_forFirstTime() {
        // Prepare
        String ZONE1 = "zone-1";
        when(cloudHealthRepository.findByCloudAndZone(any(), any())).thenReturn(Optional.empty());

        // Act
        CloudHealthUpdateRequest cloudHealthUpdateRequest = new CloudHealthUpdateRequest();
        cloudHealthUpdateRequest.setStatus(CloudHealthStatus.UNHEALTHY);
        cloudHealthService.updateCloudHealthStatus(ResourceProvider.OCI, ZONE1,
                                                   cloudHealthUpdateRequest, 100);

        // Assert
        verify(cloudHealthEventService).handleUnhealthyCloud(any());
        verify(cloudHealthRepository).findByCloudAndZone(ResourceProvider.OCI, ZONE1);
    }

    @Test
    void updateCloudHealthStatus_forHealthyZones_becomingUnhealthy() {
        // Prepare
        String ZONE1 = "zone-1";
        CloudHealthEntity zone1 =
                new CloudHealthEntity(new CloudHealthKey(ResourceProvider.OCI, ZONE1),
                                      CloudHealthStatus.HEALTHY,
                                      null,
                                      null);
        when(cloudHealthRepository.findByCloudAndZone(any(), any())).thenReturn(Optional.of(zone1));
        when(icmsConfigurationProperties.getTtlToMarkCloudUnhealthy()).thenReturn(180);

        // Act
        CloudHealthUpdateRequest cloudHealthUpdateRequest = new CloudHealthUpdateRequest();
        cloudHealthUpdateRequest.setStatus(CloudHealthStatus.UNHEALTHY);
        cloudHealthService.updateCloudHealthStatus(ResourceProvider.OCI, ZONE1,
                                                   cloudHealthUpdateRequest, 100);

        // Assert
        verify(cloudHealthEventService).handleUnhealthyCloud(any());
        verify(icmsConfigurationProperties).getTtlToMarkCloudUnhealthy();
        verify(cloudHealthRepository).findByCloudAndZone(ResourceProvider.OCI, ZONE1);
        verify(cloudHealthRepository).insert(
                new CloudHealthEntity(new CloudHealthKey(ResourceProvider.OCI, ZONE1),
                                      CloudHealthStatus.UNHEALTHY,
                                      null,
                                      null),
                180);
    }

    @Test
    void updateCloudHealthStatus_withGpuUsage_forHealthyZones() {
        // Prepare
        String ZONE1 = "zone-1";
        var gpuCapacity = Map.of("gpu1",
                                 GpuCapacity.builder().capacity(10).allocated(4).available(6)
                                         .build(),
                                 "gpu2",
                                 GpuCapacity.builder().capacity(20).allocated(11).available(6)
                                         .build());
        CloudHealthEntity zone1 =
                new CloudHealthEntity(new CloudHealthKey(ResourceProvider.BYOC, ZONE1),
                                      CloudHealthStatus.HEALTHY,
                                      null,
                                      gpuCapacity);

        // Act
        cloudHealthService.updateCloudHealthStatus(ResourceProvider.BYOC, ZONE1,
                                                   CloudHealthStatus.HEALTHY,
                                                   null,
                                                   gpuCapacity,
                                                   100);

        // Assert
        verifyNoInteractions(cloudHealthEventService);
        verify(cloudHealthRepository).insert(zone1, 100);
    }

    @Test
    void updateCloudHealthStatus_withUpgradeStatusInProgress_forHealthyZones() {
        // Prepare
        String ZONE1 = "zone-1";
        var gpuCapacity = Map.of("gpu1",
                GpuCapacity.builder().capacity(10).allocated(4).available(6)
                        .build(),
                "gpu2",
                GpuCapacity.builder().capacity(20).allocated(11).available(6)
                        .build());
        CloudHealthEntity zone1 =
                new CloudHealthEntity(new CloudHealthKey(ResourceProvider.BYOC, ZONE1),
                                      CloudHealthStatus.HEALTHY,
                                      "IN_PROGRESS",
                                      gpuCapacity);

        // Act
        cloudHealthService.updateCloudHealthStatus(ResourceProvider.BYOC, ZONE1,
                CloudHealthStatus.HEALTHY,
                "IN_PROGRESS",
                gpuCapacity,
                100);

        // Assert
        verifyNoInteractions(cloudHealthEventService);
        verify(cloudHealthRepository).insert(zone1, 100);
    }

    @Test
    void updateCloudHealthStatus_withGpuUsage_forUnHealthyZones_forFirstTime() {
        // Prepare
        String ZONE1 = "zone-1";
        var gpuCapacity = Map.of("gpu1",
                                 GpuCapacity.builder().capacity(10).allocated(4).available(6)
                                         .build(),
                                 "gpu2",
                                 GpuCapacity.builder().capacity(20).allocated(11).available(6)
                                         .build());
        when(cloudHealthRepository.findByCloudAndZone(any(), any())).thenReturn(Optional.empty());

        // Act
        cloudHealthService.updateCloudHealthStatus(ResourceProvider.BYOC, ZONE1,
                                                   CloudHealthStatus.UNHEALTHY,
                                                   null,
                                                   gpuCapacity,
                                                   100);

        // Assert
        verifyNoInteractions(cloudHealthEventService);
        verify(cloudHealthRepository).findByCloudAndZone(ResourceProvider.BYOC, ZONE1);
    }

    @Test
    void updateCloudHealthStatus_withGpuUsage_forHealthyZones_becomingUnhealthy() {
        // Prepare
        String ZONE1 = "zone-1";
        var gpuCapacity = Map.of("gpu1",
                                 GpuCapacity.builder().capacity(10).allocated(4).available(6)
                                         .build(),
                                 "gpu2",
                                 GpuCapacity.builder().capacity(20).allocated(11).available(6)
                                         .build());
        CloudHealthEntity zone1 =
                new CloudHealthEntity(new CloudHealthKey(ResourceProvider.BYOC, ZONE1),
                                      CloudHealthStatus.HEALTHY,
                                      null,
                                      gpuCapacity);
        when(cloudHealthRepository.findByCloudAndZone(any(), any())).thenReturn(Optional.of(zone1));
        when(icmsConfigurationProperties.getTtlToMarkCloudUnhealthy()).thenReturn(180);

        // Act
        cloudHealthService.updateCloudHealthStatus(ResourceProvider.BYOC, ZONE1,
                                                   CloudHealthStatus.UNHEALTHY, null,
                                                   gpuCapacity, 100);

        // Assert
        verifyNoInteractions(cloudHealthEventService);
        verify(icmsConfigurationProperties).getTtlToMarkCloudUnhealthy();
        verify(cloudHealthRepository).findByCloudAndZone(ResourceProvider.BYOC, ZONE1);
        verify(cloudHealthRepository).insert(
                new CloudHealthEntity(new CloudHealthKey(ResourceProvider.BYOC, ZONE1),
                                      CloudHealthStatus.UNHEALTHY,
                                      null,
                                      gpuCapacity),
                180);
    }
}
