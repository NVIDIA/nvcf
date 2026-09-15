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
package com.nvidia.icms.scheduled;

import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.DbConfigurationProperties;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.ReservedBackupInstanceProcessor;
import com.nvidia.icms.service.scheduled.instance.ActiveInstanceMonitoringTaskService;
import com.nvidia.icms.service.scheduled.instance.ProcessUnhealthyInstance;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.util.Optional;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;

@ExtendWith(MockitoExtension.class)
class ActiveInstanceMonitoringTaskServiceControllerTest extends IntegrationTest {

    @Autowired
    InstanceV2Repository instanceV2Repository;

    @Autowired
    CloudHealthRepository cloudHealthRepository;


    ActiveInstanceMonitoringTaskService activeInstanceMonitoringTaskService;

    @Autowired
    IcmsConfigurationProperties icmsConfigurationProperties;

    @Autowired
    CloudHealthService cloudHealthService;

    @Autowired
    DbConfigurationProperties dbConfigurationProperties;

    @Autowired
    InstanceServiceHelper instanceServiceHelper;

    @Autowired
    ReservedBackupInstanceProcessor reservedBackupInstanceProcessor;

    @Autowired
    TelemetryEventClient telemetryEventClient;

    @Autowired
    ProcessUnhealthyInstance processUnhealthyInstance;

    @BeforeEach
    void setup() {
        activeInstanceMonitoringTaskService = new ActiveInstanceMonitoringTaskService(icmsConfigurationProperties,
                                                                                      reservedBackupInstanceProcessor,
                                                                                      instanceV2Repository,
                                                                                      processUnhealthyInstance,
                                                                                      instanceServiceHelper,
                                                                                      telemetryEventClient,
                                                                                      cloudHealthRepository);
    }

    @Test
    void updateInstanceHealthState_forCloudProvider_returnsSuccess() {

        // Prepare
        insertByocDataInDb();

        // Act
        activeInstanceMonitoringTaskService.execute();

        // Assert
        Optional<InstanceV2Entity> optionalByocEntity1 =
                instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_ID, "gdn-i1");
        Optional<InstanceV2Entity> optionalByocEntity2 =
                instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_ID, "gdn-i2");

        assertTrue(optionalByocEntity1.isPresent());
        assertTrue(optionalByocEntity2.isPresent());
        assertEquals(SpotInstanceInternalState.TERMINATED,
                     optionalByocEntity1.get().getInstanceStateName());
        assertEquals(SpotInstanceInternalState.TERMINATED,
                     optionalByocEntity2.get().getInstanceStateName());

    }


    private void insertByocDataInDb() {
        // Adding BYOC instances
        instanceV2Repository.insert(
                getInstanceEntity("gdn-r0", "gdn-i0", TimeUtils.getFirstDateOfPreviousMonth(0),
                                      "gdn_zone_0",
                                      SpotInstanceInternalState.STARTING,
                                      SpotInstanceRequestState.ACTIVE));

        instanceV2Repository.insert(
                getInstanceEntity("gdn-r1", "gdn-i1", TimeUtils.getFirstDateOfPreviousMonth(1),
                                      "gdn_zone_1",
                                      SpotInstanceInternalState.RUNNING,
                                      SpotInstanceRequestState.ACTIVE));

        instanceV2Repository.insert(
                getInstanceEntity("gdn-r2", "gdn-i2", TimeUtils.getFirstDateOfPreviousMonth(2),
                                      "gdn_zone_2",
                                      SpotInstanceInternalState.SHUTTING_DOWN,
                                      SpotInstanceRequestState.ACTIVE));

        instanceV2Repository.insert(
                getInstanceEntity("gdn-r3", "gdn-i3", TimeUtils.getFirstDateOfPreviousMonth(3),
                                      "gdn_zone_3",
                                      SpotInstanceInternalState.TERMINATED,
                                      SpotInstanceRequestState.CLOSED));

        // Adding BYOC health status
        cloudHealthRepository.insert(
                getCloudHealthEntity(CloudHealthStatus.HEALTHY,
                                     "gdn_zone_0"),
                120);

        cloudHealthRepository.insert(
                getCloudHealthEntity(CloudHealthStatus.UNHEALTHY,
                                     "gdn_zone_1"),
                120);

        cloudHealthRepository.insert(
                getCloudHealthEntity(CloudHealthStatus.UNHEALTHY,
                                     "gdn_zone_2"),
                120);
    }

    private CloudHealthEntity getCloudHealthEntity(
            CloudHealthStatus cloudHealthStatus,
            String zone) {
        return CloudHealthEntity.builder()
                .key(CloudHealthKey.builder()
                             .cloudProvider(ResourceProvider.BYOC)
                             .zone(zone)
                             .build())
                .status(cloudHealthStatus)
                .build();
    }

    private InstanceV2Entity getInstanceEntity(
            String requestId, String instanceId,
            Instant instant, String zoneName,
            SpotInstanceInternalState instanceInternalState,
            SpotInstanceRequestState requestState) {

        return InstanceV2Entity.builder()
                .instanceId(instanceId)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(instant))
                .customer(DUMMY_CUSTOMER_ID)
                .instanceUpdateTime(instant)
                .requestId(requestId)
                .instanceId(instanceId)
                .zone(zoneName)
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .gpu(DUMMY_GPU)
                .backend(ResourceProvider.BYOC.toString())
                .ncaId(DUMMY_NON_BYOC_NCA_ID)
                .instanceStateName(instanceInternalState)
                .requestState(requestState)
                .resourceProvider(ResourceProvider.BYOC)
                .instanceStateCode(
                        SpotInstanceInternalState.getStateCode(instanceInternalState))
                .build();
    }
}