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
package com.nvidia.icms.service.byoc;

import static com.nvidia.icms.scheduled.dbcache.WildCardNcaIdCacheTaskController.WILD_CARD_NCA_ID_CACHE_TASK_NAME;
import static com.nvidia.icms.service.byoc.ClustersService.toReadyClusterInfo;
import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.isClusterTargetingEnabled;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class WildCardAllowedClustersCacheService {

    private record ClusterInfoSnapshot(Set<ClusterByGroupIdAndIdEntity> clusterInfo, Instant lastUpdated) {}

    private final NvcaClusterRepository nvcaClusterRepository;

    private final ClusterRepository clusterRepository;

    private final AtomicReference<ClusterInfoSnapshot> cachedClusterInfo = new AtomicReference<>();

    private final TelemetryEventClient telemetryEventClient;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    // This function will be invoked from a scheduled task to refresh the cache
    @Observed
    public void refreshCache() {
        Set<ClusterByGroupIdAndIdEntity> readyClusterInfo = getReadyClustersForWildCardNcaId();
        ClusterInfoSnapshot snapshot = new ClusterInfoSnapshot(readyClusterInfo,
                                                               Instant.now());
        cachedClusterInfo.set(snapshot);
    }

    /**
     * This function returns cached cluster info for wildcard ncaIdKey
     * This function doesn't check cloudHealth status of the cluster,
     * calling function should add validation based on usecase of cloud health check
     * @return Set of ReadyClusterInfo
     */
    @Observed
    public Set<ClusterByGroupIdAndIdEntity> getCachedReadyClusters() {

        ClusterInfoSnapshot latestSnapshot = cachedClusterInfo.get();

        if (!isSnapShotNull(latestSnapshot) &&
                getLastUpdatedTimeInSec(latestSnapshot)
                        <= icmsConfigurationProperties.getWildCardStaleCachedDataValidDurationInSec()) {
            // Cached data is fresh
            return latestSnapshot.clusterInfo();
        }

        // Cache is stale, making explicit DB calls to fetch data
        log.info("{} event: cached data is stale, calling DB explicitly to cluster information, isCachedDataNull {} lastUpdated {}",
                 WILD_CARD_NCA_ID_CACHE_TASK_NAME, isSnapShotNull(latestSnapshot), getLastUpdatedTimeInSec(latestSnapshot));

        Map<String, Object> metadata = new HashMap<>();
        metadata.put(EventMetaData.IS_CACHE_DATA_STALE.getName(), isSnapShotNull(latestSnapshot));
        metadata.put(EventMetaData.LAST_UPDATED_TIME.getName(), getLastUpdatedTimeInSec(latestSnapshot));
        metadata.put(EventMetaData.THREAD_NAME.getName(), Thread.currentThread().getName());
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withMetadata(metadata)
                                                           .withEventName(
                                                                   Events.WILD_CARD_NCA_ID_STALE_CACHE_DATA.toString())));
        return getReadyClustersForWildCardNcaId();
    }

    private Set<ClusterByGroupIdAndIdEntity> getReadyClustersForWildCardNcaId() {

        List<ClustersByAuthorizedAccountsEntity> allClustersInAuthorizedAccount =
                nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);

        Set<String> processedClusterGroups = new HashSet<>();
        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        for (ClustersByAuthorizedAccountsEntity authorizedAccountsEntity : allClustersInAuthorizedAccount) {

            // If cluster group is already processed, then skipping it
            if (processedClusterGroups.contains(authorizedAccountsEntity.getClusterGroupId())) {
                continue;
            }

            // Fetching ready clusters from cluster group
            processedClusterGroups.add(authorizedAccountsEntity.getClusterGroupId());
            Set<ClusterByGroupIdAndIdEntity> allReadyClusters = getAllReadyClustersFromClusterGroup(
                    authorizedAccountsEntity.getClusterGroupId());
            readyClusterEntities.addAll(allReadyClusters);
        }

        return readyClusterEntities;
    }


    private Set<ClusterByGroupIdAndIdEntity> getAllReadyClustersFromClusterGroup(String clusterGroupId) {
        return clusterRepository.getClustersFromClusterGroup(clusterGroupId)
                .stream()
                .filter(entity -> ClusterStatusEnum.READY.equals(entity.getClusterStatus())
                        && entity.getNvcaVersion() != null
                        && isClusterTargetingEnabled(entity.getAllowClusterTargeting())
                        && !isSetEmptyOrNull(NvcaConverter.getGpusV5(entity)))
                .collect(Collectors.toSet());
    }

    private long getLastUpdatedTimeInSec(ClusterInfoSnapshot latestSnapshot) {
        if (latestSnapshot != null) {
            return Duration.between(latestSnapshot.lastUpdated(), Instant.now()).getSeconds();
        }
        return 0;
    }

    private boolean isSnapShotNull(ClusterInfoSnapshot latestSnapshot) {
        return latestSnapshot == null;
    }
}
