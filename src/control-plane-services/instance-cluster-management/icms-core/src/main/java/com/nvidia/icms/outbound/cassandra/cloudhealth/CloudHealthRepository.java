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
package com.nvidia.icms.outbound.cassandra.cloudhealth;

import com.datastax.oss.driver.api.core.CqlSession;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.configuration.bean.DbConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import io.micrometer.observation.annotation.Observed;

import java.time.Duration;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class CloudHealthRepository {

    private final CloudHealthRepo cloudHealthRepo;

    private final ObjectMapper objectMapper;

    public void insert(CloudHealthEntity entity, int ttl) {

        logHeartBeatInsert(entity, ttl);
        if (!cloudHealthRepo.insertWithTtl(entity, Duration.ofSeconds(ttl), false)) {
            String errMsg = String.format(
                    "Failed to persist cloud %s health information for zone: %s",
                    entity.getKey().getCloudProvider(), entity.getKey().getZone());
            throw new IcmsInternalServerException(errMsg);
        }
    }

    public Optional<CloudHealthEntity> findByCloudAndZone(
            ResourceProvider resourceProvider, String zone) {
        return cloudHealthRepo.findByKeyCloudProviderAndKeyZone(resourceProvider, zone);
    }

    @Observed
    public List<CloudHealthEntity> findAll() {
        return cloudHealthRepo.findAll();
    }

    @Observed
    public Map<String, CloudHealthEntity> findAllInMap() {
        Map<String, CloudHealthEntity> cloudHealthEntityMap = new HashMap<>();
        List<CloudHealthEntity> cloudHealthEntities = cloudHealthRepo.findAll();

        for (CloudHealthEntity cloudHealth : cloudHealthEntities) {
            cloudHealthEntityMap.put(cloudHealth.getKeyZoneValue(), cloudHealth);
        }

        return cloudHealthEntityMap;
    }

    /*
     Only zones with explicitly HEALTHY status are considered healthy.
     UNHEALTHY zones are excluded immediately, not after TTL expiration.
     */
    public Set<String> finalAllHealthyZones() {
        List<CloudHealthEntity> cloudHealth = findAll();
        return cloudHealth.stream()
                .filter(CloudHealthRepository::isCloudHealthy)
                .map(CloudHealthEntity::getKeyZoneValue)
                .collect(Collectors.toSet());
    }

    /**
     * Checks if a zone is healthy based on the CloudHealthEntity status.
     * Only considers zones with explicit HEALTHY status as healthy.
     */
    public static boolean isCloudHealthy(CloudHealthEntity cloudHealthEntity) {
        return cloudHealthEntity != null && 
               cloudHealthEntity.getStatus() != null && 
               cloudHealthEntity.getStatus() == CloudHealthStatus.HEALTHY;
    }

    @Data
    @AllArgsConstructor
    public static class CompositeKeyForCloudHealth {

        ResourceProvider resourceProvider;
        String zone;
    }

    private void logHeartBeatInsert(CloudHealthEntity entity, int ttl) {
        try {
            log.info("HEART_BEAT_LOGGING: CloudHealthRepository, adding heartbeat in DB for time {}, entity {}",
                     ttl, objectMapper.writeValueAsString(entity));
        } catch (Exception exception) {
            log.error(
                    "HEART_BEAT_LOGGING: Failed to log cloudHealthEntity before writing in DB, error: {} exception: ",
                    exception.getMessage(), exception);
        }
    }
}
