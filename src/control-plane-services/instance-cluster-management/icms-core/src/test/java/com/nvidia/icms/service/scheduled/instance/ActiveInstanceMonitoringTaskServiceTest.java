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
package com.nvidia.icms.service.scheduled.instance;


import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.anySet;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.DbConfigurationProperties;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.factory.RandomFactory;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import java.util.Map;
import java.util.Set;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.ReservedBackupInstanceProcessor;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.TestUtil;
import com.nvidia.icms.util.TimeUtils;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.mockito.junit.jupiter.MockitoSettings;
import org.mockito.quality.Strictness;
import org.springframework.beans.factory.annotation.Autowired;

@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.LENIENT)
class ActiveInstanceMonitoringTaskServiceTest extends IntegrationTest {

    @Autowired
    private InstanceV2Repository instanceV2Repository;

    @Autowired
    private CloudHealthRepository cloudHealthRepository;

    @Mock
    private DbConfigurationProperties dbConfigurationProperties;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private ReservedBackupInstanceProcessor reservedBackupInstanceProcessor;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    @Mock
    private ProcessUnhealthyInstance processUnhealthyInstance;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    private ActiveInstanceMonitoringTaskService activeInstanceMonitoringTaskService;

    @BeforeEach
    void init() {
        activeInstanceMonitoringTaskService =
                new ActiveInstanceMonitoringTaskService(icmsConfigurationProperties,
                                                        reservedBackupInstanceProcessor,
                                                        instanceV2Repository,
                                                        processUnhealthyInstance,
                                                        instanceServiceHelper,
                                                        telemetryEventClient,
                                                        cloudHealthRepository);
    }

    @Test
    void execute_featureDisabled_skip() {
        // Mock
        doReturn(false).when(icmsConfigurationProperties).isCloudFailureDetectionEnabled();
        doReturn(false).when(reservedBackupInstanceProcessor).isBackupToPrimaryZoneMigrationEnabled();


        //Act
        activeInstanceMonitoringTaskService.execute();

        // Assert
        // cloudHealthService is not used by execute(); nothing to verify
        verifyNoInteractions(dbConfigurationProperties);
        verifyNoInteractions(processUnhealthyInstance);
        verify(reservedBackupInstanceProcessor, org.mockito.Mockito.never()).execute(anyList());
    }

