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
package com.nvidia.icms.service.scheduled.gpuusage;

import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_TASK_NAME;
import static org.junit.jupiter.api.Assertions.*;

import com.nvidia.icms.configuration.bean.InstanceTypeConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel.LaunchSpecification;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.service.LatestInstanceStateEventService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TestUtil;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import java.time.temporal.ChronoUnit;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;
import static org.mockito.ArgumentMatchers.argThat;

@ExtendWith(MockitoExtension.class)
@Slf4j
class GpuUsageEventServiceTest {

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    private GpuUsageEventServiceHelper gpuUsageEventServiceHelper;

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    @Mock
    private InstanceTypeConfigurationProperties instanceTypeConfigurationProperties;

    @Mock
    private LatestInstanceStateEventService latestInstanceStateEventService;

    private GpuUsageEventService gpuUsageEventService;

    @BeforeEach
    void setUp() {
        gpuUsageEventService = new GpuUsageEventService(
                telemetryEventClient,
                clusterRepository,
                gpuUsageEventServiceHelper,
                instanceRequestV2Repository,
                instanceTypeConfigurationProperties,
                latestInstanceStateEventService,
                ComputePlatformTestFixtures.nonByocComputePlatformService()
        );
    }

    // Fetching cloudProvider and gpuCountPerInstance from DB
    @Test
    void sendGpuUsageEventForRunningInstance_WhenUsingCloudProviderAndGpuCountFromDb() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousTime = currentTime.minus(1, ChronoUnit.HOURS);
        Instant creationTime = previousTime.minus(30, ChronoUnit.MINUTES);

        InstanceV2Entity instance = createTestInstance(
                "test-instance-entity-fields",
                "test-request-entity-fields",
                creationTime,
                "AZURE.GPU.A100_8x",
                "A100",
                SpotInstanceInternalState.RUNNING,
                currentTime
        );

