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

import com.nvidia.icms.configuration.bean.DbConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import java.util.Map;
import java.util.Optional;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

class CloudHealthRepositoryTest extends IntegrationTest {

    private final String ZONE1 = "ZONE1";

    @Autowired
    private CloudHealthRepository cloudHealthRepository;

    @Autowired
    private DbConfigurationProperties dbConfigurationProperties;

    @Test
    void findByCloudAndZone_successCase()
            throws InterruptedException {

        // insert
        int ttl = 1;
        CloudHealthEntity entity = CloudHealthEntity.builder()
                .key(CloudHealthKey.builder()
                             .cloudProvider(ResourceProvider.BYOC)
                             .zone(ZONE1)
                             .build())
                .status(CloudHealthStatus.HEALTHY)
                .gpuUsage(Map.of("gpu1", new GpuCapacity(2, 1, 1)))
                .build();

        cloudHealthRepository.insert(entity, ttl);

        Optional<CloudHealthEntity> cloudHealthEntity =
                cloudHealthRepository.findByCloudAndZone(ResourceProvider.BYOC, ZONE1);
        Assertions.assertTrue(cloudHealthEntity.isPresent());
        CloudHealthEntity entity1 = cloudHealthEntity.get();

        Assertions.assertEquals(entity1, entity);

        // Wait till ttl period
        Thread.sleep(ttl * 1000L);

        cloudHealthEntity =
                cloudHealthRepository.findByCloudAndZone(ResourceProvider.BYOC, ZONE1);
        Assertions.assertTrue(cloudHealthEntity.isEmpty());
    }

    @Test
    void findByCloudAndZone_successOverride() {

        // insert
        int ttl = 1;
        CloudHealthEntity entity = CloudHealthEntity.builder()
                .key(CloudHealthKey.builder()
                             .cloudProvider(ResourceProvider.BYOC)
                             .zone(ZONE1)
                             .build())
                .status(CloudHealthStatus.HEALTHY)
                .gpuUsage(Map.of("gpu1", new GpuCapacity(2, 1, 1)))
                .build();

        cloudHealthRepository.insert(entity, ttl);
        cloudHealthRepository.insert(entity, ttl);
        //If no exception happens, it means that override is allowed
    }
}
