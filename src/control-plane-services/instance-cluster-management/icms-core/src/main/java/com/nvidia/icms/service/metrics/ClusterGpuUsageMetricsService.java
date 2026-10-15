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

import static com.nvidia.icms.service.metrics.MetricsConstants.METER_GPU_AVAILABLE_COUNT;
import static com.nvidia.icms.service.metrics.MetricsConstants.METER_GPU_OCCUPIED_COUNT;
import static com.nvidia.icms.service.metrics.MetricsConstants.METER_GPU_TOTAL_COUNT;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_CLOUD_PROVIDER;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_CLUSTER_ID;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_CLUSTER_NAME;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_GPU_TYPE;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_NCA_ID;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_NVCA_VERSION;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_REGION;
import static java.time.Duration.ofMinutes;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.observation.annotation.Observed;
import lombok.Builder;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
public class ClusterGpuUsageMetricsService  {

    private final MeterRegistry meterRegistry;

    @Builder
    public record GpuUsageKey(String clusterName, String gpuType, String ncaId,
                              String clusterGroupName, String region, ClusterProviderEnum cloudProvider,
                              String nvcaVersion, String clusterId) { }

    private final Cache<GpuUsageKey, Integer> gpuTotalValues;
    private final Cache<GpuUsageKey, Integer> gpuAvailableValues;
    private final Cache<GpuUsageKey, Integer> gpuOccupiedValues;
    private final LoadingCache<GpuUsageKey, Gauge> gpuTotalGauges;
    private final LoadingCache<GpuUsageKey, Gauge> gpuAvailableGauges;
    private final LoadingCache<GpuUsageKey, Gauge> gpuOccupiedGauges;

    public ClusterGpuUsageMetricsService(MeterRegistry meterRegistry) {
        this.meterRegistry = meterRegistry;

        // Gauge for total counts
        this.gpuTotalGauges = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(3)) // Optional: Remove unused gauges
                .build(this::createTotalGpuGauge);

