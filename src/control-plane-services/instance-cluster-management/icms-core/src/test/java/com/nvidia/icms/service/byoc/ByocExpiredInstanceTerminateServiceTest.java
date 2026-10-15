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

import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import static com.nvidia.icms.util.TestUtil.getTerminatedInstance;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.scheduled.instance.ExpiredInstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TestUtil;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.List;
import java.util.Optional;

@ExtendWith(MockitoExtension.class)
class ByocExpiredInstanceTerminateServiceTest {

    private static final String ZONE = "ZONE1";
    private static final String INSTANCE_LIFETIME_EXPIRED_ERROR_LOG = "Instance lifetime expired";

    @Mock private ByocTerminateService byocTerminateService;
    @Mock private InstanceV2Repository instanceV2Repository;
    @Mock private TelemetryEventClient telemetryEventClient;
    @Mock private InstanceServiceHelper instanceServiceHelper;
    @Mock private ExpiredInstanceServiceHelper expiredInstanceServiceHelper;

    private ByocExpiredInstanceTerminateService service;

    @BeforeEach
    void setUp() {
        service = new ByocExpiredInstanceTerminateService(
                byocTerminateService,
                instanceV2Repository,
                telemetryEventClient,
                instanceServiceHelper,
                expiredInstanceServiceHelper);
    }

    @Test
    void persistAndTerminate_whenEmptyList_shouldReturnImmediately() {
        service.persistAndTerminate(List.of());

        verifyNoInteractions(byocTerminateService, instanceV2Repository,
                telemetryEventClient, expiredInstanceServiceHelper);
    }

    @Test
    void persistAndTerminate_whenClusterMissing_shouldTerminateInPlaceAndSkipSqs() {
        InstanceV2Entity entity = byocInstanceInZone(ZONE);
        when(byocTerminateService.getClusterEntityFromClusterId(ZONE)).thenReturn(Optional.empty());
        when(expiredInstanceServiceHelper.buildExpiredInstanceTerminationMetric(any(), any(), any(), any()))
                .thenReturn(new GenericMetric());

        service.persistAndTerminate(List.of(entity));

        verify(byocTerminateService).handleInstancesWithMissingClusterInfo(any());
        verify(expiredInstanceServiceHelper).sendAuditForExpiredInstance(any(), any());
        verify(instanceServiceHelper).gpuUsageEventForTerminatedInstance(any());
        verify(telemetryEventClient).triggerEvent(any());
        verify(instanceServiceHelper).sendLatestInstanceStateEvent(any());
        verify(byocTerminateService, never()).sendSqsMessageForInstanceTermination(any(), any());
        verify(instanceV2Repository, never()).update(any());
    }

    @Test
    void persistAndTerminate_whenMultipleInstancesSameZoneAndRequest_shouldFetchClusterOnce() {
        InstanceV2Entity entity1 = byocInstanceInZone(ZONE);
        entity1.setInstanceId("i1");
        InstanceV2Entity entity2 = byocInstanceInZone(ZONE);
        entity2.setInstanceId("i2");

        ClusterEntity cluster = clusterForZone(ZONE);
        when(byocTerminateService.getClusterEntityFromClusterId(ZONE)).thenReturn(Optional.of(cluster));

        InstanceV2Entity terminated1 = getTerminatedInstance(entity1, INSTANCE_LIFETIME_EXPIRED_ERROR_LOG);
        InstanceV2Entity terminated2 = getTerminatedInstance(entity2, INSTANCE_LIFETIME_EXPIRED_ERROR_LOG);
        when(byocTerminateService.updateInstanceEntityState(any(), any()))
                .thenReturn(terminated1, terminated2);

        when(expiredInstanceServiceHelper.buildExpiredInstanceTerminationMetric(any(), any(), any(), any()))
                .thenReturn(new GenericMetric());

        service.persistAndTerminate(List.of(entity1, entity2));

        // Cluster should be resolved from DB only once; second instance uses the cache
        verify(byocTerminateService, times(1)).getClusterEntityFromClusterId(ZONE);
        verify(instanceV2Repository, times(2)).update(any());
    }

    @Test
    void persistAndTerminate_whenSqsThrows_shouldSuppressExceptionAndSkipDbUpdate() {
        InstanceV2Entity entity = byocInstanceInZone(ZONE);

        ClusterEntity cluster = clusterForZone(ZONE);
        when(byocTerminateService.getClusterEntityFromClusterId(ZONE)).thenReturn(Optional.of(cluster));
        when(byocTerminateService.updateInstanceEntityState(any(), any()))
                .thenReturn(getTerminatedInstance(entity, INSTANCE_LIFETIME_EXPIRED_ERROR_LOG));
        doThrow(new RuntimeException("SQS unavailable"))
                .when(byocTerminateService).sendSqsMessageForInstanceTermination(any(), any());

        assertDoesNotThrow(() -> service.persistAndTerminate(List.of(entity)));

        verify(instanceV2Repository, never()).update(any());
        verify(expiredInstanceServiceHelper, never()).sendAuditForExpiredInstance(any(), any());
        verify(telemetryEventClient, never()).triggerEvent(any());
    }

    // -------------------------------------------------------------------------
    // helpers
    // -------------------------------------------------------------------------

    private static InstanceV2Entity byocInstanceInZone(String zone) {
        InstanceV2Entity entity = TestUtil.getInstanceEntityForRunningInstance();
        entity.setZone(zone);
        entity.setResourceProvider(ResourceProvider.BYOC);
        return entity;
    }

    private static ClusterEntity clusterForZone(String zone) {
        ClusterEntity cluster = getDummyClusterEntity();
        cluster.setClusterId(zone);
        return cluster;
    }
}
