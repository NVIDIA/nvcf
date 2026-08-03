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
package com.nvidia.icms.service.metrics;

import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_CLOUD_PROVIDER;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_CLUSTER_ID;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_CLUSTER_NAME;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_GPU_TYPE;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_NCA_ID;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_NVCA_VERSION;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_REGION;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;

import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;

import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ClusterGpuUsageMetricsServiceTest {

    private MeterRegistry meterRegistry;
    private ClusterGpuUsageMetricsService clusterGpuUsageMetricsService;
    private ClusterEntity clusterEntity;

    @BeforeEach
    void setUp() {
        meterRegistry = new SimpleMeterRegistry();
        clusterGpuUsageMetricsService = new ClusterGpuUsageMetricsService(meterRegistry);

        // Create a test cluster entity
        clusterEntity = ClusterEntity.builder()
                .clusterId("test-cluster-id")
                .clusterName("test-cluster")
                .clusterGroupName("test-group")
                .ncaId("test-nca-id")
                .region("us-east-1")
                .clusterProvider(ClusterProviderEnum.GDN)
                .nvcaVersion("1.0.0")
                .clusterStatus(ClusterStatusEnum.READY)
                .build();
    }

    @Test
    void recordTotalGpus_shouldRecordMetric() {
        // Arrange
        String clusterName = "test-cluster";
        String gpuType = "A100";
        Integer count = 10;

        // Act
        clusterGpuUsageMetricsService.recordTotalGpus(clusterName, gpuType, count, clusterEntity);

        // Assert
        Gauge gauge = meterRegistry.find(MetricsConstants.METER_GPU_TOTAL_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        assertNotNull(gauge);
        assertEquals(count.doubleValue(), gauge.value());
        validateGaugeTags(gauge, clusterEntity, gpuType);
    }

    @Test
    void recordAvailableGpus_shouldRecordMetric() {
        // Arrange
        String clusterName = "test-cluster";
        String gpuType = "A100";
        Integer count = 5;

        // Act
        clusterGpuUsageMetricsService.recordAvailableGpus(clusterName, gpuType, count, clusterEntity);

        // Assert
        Gauge gauge = meterRegistry.find(MetricsConstants.METER_GPU_AVAILABLE_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        assertNotNull(gauge);
        assertEquals(count.doubleValue(), gauge.value());
        validateGaugeTags(gauge, clusterEntity, gpuType);
    }

    @Test
    void recordOccupiedGpus_shouldRecordMetric() {
        // Arrange
        String clusterName = "test-cluster";
        String gpuType = "A100";
        Integer count = 3;

        // Act
        clusterGpuUsageMetricsService.recordOccupiedGpus(clusterName, gpuType, count, clusterEntity);

        // Assert
        Gauge gauge = meterRegistry.find(MetricsConstants.METER_GPU_OCCUPIED_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        assertNotNull(gauge);
        assertEquals(count.doubleValue(), gauge.value());
        validateGaugeTags(gauge, clusterEntity, gpuType);
    }

    @Test
    void recordGpuMetrics_shouldRecordAllMetrics() {
        // Arrange
        String clusterName = "test-cluster";
        String gpuType = "A100";
        Integer total = 10;
        Integer available = 5;
        Integer occupied = 3;

        // Act
        clusterGpuUsageMetricsService.recordTotalGpus(clusterName, gpuType, total, clusterEntity);
        clusterGpuUsageMetricsService.recordAvailableGpus(clusterName, gpuType, available, clusterEntity);
        clusterGpuUsageMetricsService.recordOccupiedGpus(clusterName, gpuType, occupied, clusterEntity);

        // Assert
        Gauge totalGauge = meterRegistry.find(MetricsConstants.METER_GPU_TOTAL_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        Gauge availableGauge = meterRegistry.find(MetricsConstants.METER_GPU_AVAILABLE_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        Gauge occupiedGauge = meterRegistry.find(MetricsConstants.METER_GPU_OCCUPIED_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();

        assertNotNull(totalGauge);
        assertNotNull(availableGauge);
        assertNotNull(occupiedGauge);
        assertEquals(total.doubleValue(), totalGauge.value());
        assertEquals(available.doubleValue(), availableGauge.value());
        assertEquals(occupied.doubleValue(), occupiedGauge.value());
    }

    @Test
    void recordGpuMetrics_shouldUpdateExistingGauges() {
        // Arrange
        String clusterName = "test-cluster";
        String gpuType = "A100";
        Integer initialCount = 10;
        Integer updatedCount = 15;

        // Act - Record initial value
        clusterGpuUsageMetricsService.recordTotalGpus(clusterName, gpuType, initialCount, clusterEntity);
        Gauge initialGauge = meterRegistry.find(MetricsConstants.METER_GPU_TOTAL_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        assertEquals(initialCount.doubleValue(), initialGauge.value());

        // Act - Record updated value
        clusterGpuUsageMetricsService.recordTotalGpus(clusterName, gpuType, updatedCount, clusterEntity);
        Gauge updatedGauge = meterRegistry.find(MetricsConstants.METER_GPU_TOTAL_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        assertEquals(updatedCount.doubleValue(), updatedGauge.value());
    }

    @Test
    void recordGpuMetrics_withDifferentGpuTypes_shouldCreateSeparateGauges() {
        // Arrange
        String clusterName = "test-cluster";
        String gpuType1 = "A100";
        String gpuType2 = "H100";
        Integer count = 10;

        // Act
        clusterGpuUsageMetricsService.recordTotalGpus(clusterName, gpuType1, count, clusterEntity);
        clusterGpuUsageMetricsService.recordTotalGpus(clusterName, gpuType2, count, clusterEntity);

        // Assert
        Gauge gauge1 = meterRegistry.find(MetricsConstants.METER_GPU_TOTAL_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType1)
                .gauge();
        Gauge gauge2 = meterRegistry.find(MetricsConstants.METER_GPU_TOTAL_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType2)
                .gauge();
        
        assertNotNull(gauge1);
        assertNotNull(gauge2);
        assertEquals(count.doubleValue(), gauge1.value());
        assertEquals(count.doubleValue(), gauge2.value());
    }

    @Test
    void recordGpuMetrics_shouldUpdateExistingGaugeWithNewValue() {
        // Arrange
        String clusterName = "test-cluster";
        String gpuType = "A100";
        Integer initialValue = 10;
        Integer newValue = 20;

        // Act - Record initial value
        clusterGpuUsageMetricsService.recordTotalGpus(clusterName, gpuType, initialValue, clusterEntity);
        
        // Verify initial value
        Gauge initialGauge = meterRegistry.find(MetricsConstants.METER_GPU_TOTAL_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        assertNotNull(initialGauge);
        assertEquals(initialValue.doubleValue(), initialGauge.value());
        
        // Act - Record new value
        clusterGpuUsageMetricsService.recordTotalGpus(clusterName, gpuType, newValue, clusterEntity);
        
        // Assert - Verify gauge has been updated with new value
        Gauge updatedGauge = meterRegistry.find(MetricsConstants.METER_GPU_TOTAL_COUNT)
                .tag(TAG_CLUSTER_NAME, clusterName)
                .tag(TAG_GPU_TYPE, gpuType)
                .gauge();
        assertNotNull(updatedGauge);
        assertEquals(newValue.doubleValue(), updatedGauge.value());
        
        // Verify it's the same gauge instance
        assertEquals(initialGauge.getId(), updatedGauge.getId());
    }

    private void validateGaugeTags(Gauge gauge, ClusterEntity clusterEntity, String gpuType) {
        assertEquals(clusterEntity.getClusterName(), gauge.getId().getTag(TAG_CLUSTER_NAME));
        assertEquals(gpuType, gauge.getId().getTag(TAG_GPU_TYPE));
        assertEquals(clusterEntity.getRegion(), gauge.getId().getTag(TAG_REGION));
        assertEquals(clusterEntity.getNcaId(), gauge.getId().getTag(TAG_NCA_ID));
        assertEquals(clusterEntity.getClusterProvider().toString(), gauge.getId().getTag(TAG_CLOUD_PROVIDER));
        assertEquals(clusterEntity.getClusterGroupName(), gauge.getId().getTag(TAG_CLUSTER_GROUP_NAME));
        assertEquals(clusterEntity.getNvcaVersion(), gauge.getId().getTag(TAG_NVCA_VERSION));
        assertEquals(clusterEntity.getClusterId(), gauge.getId().getTag(TAG_CLUSTER_ID));
    }
} 