        this.gpuTotalValues = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(3))
                .scheduler(Scheduler.systemScheduler())
                .build();

        // Gauge for available counts
        this.gpuAvailableGauges = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(3)) // Optional: Remove unused gauges
                .build(this::createAvailableGpuGauge);

        gpuAvailableValues = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(3))
                .scheduler(Scheduler.systemScheduler())
                .build();

        // Gauge for occupied counts
        this.gpuOccupiedGauges = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(3)) // Optional: Remove unused gauges
                .build(this::createOccupiedGpuGauge);

        gpuOccupiedValues = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(3))
                .scheduler(Scheduler.systemScheduler())
                .build();

    }

    private Gauge createTotalGpuGauge(GpuUsageKey key) {

        return Gauge.builder(METER_GPU_TOTAL_COUNT, () -> gpuTotalValues.getIfPresent(key))
                .tag(TAG_CLUSTER_NAME, key.clusterName())
                .tag(TAG_GPU_TYPE, key.gpuType())
                .tag(TAG_REGION, key.region())
                .tag(TAG_NCA_ID, key.ncaId())
                .tag(TAG_CLOUD_PROVIDER, key.cloudProvider().toString())
                .tag(TAG_CLUSTER_GROUP_NAME, key.clusterGroupName())
                .tag(TAG_NVCA_VERSION, key.nvcaVersion())
                .tag(TAG_CLUSTER_ID, key.clusterId())
                .description("Total GPU count for given cluster and GPU type")
                .register(meterRegistry);
    }

    private Gauge createAvailableGpuGauge(GpuUsageKey key) {

        return Gauge.builder(METER_GPU_AVAILABLE_COUNT, () -> gpuAvailableValues.getIfPresent(key))
                .tag(TAG_CLUSTER_NAME, key.clusterName())
                .tag(TAG_GPU_TYPE, key.gpuType())
                .tag(TAG_REGION, key.region())
                .tag(TAG_NCA_ID, key.ncaId())
                .tag(TAG_CLOUD_PROVIDER, key.cloudProvider().toString())
                .tag(TAG_CLUSTER_GROUP_NAME, key.clusterGroupName())
                .tag(TAG_NVCA_VERSION, key.nvcaVersion())
                .tag(TAG_CLUSTER_ID, key.clusterId())
                .description("Available GPU count for given cluster and GPU type")
                .register(meterRegistry);
    }

    private Gauge createOccupiedGpuGauge(GpuUsageKey key) {

        return Gauge.builder(METER_GPU_OCCUPIED_COUNT, () -> gpuOccupiedValues.getIfPresent(key))
                .tag(TAG_CLUSTER_NAME, key.clusterName())
                .tag(TAG_GPU_TYPE, key.gpuType())
                .tag(TAG_REGION, key.region())
                .tag(TAG_NCA_ID, key.ncaId())
                .tag(TAG_CLOUD_PROVIDER, key.cloudProvider().toString())
                .tag(TAG_CLUSTER_GROUP_NAME, key.clusterGroupName())
                .tag(TAG_NVCA_VERSION, key.nvcaVersion())
                .tag(TAG_CLUSTER_ID, key.clusterId())
                .description("Occupied GPU count for given cluster and GPU type")
                .register(meterRegistry);
    }

    /**
     * OTEL Gauge has async nature: on building a metric we have to provide a function that will
     * return actual values to record. We introduce a cache and store real values there. The
     * metric is getting values from the cache. We keep our metrics in another async loading
     * cache. The metric is created on the first request to metric cache. Therefore, we need to
     * call metric cache on each metric record to make sure the metric is created.
     * @param clusterName clusterName to record a metrics
     * @param gpuType gpuType to record a metric
     * @param count the actual count provided by NVCA in health
     */
    @Observed
    public void recordTotalGpus(
            String clusterName, String gpuType,
            Integer count, ClusterEntity clusterEntity) {
        GpuUsageKey key = GpuUsageKey.builder()
                .clusterName(clusterName)
                .gpuType(gpuType)
                .ncaId(clusterEntity.getNcaId())
                .region(clusterEntity.getRegion())
                .cloudProvider(clusterEntity.getClusterProvider())
                .nvcaVersion(clusterEntity.getNvcaVersion())
                .clusterGroupName(clusterEntity.getClusterGroupName())
                .clusterId(clusterEntity.getClusterId())
                .build();

        // Put latest total count for key in gpuTotalValues
        gpuTotalValues.put(key, count);

        // Create and register gauge if it doesn't exists
        Gauge unusedGauge = gpuTotalGauges.get(key);
    }

    @Observed
    public void recordAvailableGpus(
            String clusterName, String gpuType,
            Integer count, ClusterEntity clusterEntity) {
        GpuUsageKey key = GpuUsageKey.builder()
                .clusterName(clusterName)
                .gpuType(gpuType)
                .ncaId(clusterEntity.getNcaId())
                .region(clusterEntity.getRegion())
                .cloudProvider(clusterEntity.getClusterProvider())
                .nvcaVersion(clusterEntity.getNvcaVersion())
                .clusterGroupName(clusterEntity.getClusterGroupName())
                .clusterId(clusterEntity.getClusterId())
                .build();

        // Put latest available count for key in gpuAvailableValues
        gpuAvailableValues.put(key, count);

        // Create and register gauge if it doesn't exists
        Gauge unusedGauge = gpuAvailableGauges.get(key);
    }

    @Observed
    public void recordOccupiedGpus(
            String clusterName, String gpuType,
            Integer count, ClusterEntity clusterEntity) {
        GpuUsageKey key = GpuUsageKey.builder()
                .clusterName(clusterName)
                .gpuType(gpuType)
                .ncaId(clusterEntity.getNcaId())
                .region(clusterEntity.getRegion())
                .cloudProvider(clusterEntity.getClusterProvider())
                .nvcaVersion(clusterEntity.getNvcaVersion())
                .clusterGroupName(clusterEntity.getClusterGroupName())
                .clusterId(clusterEntity.getClusterId())
                .build();

        // Put latest occupied count for key in gpuOccupiedValues
        gpuOccupiedValues.put(key, count);

        // Create and register gauge if it doesn't exists
        Gauge unusedGauge = gpuOccupiedGauges.get(key);
    }

}