        // Set cloudProvider and gpuCountPerInstance directly on the entity
        instance.setCloudProvider(CloudProvider.DGXCLOUD.toString());
        instance.setGpuCountPerInstance(8);

        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("AZURE.GPU.A100_8x", "A100"));

        // Act
        gpuUsageEventService.sendGpuUsageEventForRunningInstance(
                instance, currentTime, previousTime, cloudProviderCache);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.GPU_USAGE_PER_INSTANCE.toString(), metric.getEventName());
        assertEquals(instance.getInstanceId(), metric.getInstanceId());
        assertEquals(instance.getNcaId(), metric.getNcaId());
        assertEquals(instance.getGpu(), metric.getGpuName());
        assertEquals(instance.getInstanceType(), metric.getInstanceType());
        assertEquals(instance.getInstanceStateName().getStateName(), metric.getInstanceState());
        assertEquals(DUMMY_FUNCTION_NAME, metric.getFunctionName());

        // Should be time between previous job and current job (1 hour = 1.0 hours) * 8(gpuCountPerInstance from entity)
        assertEquals(8.0, metric.getGpuUsageInHours(), 0.01);

        verify(clusterRepository, never()).getClusterInfoByClusterId(anyString(), anyBoolean());
        verify(instanceTypeConfigurationProperties, never()).getGpuCountForInstanceType(anyString());
    }

    @Test
    void sendGpuUsageEventForRunningInstance_WithReservedCapacityTypeAndReservationId_ShouldEmitReserved() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousTime = currentTime.minus(1, ChronoUnit.HOURS);
        Instant creationTime = previousTime.minus(10, ChronoUnit.MINUTES);

        InstanceV2Entity instance = createTestInstance(
                "test-instance-reserved",
                "test-request-reserved",
                creationTime,
                "AZURE.GPU.A100_1x",
                "A100",
                SpotInstanceInternalState.RUNNING,
                currentTime
        );
        instance.setCloudProvider(CloudProvider.OCI.toString());
        instance.setGpuCountPerInstance(1);
        UUID reservationId = UUID.randomUUID();
        instance.setReservationId(reservationId);
        instance.setCapacityType(CapacityType.RESERVED.name());

        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("AZURE.GPU.A100_1x", "A100"));

        // Act
        gpuUsageEventService.sendGpuUsageEventForRunningInstance(
                instance, currentTime, previousTime, cloudProviderCache);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());
        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(reservationId.toString(), metric.getReservationId());
        assertEquals(CapacityType.RESERVED.toString(), metric.getCapacityType());
    }

    @Test
    void sendGpuUsageEventForRunningInstance_WithReservedBackupCapacityTypeAndReservationId_ShouldEmitReservedBackup() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousTime = currentTime.minus(1, ChronoUnit.HOURS);
        Instant creationTime = previousTime.minus(5, ChronoUnit.MINUTES);

        InstanceV2Entity instance = createTestInstance(
                "test-instance-reserved-backup",
                "test-request-reserved-backup",
                creationTime,
                "OCI.GPU.H100_1x",
                "H100",
                SpotInstanceInternalState.RUNNING,
                currentTime
        );
        instance.setCloudProvider(CloudProvider.DGXCLOUD.toString());
        instance.setGpuCountPerInstance(1);
        UUID reservationId = UUID.randomUUID();
        instance.setReservationId(reservationId);
        instance.setCapacityType(CapacityType.RESERVED_BACKUP.name());

        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("OCI.GPU.H100_1x", "H100"));

        // Act
        gpuUsageEventService.sendGpuUsageEventForRunningInstance(
                instance, currentTime, previousTime, cloudProviderCache);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());
        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(reservationId.toString(), metric.getReservationId());
        assertEquals(CapacityType.RESERVED_BACKUP.toString(), metric.getCapacityType());
    }

    @Test
    void sendGpuUsageEventForTerminatedInstance_WhenUsingCloudProviderAndGpuCountFromDb() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousJobTime = currentTime.truncatedTo(ChronoUnit.HOURS);
        when(gpuUsageEventServiceHelper.getInstantNow()).thenReturn(currentTime);

        Instant creationTime = previousJobTime.minus(20, ChronoUnit.MINUTES);
        Instant terminationTime = previousJobTime.plus(10, ChronoUnit.MINUTES);

        InstanceV2Entity instance = createTestInstance(
                "test-instance-entity-terminated",
                "test-request-entity-terminated",
                creationTime,
                "OCI.GPU.H1OO_6x",
                "H100",
                SpotInstanceInternalState.TERMINATED,
                terminationTime
        );

        // Set cloudProvider and gpuCountPerInstance directly on the entity
        instance.setCloudProvider(CloudProvider.OCI.toString());
        instance.setGpuCountPerInstance(6);

        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("OCI.GPU.H1OO_6x", "H100"));

        // Act
        gpuUsageEventService.sendGpuUsageEventForTerminatedInstance(instance);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.GPU_USAGE_PER_INSTANCE.toString(), metric.getEventName());
        assertEquals(DUMMY_FUNCTION_NAME, metric.getFunctionName());

        // Should be time between previous job and termination (10 minutes ≈ 0.17 hours) * 6 (gpuCountPerInstance from entity)
        assertEquals(1.02, metric.getGpuUsageInHours(), 0.01);

        verify(clusterRepository, never()).getClusterInfoByClusterId(anyString(), anyBoolean());
        verify(instanceTypeConfigurationProperties, never()).getGpuCountForInstanceType(anyString());
    }

    // Fall back logic success cases
    @Test
    void sendGpuUsageEventForRunningInstance_WhenInstanceCreatedBeforePreviousJob() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousTime = currentTime.minus(1, ChronoUnit.HOURS);
        Instant creationTime = previousTime.minus(30, ChronoUnit.MINUTES);
        
        InstanceV2Entity instance = createTestInstance(
                "test-instance-1",
                "test-request-1",
                creationTime,
                "OCI.GPU.H100_4x.3x",
                "H100",
                SpotInstanceInternalState.RUNNING,
                currentTime
        );

        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        when(clusterRepository.getClusterInfoByClusterId(anyString(), anyBoolean()))
                .thenReturn(Optional.of(createTestCluster(ClusterProviderEnum.AWS)));
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("OCI.GPU.H100_4x.3x", "H100"));

        // Act
        gpuUsageEventService.sendGpuUsageEventForRunningInstance(
                instance, currentTime, previousTime, cloudProviderCache);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.GPU_USAGE_PER_INSTANCE.toString(), metric.getEventName());
        assertEquals(instance.getInstanceId(), metric.getInstanceId());
        assertEquals(instance.getNcaId(), metric.getNcaId());
        assertEquals(instance.getGpu(), metric.getGpuName());
        assertEquals(instance.getInstanceType(), metric.getInstanceType());
        assertEquals(instance.getInstanceStateName().getStateName(), metric.getInstanceState());
        assertEquals(DUMMY_FUNCTION_NAME, metric.getFunctionName());

        // Should be time between previous job and current job (1 hour = 1.0 hours) * 12(gpuCount)
        assertEquals(12.0, metric.getGpuUsageInHours(), 0.01);
    }

    @Test
    void sendGpuUsageEventForRunningInstance_WhenInstanceCreatedAfterPreviousJob() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousTime = currentTime.minus(1, ChronoUnit.HOURS);
        Instant creationTime = previousTime.plus(30, ChronoUnit.MINUTES);

        InstanceV2Entity instance = createTestInstance(
                "test-instance-2",
                "test-request-2",
                creationTime,
                "AZURE.GPU.A100_4x",
                "A100",
                SpotInstanceInternalState.RUNNING,
                currentTime
        );

        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        when(clusterRepository.getClusterInfoByClusterId(anyString(), anyBoolean()))
                .thenReturn(Optional.of(createTestCluster(ClusterProviderEnum.AZURE)));
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForTasks("AZURE.GPU.A100_4x", "A100"));

        // Act
        gpuUsageEventService.sendGpuUsageEventForRunningInstance(
                instance, currentTime, previousTime, cloudProviderCache);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.GPU_USAGE_PER_INSTANCE.toString(), metric.getEventName());
        assertEquals(instance.getInstanceId(), metric.getInstanceId());
        assertEquals(instance.getNcaId(), metric.getNcaId());
        assertEquals(instance.getGpu(), metric.getGpuName());
        assertEquals(instance.getInstanceType(), metric.getInstanceType());
        assertEquals(instance.getInstanceStateName().getStateName(), metric.getInstanceState());
        assertEquals(DUMMY_TASK_NAME, metric.getTaskName());

        // Should be time between instance creation and current job (30 minutes = 0.5 hours) * 4(gpuCount)
        assertEquals(2.0, metric.getGpuUsageInHours(), 0.01);
    }

    @Test
    void sendGpuUsageEventForRunningInstance_WhenInstanceCreatedAtPreviousJob() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousTime = currentTime.minus(1, ChronoUnit.HOURS);
        Instant creationTime = previousTime; // Instance created exactly at previous job time

        InstanceV2Entity instance = createTestInstance(
                "test-instance-3",
                "test-request-3",
                creationTime,
                "dummy_gpu_4.large",
                "DUMMY_GPU_4",
                SpotInstanceInternalState.RUNNING,
                currentTime
        );

        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        when(clusterRepository.getClusterInfoByClusterId(anyString(), anyBoolean()))
                .thenReturn(Optional.of(createTestCluster(ClusterProviderEnum.OCI)));
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("dummy_gpu_4.large", "DUMMY_GPU_4"));
        when(instanceTypeConfigurationProperties.getGpuCountForInstanceType("dummy_gpu_4.large")).thenReturn(1);

        // Act
        gpuUsageEventService.sendGpuUsageEventForRunningInstance(
                instance, currentTime, previousTime, cloudProviderCache);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.GPU_USAGE_PER_INSTANCE.toString(), metric.getEventName());
        assertEquals(instance.getInstanceId(), metric.getInstanceId());
        assertEquals(instance.getNcaId(), metric.getNcaId());
        assertEquals(instance.getGpu(), metric.getGpuName());
        assertEquals(instance.getInstanceType(), metric.getInstanceType());
        assertEquals(instance.getInstanceStateName().getStateName(), metric.getInstanceState());
        assertEquals(DUMMY_FUNCTION_NAME, metric.getFunctionName());

        // Should be time between previous job and current job (1 hour = 1.0 hours) * 1(gpuCount)
        assertEquals(1.0, metric.getGpuUsageInHours(), 0.01);
    }

    @Test
    void sendGpuUsageEventForTerminatedInstance_WhenInstanceCreatedBeforePreviousJob() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousJobTime = currentTime.truncatedTo(ChronoUnit.HOURS);
        when(gpuUsageEventServiceHelper.getInstantNow()).thenReturn(currentTime);

        Instant creationTime = previousJobTime.minus(30, ChronoUnit.MINUTES);
        Instant terminationTime = previousJobTime.plus(15, ChronoUnit.MINUTES);

        InstanceV2Entity instance = createTestInstance(
                "test-instance-1",
                "test-request-1",
                creationTime,
                "OCI.GPU.H100_4x",
                "test-gpu",
                SpotInstanceInternalState.TERMINATED,
                terminationTime
        );

        when(clusterRepository.getClusterInfoByClusterId(anyString(), anyBoolean()))
                .thenReturn(Optional.of(createTestCluster(ClusterProviderEnum.AWS)));
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("OCI.GPU.H100_4x", "OCI"));

        // Act
        gpuUsageEventService.sendGpuUsageEventForTerminatedInstance(instance);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.GPU_USAGE_PER_INSTANCE.toString(), metric.getEventName());
        assertEquals(DUMMY_FUNCTION_NAME, metric.getFunctionName());

        // Should be time between previous job and termination (15 minutes = 0.25 hours) * 4(gpuCount)
        assertEquals(1, metric.getGpuUsageInHours(), 0.01);
    }

    @Test
    void sendGpuUsageEventForTerminatedInstance_WhenInstanceCreatedAfterPreviousJob() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousJobTime = currentTime.truncatedTo(ChronoUnit.HOURS);
        when(gpuUsageEventServiceHelper.getInstantNow()).thenReturn(currentTime);

        Instant creationTime = previousJobTime.plus(10, ChronoUnit.MINUTES);
        Instant terminationTime = previousJobTime.plus(15, ChronoUnit.MINUTES);

        InstanceV2Entity instance = createTestInstance(
                "test-instance-2",
                "test-request-2",
                creationTime,
                "AZURE.GPU.A100_4x.3x",
                "test-gpu",
                SpotInstanceInternalState.TERMINATED,
                terminationTime
        );

        when(clusterRepository.getClusterInfoByClusterId(anyString(), anyBoolean()))
                .thenReturn(Optional.of(createTestCluster(ClusterProviderEnum.AZURE)));
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("AZURE.GPU.A100_4x.3x", "A100"));

        // Act
        gpuUsageEventService.sendGpuUsageEventForTerminatedInstance(instance);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.GPU_USAGE_PER_INSTANCE.toString(), metric.getEventName());
        assertEquals(DUMMY_FUNCTION_NAME, metric.getFunctionName());

        // Should be time between creation and termination (5 minutes ≈ 0.08 hours) * 12 (gpuCount)
        assertEquals(0.96, metric.getGpuUsageInHours(), 0.01);
    }

    @Test
    void sendGpuUsageEventForTerminatedInstance_WhenInstanceCreatedAndTerminatedInSameHour() {
        // Arrange
        Instant currentTime = Instant.now();
        Instant previousJobTime = currentTime.truncatedTo(ChronoUnit.HOURS);
        when(gpuUsageEventServiceHelper.getInstantNow()).thenReturn(currentTime);

        Instant creationTime = previousJobTime.plus(5, ChronoUnit.MINUTES);
        Instant terminationTime = previousJobTime.plus(10, ChronoUnit.MINUTES);

        InstanceV2Entity instance = createTestInstance(
                "test-instance-3",
                "test-request-3",
                creationTime,
                "dummy_gpu_4.large",
                "DUMMY_GPU_4",
                SpotInstanceInternalState.TERMINATED,
                terminationTime
        );

        when(clusterRepository.getClusterInfoByClusterId(anyString(), anyBoolean()))
                .thenReturn(Optional.of(createTestCluster(ClusterProviderEnum.OCI)));
        when(gpuUsageEventServiceHelper.parseRequestInfo(anyString()))
                .thenReturn(dummyLaunchSpecificationForFunction("dummy_gpu_4.large", "DUMMY_GPU_4"));
        when(instanceTypeConfigurationProperties.getGpuCountForInstanceType("dummy_gpu_4.large")).thenReturn(1);

        // Act
        gpuUsageEventService.sendGpuUsageEventForTerminatedInstance(instance);

        // Assert
        ArgumentCaptor<List<GenericMetric>> metricsCaptor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());

        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(Events.GPU_USAGE_PER_INSTANCE.toString(), metric.getEventName());
        assertEquals(DUMMY_FUNCTION_NAME, metric.getFunctionName());

        // Should be time between creation and termination (5 minutes ≈ 0.08 hours) * 1 (gpuCount)
        assertEquals(0.08, metric.getGpuUsageInHours(), 0.01);
    }

    @Test
    void findCloudProvider_useCloudProviderFromDb_returnSuccess() {
        // Arrange
        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        InstanceV2Entity testInstance = createTestInstance(
                "test-instance",
                "test-request",
                Instant.now(),
                "test-instance-type",
                "H100",
                SpotInstanceInternalState.RUNNING,
                null
        );
        testInstance.setZone(TestUtil.DUMMY_CLUSTER_ID);
        testInstance.setCloudProvider(CloudProvider.OCI.toString());

        // Act
        CloudProvider result = gpuUsageEventService.findCloudProvider(cloudProviderCache, testInstance);

        // Assert
        assertNotNull(result);
        assertEquals(CloudProvider.OCI, result);
    }

    @Test
    void findCloudProvider_useCloudProviderFromDb_failedToFindEnumValue() {
        // Arrange
        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        InstanceV2Entity testInstance = createTestInstance(
                "test-instance",
                "test-request",
                Instant.now(),
                "test-instance-type",
                "H100",
                SpotInstanceInternalState.RUNNING,
                null
        );
        testInstance.setZone(TestUtil.DUMMY_CLUSTER_ID);
        testInstance.setCloudProvider("DUMMY_VALUE");

        // Act
        CloudProvider result = gpuUsageEventService.findCloudProvider(cloudProviderCache, testInstance);

        // Assert
        assertNotNull(result);
        assertEquals(CloudProvider.UNKNOWN, result);
    }

    // findCloudProvider fall back logic success cases
    @Test
    void findCloudProvider_ShouldReturnCorrectProvider() {
        // Arrange
        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        InstanceV2Entity testInstance = createTestInstance(
                "test-instance",
                "test-request",
                Instant.now(),
                "test-instance-type",
                "H100",
                SpotInstanceInternalState.RUNNING,
                null
        );
        testInstance.setZone(TestUtil.DUMMY_CLUSTER_ID);
        
        ClusterEntity cluster = createTestCluster(ClusterProviderEnum.AWS);
        when(clusterRepository.getClusterInfoByClusterId(TestUtil.DUMMY_CLUSTER_ID, true))
                .thenReturn(Optional.of(cluster));

        // Act
        CloudProvider result = gpuUsageEventService.findCloudProvider(cloudProviderCache, testInstance);

        // Assert
        assertNotNull(result);
        assertEquals(CloudProvider.getCloudProviderFromClusterProvider(cluster.getClusterProvider()), result);
    }

    @Test
    void findCloudProvider_ShouldUseCache() {
        // Arrange
        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        ClusterProviderEnum cachedProvider = ClusterProviderEnum.AWS;
        cloudProviderCache.put(TestUtil.DUMMY_CLUSTER_ID, cachedProvider);
        
        InstanceV2Entity testInstance = createTestInstance(
                "test-instance",
                "test-request",
                Instant.now(),
                "test-instance-type",
                "H100",
                SpotInstanceInternalState.RUNNING,
                null
        );
        testInstance.setZone(TestUtil.DUMMY_CLUSTER_ID);

        // Act
        CloudProvider result = gpuUsageEventService.findCloudProvider(cloudProviderCache, testInstance);

        // Assert
        assertNotNull(result);
        assertEquals(CloudProvider.getCloudProviderFromClusterProvider(cachedProvider), result);
        verify(clusterRepository, never()).getClusterInfoByClusterId(anyString(), anyBoolean());
    }

    @Test
    void findCloudProvider_ShouldHandleNullProvider() {
        // Arrange
        Map<String, ClusterProviderEnum> cloudProviderCache = new HashMap<>();
        InstanceV2Entity testInstance = createTestInstance(
                "test-instance",
                "test-request",
                Instant.now(),
                "test-instance-type",
                "H100",
                SpotInstanceInternalState.RUNNING,
                null
        );
        testInstance.setZone(TestUtil.DUMMY_CLUSTER_ID);

        when(clusterRepository.getClusterInfoByClusterId(TestUtil.DUMMY_CLUSTER_ID, true))
                .thenReturn(Optional.empty());

        // Act
        CloudProvider result = gpuUsageEventService.findCloudProvider(cloudProviderCache, testInstance);

        // Assert
        assertNull(result);
    }

    @Test
    void getGpuCountFromInstanceType_ShouldHandleComplexInstanceTypes() {
        // Test various complex instance type formats
        assertEquals(12, gpuUsageEventService.getGpuCountFromInstanceType("OCI.GPU.H100_4x.x3"));
        assertEquals(8, gpuUsageEventService.getGpuCountFromInstanceType("instance_2x.x4"));
        assertEquals(2, gpuUsageEventService.getGpuCountFromInstanceType("instance_2x"));
        assertEquals(2, gpuUsageEventService.getGpuCountFromInstanceType("instance_x2"));
        assertEquals(1, gpuUsageEventService.getGpuCountFromInstanceType("instance"));
        assertEquals(1, gpuUsageEventService.getGpuCountFromInstanceType("instance_without_numbers"));
    }

    @Test
    void getConfiguredGpuCount_ShouldReturnCorrectGpuCount() {
        // Arrange
        when(instanceTypeConfigurationProperties.getGpuCountForInstanceType("dummy_gpu_1.xlarge")).thenReturn(1);
        when(instanceTypeConfigurationProperties.getGpuCountForInstanceType("dummy_gpu_5.xlarge")).thenReturn(2);
        when(instanceTypeConfigurationProperties.getGpuCountForInstanceType("unknown_instance_type")).thenReturn(1);

        // Act & Assert
        assertEquals(1, gpuUsageEventService.getConfiguredGpuCount("dummy_gpu_1.xlarge"));
        assertEquals(2, gpuUsageEventService.getConfiguredGpuCount("dummy_gpu_5.xlarge"));
        assertEquals(1, gpuUsageEventService.getConfiguredGpuCount("unknown_instance_type"));
        
        // Verify all interactions with the mock
        verify(instanceTypeConfigurationProperties, times(3)).getGpuCountForInstanceType(anyString());
    }

    private InstanceV2Entity createTestInstance(
            String instanceId,
            String requestId,
            Instant creationTime,
            String instanceType,
            String gpu,
            SpotInstanceInternalState state,
            Instant updateTime) {
        return InstanceV2Entity.builder()
                .instanceId(instanceId)
                .requestId(requestId)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(creationTime))
                .zone("test-zone")
                .instanceType(instanceType)
                .gpu(gpu)
                .ncaId("test-nca")
                .requestRawData("test-request-data")
                .instanceStateName(state)
                .instanceUpdateTime(updateTime)
                .build();
    }

    private ClusterEntity createTestCluster(ClusterProviderEnum clusterProviderEnum) {
        return ClusterEntity.builder()
                .clusterId(TestUtil.DUMMY_CLUSTER_ID)
                .clusterProvider(clusterProviderEnum)
                .build();
    }

    private ClientRequestDataModel dummyLaunchSpecificationForFunction(String instanceType, String gpu) {
        LaunchSpecification launch = LaunchSpecification.builder()
                .functionId(UUID.randomUUID().toString())
                .versionId(UUID.randomUUID().toString())
                .instanceType(instanceType)
                .functionName(DUMMY_FUNCTION_NAME)
                .ncaId("test-nca")
                .gpu(gpu)
                .build();
        return ClientRequestDataModel.builder()
                .launchSpecification(launch)
                .build();
    }

    private ClientRequestDataModel dummyLaunchSpecificationForTasks(String instanceType, String gpu) {
        LaunchSpecification launch = LaunchSpecification.builder()
                .taskId(UUID.randomUUID().toString())
                .maxRuntimeDuration("2H")
                .instanceType(instanceType)
                .taskName(DUMMY_TASK_NAME)
                .ncaId("test-nca")
                .gpu(gpu)
                .build();
        return ClientRequestDataModel.builder()
                .launchSpecification(launch)
                .build();
    }
}