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
package com.nvidia.icms.outbound.cassandra.instance;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.RUNNING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.STARTING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.getStateCode;
import static com.nvidia.icms.outbound.cassandra.instance.InstanceConverter.toInstanceByZoneEntity;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;

import com.nvidia.icms.configuration.bean.DbConfigurationProperties;
import com.nvidia.icms.factory.InstanceEntityFactory;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneKey;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.util.CopyUtil;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import jakarta.annotation.Nullable;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

class InstancePerZoneRepositoryTest extends IntegrationTest {

    @Autowired
    private InstancePerZoneRepository instancePerZoneRepository;

    @Autowired
    private DbConfigurationProperties dbConfigurationProperties;

    @Autowired
    private InstanceV2Repo instanceV2Repo;

    private final String STARTING_INSTANCE_ID = "starting_instance_id";

    private final String RUNNING_INSTANCE_ID = "running_instance_id";

    private final String SHUTTING_DOWN_INSTANCE_ID = "shutting_down_instance_id";

    private final String TERMINATED_INSTANCE_ID = "terminated_instance_id";

    private final String ZONE_1 = "cluster_id_1";

    private final String ZONE_2 = "cluster_id_2";

    @BeforeEach
    void setup() {
        insertEntriesInDb();
    }

    @Test
    void findAllActiveInstancesByZone_withValidInputs_returnsSuccess() {

        // Act
        List<InstanceByZoneEntity> response =
                instancePerZoneRepository.findAllActiveInstancesByZone(ZONE_1);

        // Assert
        Assertions.assertEquals(3, response.size());
        Set<String> expectedInstanceIds =
                Set.of(STARTING_INSTANCE_ID, RUNNING_INSTANCE_ID, SHUTTING_DOWN_INSTANCE_ID);
        for (InstanceByZoneEntity entity : response) {
            Assertions.assertTrue(expectedInstanceIds.contains(entity.getKey().getInstanceId()));
        }
    }

    @Test
    void findByZoneAndInstanceId_withValidInputs_returnsSuccess() {

        // Act
        Optional<InstanceByZoneEntity> optionalResponse1 =
                instancePerZoneRepository.findByZoneAndInstanceId(ZONE_2, STARTING_INSTANCE_ID);

        // Assert
        Assertions.assertTrue(optionalResponse1.isPresent());
        InstanceByZoneEntity response = optionalResponse1.get();
        Assertions.assertEquals(STARTING_INSTANCE_ID, response.getKey().getInstanceId());
        Assertions.assertEquals(STARTING, response.getInstanceStateName());

        // Act
        InstanceByZoneEntity responseToUpdate = CopyUtil.deepCopy(response);
        responseToUpdate.setInstanceStateName(SpotInstanceInternalState.RUNNING);
        instancePerZoneRepository.update(responseToUpdate);

        Optional<InstanceByZoneEntity> optionalResponse2 =
                instancePerZoneRepository.findByZoneAndInstanceId(ZONE_2, STARTING_INSTANCE_ID);

        // Assert
        Assertions.assertTrue(optionalResponse2.isPresent());
        InstanceByZoneEntity response2 = optionalResponse2.get();
        Assertions.assertEquals(STARTING_INSTANCE_ID, response.getKey().getInstanceId());
        Assertions.assertEquals(RUNNING, response2.getInstanceStateName());
    }

    private void insertEntriesInDb() {
        InstanceV2Entity startingInstance_1 =
                getInstanceV2Entity(SpotInstanceRequestState.ACTIVE,
                                            SpotInstanceInternalState.STARTING, ZONE_1);

        InstanceV2Entity runningInstance_1 =
                getInstanceV2Entity(SpotInstanceRequestState.ACTIVE,
                                            SpotInstanceInternalState.RUNNING, ZONE_1);

        InstanceV2Entity shuttingDownInstance_1 =
                getInstanceV2Entity(SpotInstanceRequestState.ACTIVE,
                                            SpotInstanceInternalState.SHUTTING_DOWN, ZONE_1);

        InstanceV2Entity terminatedInstance_1 =
                getInstanceV2Entity(SpotInstanceRequestState.CLOSED,
                                            SpotInstanceInternalState.TERMINATED, ZONE_1);

        InstanceV2Entity startingInstance_2 =
                getInstanceV2Entity(SpotInstanceRequestState.ACTIVE,
                                            SpotInstanceInternalState.STARTING, ZONE_2);

        instancePerZoneRepository.insert(toInstanceByZoneEntity(startingInstance_1));
        instancePerZoneRepository.insert(toInstanceByZoneEntity(runningInstance_1));
        instancePerZoneRepository.insert(toInstanceByZoneEntity(shuttingDownInstance_1));
        instancePerZoneRepository.insert(toInstanceByZoneEntity(terminatedInstance_1));
        instancePerZoneRepository.insert(toInstanceByZoneEntity(startingInstance_2));

        instanceV2Repo.insert(startingInstance_1);
        instanceV2Repo.insert(runningInstance_1);
        instanceV2Repo.insert(shuttingDownInstance_1);
        instanceV2Repo.insert(startingInstance_2);
    }

    private InstanceV2Entity getInstanceV2Entity(
            SpotInstanceRequestState requestState,
            SpotInstanceInternalState instanceStateName,
            String zoneName) {

        String instanceId = null;
        switch (instanceStateName) {
            case STARTING -> instanceId = STARTING_INSTANCE_ID;
            case TERMINATED -> instanceId = TERMINATED_INSTANCE_ID;
            case RUNNING -> instanceId = RUNNING_INSTANCE_ID;
            case SHUTTING_DOWN -> instanceId = SHUTTING_DOWN_INSTANCE_ID;
        }

        return InstanceV2Entity.builder()
                .createTimeuuid(TimeUtils.getTimeUuidNow())
                .instanceId(instanceId)
                .zone(zoneName)
                .customer(DUMMY_CLUSTER_ID)
                .requestId(DUMMY_REQUEST_ID)
                .requestState(requestState)
                .requestStatusMessage(
                        String.format("Request is updated to %s state", requestState.toString()))
                .requestStatusUpdateTime(Instant.now())
                .requestStatusCode(SpotInstanceStatus.FULFILLED)
                .instanceStateName(instanceStateName)
                .instanceStateCode(getStateCode(instanceStateName))
                .imageId(DUMMY_CONTAINER_IMAGE)
                .instanceUpdateTime(Instant.now())
                .resourceProvider(ResourceProvider.BYOC)
                .errorLog(null)
                .ncaId(DUMMY_BYOC_NCA_ID)
                .instanceType(DUMMY_BYOC_INSTANCE_TYPE)
                .backend(CloudProvider.AZURE.toString())
                .gpu(DUMMY_GPU)
                .build();
    }
}