    @Test
    void execute_unhealthyInstances_callsUnhealthyInstanceService() {
        // Feature toggles
        doReturn(true).when(icmsConfigurationProperties).isCloudFailureDetectionEnabled();
        doReturn(false).when(reservedBackupInstanceProcessor).isBackupToPrimaryZoneMigrationEnabled();

        // skip list
        doReturn(Set.of("skip-1")).when(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();

        // Prepare one active instance
        InstanceV2Entity active = TestUtil.getInstanceEntityForRunningInstance();
        active.setInstanceStateName(SpotInstanceInternalState.RUNNING);
        active.setInstanceId(RandomFactory.getRandomStringWithPrefix("instanceId", 5));
        active.setCustomer(DUMMY_CUSTOMER_ID);
        active.setCreateTimeuuid(TimeUtils.getTimeUuidNow());
        instanceV2Repository.insert(active);

        // Mark instance unhealthy
        cloudHealthRepository.insert(getDummyCloudHealthEntity(active.getZone(), CloudHealthStatus.UNHEALTHY), 1);

        activeInstanceMonitoringTaskService.execute();

        verify(processUnhealthyInstance).execute(any(), anySet());
        verify(reservedBackupInstanceProcessor, org.mockito.Mockito.never()).execute(anyList());
    }

    @Test
    void execute_healthyReservedBackup_callsReservedBackupMonitoring() {
        // Feature toggles
        doReturn(false).when(icmsConfigurationProperties).isCloudFailureDetectionEnabled();
        doReturn(true).when(reservedBackupInstanceProcessor).isBackupToPrimaryZoneMigrationEnabled();

        // Prepare one active RESERVED_BACKUP instance that is healthy
        InstanceV2Entity backup = TestUtil.getInstanceEntityForRunningInstance();
        backup.setInstanceStateName(SpotInstanceInternalState.RUNNING);
        backup.setCapacityType(CapacityType.RESERVED_BACKUP.toString());
        backup.setInstanceId(RandomFactory.getRandomStringWithPrefix("instanceId", 5));
        backup.setCustomer(DUMMY_CUSTOMER_ID);
        backup.setCreateTimeuuid(TimeUtils.getTimeUuidNow());
        instanceV2Repository.insert(backup);

        // Mark instance healthy
        cloudHealthRepository.insert(getDummyCloudHealthEntity(backup.getZone(), CloudHealthStatus.HEALTHY), 1);

        activeInstanceMonitoringTaskService.execute();

        verify(reservedBackupInstanceProcessor).execute(anyList());
        verifyNoMoreInteractions(processUnhealthyInstance);
    }

    @Test
    void execute_healthMonitoringException_suppressed_and_other_tasks_continue() {
        // Enable both features so both paths are available
        doReturn(true).when(icmsConfigurationProperties).isCloudFailureDetectionEnabled();
        doReturn(true).when(reservedBackupInstanceProcessor).isBackupToPrimaryZoneMigrationEnabled();

        // Prepare one unhealthy active and one healthy RESERVED_BACKUP
        InstanceV2Entity unhealthy = TestUtil.getInstanceEntityForRunningInstance();
        unhealthy.setInstanceStateName(SpotInstanceInternalState.RUNNING);
        unhealthy.setCapacityType(CapacityType.SPOT.toString());
        unhealthy.setInstanceId(RandomFactory.getRandomStringWithPrefix("instanceId", 5));
        unhealthy.setZone(RandomFactory.getRandomStringWithPrefix("zone", 5));
        instanceV2Repository.insert(unhealthy);

        InstanceV2Entity backup = TestUtil.getInstanceEntityForRunningInstance();
        backup.setInstanceStateName(SpotInstanceInternalState.RUNNING);
        backup.setCapacityType(CapacityType.RESERVED_BACKUP.toString());
        backup.setInstanceId(RandomFactory.getRandomStringWithPrefix("instanceId", 5));
        backup.setZone(RandomFactory.getRandomStringWithPrefix("zone", 5));
        instanceV2Repository.insert(backup);

        // Mark instance healthy
        cloudHealthRepository.insert(getDummyCloudHealthEntity(backup.getZone(), CloudHealthStatus.HEALTHY), 2);
        // Mark instance unhealthy
        cloudHealthRepository.insert(getDummyCloudHealthEntity(unhealthy.getZone(), CloudHealthStatus.UNHEALTHY), 2);

        // Throw from health processing; should be suppressed
        doReturn(1).when(icmsConfigurationProperties).getCloudFailureDetectionTaskPauseBetweenDaysInMilliseconds();
        doThrow(new RuntimeException("run time error"))
                .when(processUnhealthyInstance)
                .execute(anyList(), any());

        // Execute should not throw
        activeInstanceMonitoringTaskService.execute();

        // Verify
        // backup path still executed
        verify(reservedBackupInstanceProcessor).execute(anyList());
    }

    @Test
    void execute_topLevelException_sendsFailureTelemetry_andRethrows() {
        // Make helper throw at top level
        doReturn(true).when(icmsConfigurationProperties).isCloudFailureDetectionEnabled();
        when(instanceServiceHelper.getClusterIdsOfClusterToSkipHealthCheck())
                .thenThrow(new RuntimeException("top-level"));

        Assertions.assertThrows(RuntimeException.class,
                                () -> activeInstanceMonitoringTaskService.execute());

        verify(telemetryEventClient).triggerEvent(anyList());
    }

    private CloudHealthEntity getDummyCloudHealthEntity(String zoneName, CloudHealthStatus cloudHealthStatus) {
        return TestUtil.getDummyCloudHealthEntity(zoneName, DUMMY_GPU, cloudHealthStatus, ResourceProvider.BYOC, 10, 0,
                                                  10);
    }
}
