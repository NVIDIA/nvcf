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
package com.nvidia.icms.service.scheduled.request;

import static com.nvidia.icms.util.TestUtil.getDummyClusterHealthEntity;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.TestUtil;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ClusterHealthMonitorTaskTest {

    @Mock
    QueueManager queueManager;

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    CloudHealthRepository cloudHealthRepository;

    @Mock
    ByocConfigurationProperties byocConfigurationProperties;

    @Mock
    AppAuditService auditService;

    @Mock
    TelemetryEventClient telemetryEventClient;

    @Mock
    InstanceServiceHelper instanceServiceHelper;

    @Mock
    ComputePlatformService computePlatformService;

    @InjectMocks
    ClusterHealthMonitorTask clusterHealthMonitorTask;

    @Test
    void monitorClusterHealth_oneClusterFromClusterGroupIsInActive_deleteQueuesForInactiveCluster() {

        // Prepare
        var clusterIds = Set.of("cluster-1", "cluster-2", "cluster-3");
        var cluster1 = TestUtil.getDummyClusterEntity();
        cluster1.setNvcaVersion(null);
        cluster1.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster1.setClusterId("cluster-1");
        cluster1.setTerminationQueueUrl("termination-queue-1");

        var cluster2 = TestUtil.getDummyClusterEntity();
        cluster2.setNvcaVersion(null);
        cluster2.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster2.setClusterId("cluster-2");
        cluster2.setTerminationQueueUrl("termination-queue-2");

        var cluster3 = TestUtil.getDummyClusterEntity();
        cluster3.setNvcaVersion(null);
        cluster3.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster3.setClusterId("cluster-3");
        cluster3.setTerminationQueueUrl("termination-queue-3");

        var clusterHealth1 = getDummyClusterHealthEntity("cluster-1");
        clusterHealth1.setHealthUpdatedTs(Instant.now().minus(10, ChronoUnit.MINUTES));

        var clusterHealth2 = getDummyClusterHealthEntity("cluster-2");
        clusterHealth1.setHealthUpdatedTs(Instant.now().minus(10, ChronoUnit.MINUTES));

        doReturn(List.of(cluster3, cluster2, cluster1)).when(clusterRepository).getAllClusters();

        doReturn(List.of(CloudHealthEntity.builder()
                                 .key(CloudHealthKey.builder()
                                              .cloudProvider(ResourceProvider.BYOC)
                                              .zone("cluster-id-1")
                                              .build())
                                 .status(CloudHealthStatus.HEALTHY)
                                 .build(),
                         CloudHealthEntity.builder()
                                 .key(CloudHealthKey.builder()
                                              .cloudProvider(ResourceProvider.BYOC)
                                              .zone("cluster-id-2")
                                              .build())
                                 .status(CloudHealthStatus.HEALTHY)
                                 .build())).when(cloudHealthRepository).findAll();

        doNothing().when(clusterRepository).saveClusterHealth(Mockito.any(), Mockito.anyInt());

        doReturn(30).when(byocConfigurationProperties).getClusterExpiryTimeInDays();

        doReturn(Optional.of(clusterHealth1)).when(clusterRepository)
                .getClusterHealthById("cluster-1");
        doReturn(Optional.of(clusterHealth2)).when(clusterRepository)
                .getClusterHealthById("cluster-2");
        doReturn(Optional.empty()).when(clusterRepository).getClusterHealthById("cluster-3");

        doNothing().when(queueManager).deleteQueue("termination-queue-3");

        doNothing().when(clusterRepository).updateClusterInfo(Mockito.argThat(
                                                                      entity -> entity.getClusterId().equals("cluster-3") &&
                                                                              entity.getClusterStatus().equals(
                                                                                      ClusterStatusEnum.ABANDONED)), Mockito.anySet(),
                                                              Mockito.eq(false));
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(Set.of()).when(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();

        // Act
        clusterHealthMonitorTask.monitorClusterHealth();

        // Verify
        verify(byocConfigurationProperties, times(2)).getClusterExpiryTimeInDays();
        verify(clusterRepository, times(3)).getClusterHealthById(
                Mockito.argThat(clusterId -> Set.of("cluster-1", "cluster-2", "cluster-3")
                        .contains(clusterId)));
        verify(clusterRepository).getAllClusters();
        verify(queueManager).deleteQueue("termination-queue-3");
        verifyNoMoreInteractions(queueManager);
        verify(clusterRepository).updateClusterInfo(Mockito.argThat(
                                                            entity -> entity.getClusterId().equals("cluster-3") &&
                                                                    entity.getClusterStatus().equals(
                                                                            ClusterStatusEnum.ABANDONED)), Mockito.anySet(),
                                                    Mockito.eq(false));
        verify(clusterRepository, times(2)).saveClusterHealth(Mockito.any(), Mockito.anyInt());
        verify(cloudHealthRepository).findAll();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(Mockito.any());
        verify(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();
    }

    @Test
    void monitorClusterHealth_twoClusterFromClusterGroupIsInActive_deleteQueuesForInactiveCluster() {

        // Prepare
        var cluster1 = TestUtil.getDummyClusterEntity();
        cluster1.setNvcaVersion(null);
        cluster1.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster1.setClusterId("cluster-1");
        cluster1.setTerminationQueueUrl("termination-queue-1");

        var cluster2 = TestUtil.getDummyClusterEntity();
        cluster2.setNvcaVersion(null);
        cluster2.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster2.setClusterId("cluster-2");
        cluster2.setTerminationQueueUrl("termination-queue-2");

        var cluster3 = TestUtil.getDummyClusterEntity();
        cluster3.setNvcaVersion(null);
        cluster3.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster3.setClusterId("cluster-3");
        cluster3.setTerminationQueueUrl("termination-queue-3");

        var clusterHealth1 = getDummyClusterHealthEntity("cluster-1");
        clusterHealth1.setHealthUpdatedTs(Instant.now().minus(10, ChronoUnit.MINUTES));

        doReturn(30).when(byocConfigurationProperties).getClusterExpiryTimeInDays();

        doReturn(List.of(cluster3, cluster2, cluster1)).when(clusterRepository).getAllClusters();

        doReturn(List.of(CloudHealthEntity.builder()
                                 .key(CloudHealthKey.builder()
                                              .cloudProvider(ResourceProvider.BYOC)
                                              .zone("cluster-id-1")
                                              .build())
                                 .status(CloudHealthStatus.HEALTHY)
                                 .build())).when(cloudHealthRepository).findAll();

        doNothing().when(clusterRepository).saveClusterHealth(Mockito.any(), Mockito.anyInt());

        doReturn(Optional.of(clusterHealth1)).when(clusterRepository)
                .getClusterHealthById("cluster-1");
        doReturn(Optional.empty()).when(clusterRepository).getClusterHealthById("cluster-2");
        doReturn(Optional.empty()).when(clusterRepository).getClusterHealthById("cluster-3");

        doNothing().when(queueManager).deleteQueue(Mockito.argThat(
                url -> Set.of("termination-queue-3", "termination-queue-2").contains(url)));

        doNothing().when(clusterRepository).updateClusterInfo(Mockito.argThat(
                entity -> Set.of("cluster-2", "cluster-3").contains(entity.getClusterId()) &&
                        entity.getClusterStatus().equals(
                                ClusterStatusEnum.ABANDONED)), Mockito.anySet(), Mockito.eq(false));
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(Set.of()).when(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();

        // Act
        clusterHealthMonitorTask.monitorClusterHealth();

        // Verify
        verify(byocConfigurationProperties, times(2)).getClusterExpiryTimeInDays();
        verify(clusterRepository, times(3)).getClusterHealthById(
                Mockito.argThat(clusterId -> Set.of("cluster-1", "cluster-2", "cluster-3")
                        .contains(clusterId)));
        verify(clusterRepository).getAllClusters();
        verify(queueManager, times(2)).deleteQueue(Mockito.argThat(
                url -> Set.of("termination-queue-3", "termination-queue-2").contains(url)));
        verifyNoMoreInteractions(queueManager);
        verify(clusterRepository, times(2)).updateClusterInfo(Mockito.argThat(
                entity -> Set.of("cluster-2", "cluster-3").contains(entity.getClusterId()) &&
                        entity.getClusterStatus().equals(
                                ClusterStatusEnum.ABANDONED)), Mockito.anySet(), Mockito.eq(false));
        verify(clusterRepository).saveClusterHealth(Mockito.any(), Mockito.anyInt());
        verify(cloudHealthRepository).findAll();
        verify(auditService, times(2)).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                                      Mockito.any());
        verify(telemetryEventClient, times(2)).triggerEvent(Mockito.any());
        verify(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();
    }

    @Test
    void monitorClusterHealth_allClusterFromClusterGroupIsActive_doNothing() {

        // Prepare
        var cluster1 = TestUtil.getDummyClusterEntity();
        cluster1.setNvcaVersion(null);
        cluster1.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster1.setClusterId("cluster-1");
        cluster1.setTerminationQueueUrl("termination-queue-1");

        var cluster2 = TestUtil.getDummyClusterEntity();
        cluster2.setNvcaVersion(null);
        cluster2.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster2.setClusterId("cluster-2");
        cluster2.setTerminationQueueUrl("termination-queue-2");

        var clusterHealth1 = getDummyClusterHealthEntity("cluster-1");
        clusterHealth1.setHealthUpdatedTs(Instant.now().minus(10, ChronoUnit.MINUTES));

        var clusterHealth2 = getDummyClusterHealthEntity("cluster-2");
        clusterHealth1.setHealthUpdatedTs(Instant.now().minus(10, ChronoUnit.MINUTES));

        doReturn(30).when(byocConfigurationProperties).getClusterExpiryTimeInDays();

        doReturn(List.of(cluster2, cluster1)).when(clusterRepository).getAllClusters();

        doReturn(List.of(CloudHealthEntity.builder()
                                 .key(CloudHealthKey.builder()
                                              .cloudProvider(ResourceProvider.BYOC)
                                              .zone("cluster-id-1")
                                              .build())
                                 .status(CloudHealthStatus.HEALTHY)
                                 .build(),
                         CloudHealthEntity.builder()
                                 .key(CloudHealthKey.builder()
                                              .cloudProvider(ResourceProvider.BYOC)
                                              .zone("cluster-id-2")
                                              .build())
                                 .status(CloudHealthStatus.HEALTHY)
                                 .build())).when(cloudHealthRepository).findAll();

        doNothing().when(clusterRepository).saveClusterHealth(Mockito.any(), Mockito.anyInt());

        doReturn(Optional.of(clusterHealth1)).when(clusterRepository)
                .getClusterHealthById("cluster-1");
        doReturn(Optional.of(clusterHealth2)).when(clusterRepository)
                .getClusterHealthById("cluster-2");
        doReturn(Set.of()).when(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();

        // Act
        clusterHealthMonitorTask.monitorClusterHealth();

        // Verify
        verify(byocConfigurationProperties, times(2)).getClusterExpiryTimeInDays();
        verify(clusterRepository, times(2)).getClusterHealthById(
                Mockito.argThat(clusterId -> Set.of("cluster-1", "cluster-2")
                        .contains(clusterId)));
        verify(clusterRepository).getAllClusters();
        verifyNoInteractions(queueManager);
        verify(clusterRepository, times(2)).saveClusterHealth(Mockito.any(), Mockito.anyInt());
        verify(cloudHealthRepository).findAll();
        verifyNoMoreInteractions(clusterRepository);
        verifyNoInteractions(auditService);
        verifyNoInteractions(telemetryEventClient);
        verify(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();
    }


    @Test
    void monitorClusterHealth_allClusterFromClusterGroupInActive_deleteQueuesForInactiveClusterAndDeleteCreationQueueOfClusterGroup() {

        // Prepare
        var cluster1 = TestUtil.getDummyClusterEntity();
        cluster1.setNvcaVersion(null);
        cluster1.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster1.setClusterId("cluster-1");
        cluster1.setTerminationQueueUrl("termination-queue-1");
        cluster1.setCreationQueueUrl("creation-queue");

        var cluster2 = TestUtil.getDummyClusterEntity();
        cluster2.setNvcaVersion(null);
        cluster2.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster2.setClusterId("cluster-2");
        cluster2.setTerminationQueueUrl("termination-queue-2");
        cluster2.setCreationQueueUrl("creation-queue");

        doReturn(30).when(byocConfigurationProperties).getClusterExpiryTimeInDays();

        doReturn(List.of(cluster2, cluster1)).when(clusterRepository).getAllClusters();

        doReturn(List.of()).when(cloudHealthRepository).findAll();

        doReturn(Optional.empty()).when(clusterRepository)
                .getClusterHealthById(Mockito.argThat(clientId -> Set.of("cluster-1", "cluster-2")
                        .contains(clientId)));

        doNothing().when(queueManager).deleteQueue(Mockito.argThat(
                url -> Set.of("termination-queue-1", "termination-queue-2").contains(url)));

        doNothing().when(clusterRepository).updateClusterInfo(Mockito.argThat(
                entity -> Set.of("cluster-1", "cluster-2").contains(entity.getClusterId()) &&
                        entity.getClusterStatus().equals(
                                ClusterStatusEnum.ABANDONED)), Mockito.anySet(), Mockito.eq(false));
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(Set.of()).when(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();

        // Act
        clusterHealthMonitorTask.monitorClusterHealth();

        // Verify
        verify(byocConfigurationProperties, times(2)).getClusterExpiryTimeInDays();
        verify(clusterRepository, times(2)).getClusterHealthById(
                Mockito.argThat(clusterId -> Set.of("cluster-1", "cluster-2")
                        .contains(clusterId)));
        verify(clusterRepository).getAllClusters();
        verifyNoMoreInteractions(clusterRepository);
        verify(queueManager, times(2)).deleteQueue(Mockito.argThat(
                url -> Set.of("termination-queue-1", "termination-queue-2").contains(url)));
        verify(queueManager).deleteQueue("creation-queue");
        verifyNoMoreInteractions(queueManager);
        verify(clusterRepository, times(2)).updateClusterInfo(Mockito.argThat(
                                                                      entity -> Set.of("cluster-1", "cluster-2").contains(entity.getClusterId())),
                                                              Mockito.anySet(), Mockito.eq(false));
        verify(cloudHealthRepository).findAll();
        verify(auditService, times(2)).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                                      Mockito.any());
        verify(telemetryEventClient, times(2)).triggerEvent(Mockito.any());
        verify(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();
    }

    @Test
    void monitorClusterHealth_withInactiveNvca2Cluster_deleteQueuesForInactiveClusterAndDeleteCreationQueuesOfCluster() {

        // Prepare
        var cluster1 = TestUtil.getDummyClusterEntity();
        CreationQueueUdt creationQueue = CreationQueueUdt.builder()
                .queueType("FIFO")
                .url("creation-queue-url")
                .build();
        CreationQueueUdt creationQueue1 = CreationQueueUdt.builder()
                .queueType("FIFO")
                .url("cluster-creation-queue-url")
                .build();
        cluster1.setCreationQueues(Map.of("gpu1", creationQueue));
        cluster1.setClusterCreationQueues(Map.of("gpu1", creationQueue1));
        cluster1.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster1.setClusterId("cluster-1");
        cluster1.setTerminationQueueUrl("termination-queue-1");
        cluster1.setCreationQueueUrl("creation-queue");

        doReturn(30).when(byocConfigurationProperties).getClusterExpiryTimeInDays();

        doReturn(List.of(cluster1)).when(clusterRepository).getAllClusters();

        doReturn(List.of()).when(cloudHealthRepository).findAll();

        doReturn(Optional.empty()).when(clusterRepository)
                .getClusterHealthById(Mockito.argThat(clientId -> Objects.equals("cluster-1",
                                                                                 clientId)));

        doNothing().when(queueManager).deleteQueue(Mockito.argThat(
                url -> Objects.equals("termination-queue-1", url)));

        doNothing().when(clusterRepository).updateClusterInfo(Mockito.argThat(
                entity -> Objects.equals("cluster-1", entity.getClusterId()) &&
                        entity.getClusterStatus().equals(
                                ClusterStatusEnum.ABANDONED)), Mockito.anySet(), Mockito.eq(false));
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(Set.of()).when(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();

        // Act
        clusterHealthMonitorTask.monitorClusterHealth();

        // Verify
        verify(byocConfigurationProperties, times(2)).getClusterExpiryTimeInDays();
        verify(clusterRepository, times(1)).getClusterHealthById(
                Mockito.argThat(clusterId -> Objects.equals("cluster-1", clusterId)));
        verify(clusterRepository).getAllClusters();
        verifyNoMoreInteractions(clusterRepository);
        verify(queueManager, times(3)).deleteQueue(Mockito.argThat(
                url -> Set.of("termination-queue-1", "creation-queue-url",
                              "cluster-creation-queue-url").contains(url)));
        verifyNoMoreInteractions(queueManager);
        verify(clusterRepository, times(1)).updateClusterInfo(Mockito.argThat(
                                                                      entity -> Objects.equals(
                                                                              "cluster-1",
                                                                              entity.getClusterId())),
                                                              Mockito.anySet(), Mockito.eq(false));
        verify(cloudHealthRepository).findAll();
        verify(auditService, times(1)).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                                      Mockito.any());
        verify(telemetryEventClient, times(1)).triggerEvent(Mockito.any());
        verify(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();
    }

    @Test
    void monitorClusterHealth_oneClusterCreatedBefore30DaysAndOtherClusterIsInactive_deleteQueuesForInactiveCluster() {

        // Prepare
        var cluster1 = TestUtil.getDummyClusterEntity();
        cluster1.setNvcaVersion(null);
        cluster1.setRegistrationTime(Instant.now().minus(14, ChronoUnit.DAYS));
        cluster1.setClusterId("cluster-1");
        cluster1.setTerminationQueueUrl("termination-queue-1");

        var cluster2 = TestUtil.getDummyClusterEntity();
        cluster2.setNvcaVersion(null);
        cluster2.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster2.setClusterId("cluster-2");
        cluster2.setTerminationQueueUrl("termination-queue-2");

        doReturn(30).when(byocConfigurationProperties).getClusterExpiryTimeInDays();

        doReturn(List.of(cluster2, cluster1)).when(clusterRepository).getAllClusters();

        doReturn(List.of()).when(cloudHealthRepository).findAll();
        doReturn(Optional.empty()).when(clusterRepository)
                .getClusterHealthById("cluster-2");

        doNothing().when(queueManager).deleteQueue("termination-queue-2");

        doNothing().when(clusterRepository).updateClusterInfo(Mockito.argThat(
                                                                      entity -> entity.getClusterId().equals("cluster-2") &&
                                                                              entity.getClusterStatus().equals(
                                                                                      ClusterStatusEnum.ABANDONED)), Mockito.anySet(),
                                                              Mockito.eq(false));
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(Set.of()).when(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();

        // Act
        clusterHealthMonitorTask.monitorClusterHealth();

        // Verify
        verify(byocConfigurationProperties, times(2)).getClusterExpiryTimeInDays();
        verify(clusterRepository).getClusterHealthById("cluster-2");
        verify(clusterRepository).getAllClusters();
        verify(queueManager).deleteQueue("termination-queue-2");
        verifyNoMoreInteractions(queueManager);
        verify(clusterRepository).updateClusterInfo(Mockito.argThat(
                                                            entity -> entity.getClusterId().equals("cluster-2") &&
                                                                    entity.getClusterStatus().equals(
                                                                            ClusterStatusEnum.ABANDONED)), Mockito.anySet(),
                                                    Mockito.eq(false));
        verifyNoMoreInteractions(clusterRepository);
        verify(cloudHealthRepository).findAll();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(Mockito.any());
        verify(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();
    }

    @Test
    void monitorClusterHealth_allClusterFromClusterGroupInActiveButCloudHealthCheckIsSkipped_returnsSuccess() {

        // Prepare
        var ncaId = "nca-id-1";
        var clusterName = "cluster-name-1";
        Map<String, String> ncaIdToClusterNameMap1 = Map.of(ncaId, clusterName);
        List<Map<String, String>> mapList = List.of(ncaIdToClusterNameMap1);

        var cluster1 = TestUtil.getDummyClusterEntity();
        cluster1.setNvcaVersion(null);
        cluster1.setRegistrationTime(Instant.now().minus(32, ChronoUnit.DAYS));
        cluster1.setClusterId("cluster-1");
        cluster1.setNcaId(ncaId);
        cluster1.setClusterName(clusterName);
        cluster1.setTerminationQueueUrl("termination-queue-1");
        cluster1.setCreationQueueUrl("creation-queue");

        var cluster2 = TestUtil.getDummyClusterEntity();
        cluster2.setNvcaVersion(null);
        cluster2.setRegistrationTime(Instant.now().minus(10, ChronoUnit.DAYS));
        cluster2.setClusterId("cluster-2");
        cluster2.setTerminationQueueUrl("termination-queue-2");
        cluster2.setCreationQueueUrl("creation-queue");

        doReturn(30).when(byocConfigurationProperties).getClusterExpiryTimeInDays();

        doReturn(List.of(cluster2, cluster1)).when(clusterRepository).getAllClusters();

        doReturn(List.of()).when(cloudHealthRepository).findAll();

        doReturn(Set.of("cluster-1")).when(instanceServiceHelper)
                .getClusterIdsOfClusterToSkipHealthCheck();

        // Act
        clusterHealthMonitorTask.monitorClusterHealth();

        // Verify
        verify(byocConfigurationProperties, times(2)).getClusterExpiryTimeInDays();
        verify(clusterRepository).getAllClusters();
        verify(cloudHealthRepository).findAll();
        verify(instanceServiceHelper).getClusterIdsOfClusterToSkipHealthCheck();
        verifyNoMoreInteractions(clusterRepository);
        verifyNoInteractions(auditService);
        verifyNoMoreInteractions(telemetryEventClient);
        verifyNoMoreInteractions(queueManager);
    }
}
