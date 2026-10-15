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
package com.nvidia.icms.service.byoc.nvca.clustermanagement;

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_TERMINATION_QUEUE_URL;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_CREATION_QUEUE_URL;
import static com.nvidia.icms.util.TestUtil.DUMMY_CREATION_QUEUE_URL;
import static com.nvidia.icms.util.TestUtil.DUMMY_RUNNING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_STARTING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterConfigurationRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ClusterTerminateServiceTest {

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    NvcaClusterRepository nvcaClusterRepository;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    @Mock
    private QueueManager queueManager;

    @Mock
    private AppAuditService auditService;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private NvcaClusterRegistrationService nvcaClusterRegistrationService;

    @Mock
    NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    private ClusterTerminateService clusterTerminateService;

    @BeforeEach
    void init() {

        clusterTerminateService =
                new ClusterTerminateService(nvcaClusterRepository, clusterRepository, queueManager,
                                            instanceServiceHelper, auditService, telemetryEventClient,
                                            nvcaClusterRegistrationService, nvcaClusterConfigurationRepository);
    }

    @Test
    void deleteCluster_singleClusterInGroup_success() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        CreationQueueUdt creationQueue = CreationQueueUdt.builder()
                .url(DUMMY_CREATION_QUEUE_URL)
                .queueType("queue_type")
                .build();
        clusterEntity.setCreationQueues(Map.of("AZURE", creationQueue));
        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of());
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(queueManager).deleteQueue(DUMMY_BYOC_TERMINATION_QUEUE_URL);
        doNothing().when(queueManager).deleteQueue(DUMMY_CREATION_QUEUE_URL);
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                              clusterEntity.getClusterId(),
                                              new HashMap<>());

        // Assert
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(nvcaClusterRepository).deleteClusterInfo(clusterEntity);
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(instanceServiceHelper).getActiveInstancesFromZone(clusterEntity.getClusterId());
        verify(queueManager).deleteQueue(DUMMY_BYOC_TERMINATION_QUEUE_URL);
        verify(queueManager).deleteQueue(DUMMY_CREATION_QUEUE_URL);

    }

    @Test
    void deleteCluster_deletesConfigurationEntry() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of());
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                              clusterEntity.getClusterId(),
                                              new HashMap<>());

        // Assert
        verify(nvcaClusterConfigurationRepository).deleteByClusterId(clusterEntity.getClusterId());
    }
    @Test
    void deleteCluster_singleClusterInGroup_withNatsEnabled_success() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        CreationQueueUdt creationQueue = CreationQueueUdt.builder()
                .url(DUMMY_CREATION_QUEUE_URL)
                .queueType("queue_type")
                .build();
        clusterEntity.setCreationQueues(Map.of("AZURE", creationQueue));
        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of());
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        doReturn(true).when(instanceServiceHelper).isNatsEnabled();

        // Act
        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                              clusterEntity.getClusterId(),
                                              new HashMap<>());

        // Assert
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(nvcaClusterRepository).deleteClusterInfo(clusterEntity);
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(instanceServiceHelper).getActiveInstancesFromZone(clusterEntity.getClusterId());
        verifyNoInteractions(queueManager);

    }

    @Test
    void deleteCluster_withActiveInstances_throwsException() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of(DUMMY_STARTING_INSTANCE_ID, DUMMY_RUNNING_INSTANCE_ID));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () ->
                        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                                              clusterEntity.getClusterId(),
                                                              new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "Cluster un-registration failed, terminate following active instances "
                        + "[dummy_starting_instance_Id, dummy_running_instance_id]",
                exception.getBody().getDetail());
        verifyNoInteractions(auditService);
        verifyNoInteractions(nvcaClusterRepository);
        verifyNoInteractions(nvcaClusterConfigurationRepository);
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verify(instanceServiceHelper).getActiveInstancesFromZone(clusterEntity.getClusterId());
    }

    @Test
    void deleteCluster_withNvca1Cluster_throwsException() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setNvcaVersion(null);

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class, () ->
                        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                                              clusterEntity.getClusterId(),
                                                              new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "This cluster id can not be deleted since it was registered with NVCA 1.0 flow",
                exception.getBody().getDetail());
        verifyNoInteractions(auditService);
        verifyNoInteractions(nvcaClusterRepository);
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verifyNoInteractions(instanceServiceHelper);
        verifyNoInteractions(nvcaClusterConfigurationRepository);
    }

    @Test
    void deleteCluster_withClusterNotFound_throwsNotFoundException() {
        // Prepare
        String clusterId = "non-existent-cluster";
        String ncaId = "ncaId";

        when(clusterRepository.getClusterInfoByClusterId(clusterId, false))
                .thenReturn(Optional.empty());

        // Act
        IcmsNotFoundException exception = assertThrows(
                IcmsNotFoundException.class, () ->
                        clusterTerminateService.deleteCluster(ncaId, clusterId, new HashMap<>()));

        // Assert
        assertEquals("Could not find any cluster registered with id non-existent-cluster",
                     exception.getBody().getDetail());
        verify(clusterRepository).getClusterInfoByClusterId(clusterId, false);
        verifyNoInteractions(auditService);
        verifyNoInteractions(nvcaClusterRepository);
        verifyNoInteractions(instanceServiceHelper);
        verifyNoInteractions(nvcaClusterConfigurationRepository);
    }

    @Test
    void deleteCluster_withNcaIdMismatch_throwsConflictException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        String wrongNcaId = "wrong_nca_id";

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () ->
                        clusterTerminateService.deleteCluster(wrongNcaId,
                                                              clusterEntity.getClusterId(),
                                                              new HashMap<>()));

        // Assert
        assertEquals("The provided cluster ID is registered with different ncaId. Specified ncaId wrong_nca_id, "
                             + "ncaId associated for cluster ncaId",
                     exception.getBody().getDetail());
        verify(clusterRepository).getClusterInfoByClusterId(clusterEntity.getClusterId(), false);
        verifyNoInteractions(auditService);
        verifyNoInteractions(nvcaClusterRepository);
        verifyNoInteractions(instanceServiceHelper);
        verifyNoInteractions(nvcaClusterConfigurationRepository);
    }

    @Test
    void deleteCluster_whenOperationThrowsException_throwsInternalServerException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of());
        doThrow(new RuntimeException("Database error"))
                .when(nvcaClusterConfigurationRepository).deleteByClusterId(clusterEntity.getClusterId());

        // Act
        IcmsInternalServerException exception = assertThrows(
                IcmsInternalServerException.class, () ->
                        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                                              clusterEntity.getClusterId(),
                                                              new HashMap<>()));

        // Assert
        assertEquals("Failed to un-register cluster, error: Database error",
                     exception.getBody().getDetail());
    }

    @Test
    void deleteCluster_withEmptyTerminationQueueUrl_doesNotDeleteTerminationQueue() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setTerminationQueueUrl("");
        clusterEntity.setCreationQueues(null);

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                              clusterEntity.getClusterId(),
                                              new HashMap<>());

        // Assert
        verify(queueManager, never()).deleteQueue(DUMMY_BYOC_TERMINATION_QUEUE_URL);
    }

    @Test
    void deleteCluster_withClusterTargetingEnabled_deletesClusterCreationQueues() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setAllowClusterTargeting(true);
        CreationQueueUdt creationQueue = CreationQueueUdt.builder()
                .url(DUMMY_CREATION_QUEUE_URL)
                .queueType("queue_type")
                .build();
        CreationQueueUdt clusterCreationQueue = CreationQueueUdt.builder()
                .url(DUMMY_CLUSTER_CREATION_QUEUE_URL)
                .queueType("queue_type")
                .build();
        clusterEntity.setCreationQueues(Map.of("AZURE", creationQueue));
        clusterEntity.setClusterCreationQueues(Map.of("AZURE", clusterCreationQueue));

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);
        when(clusterRepository.getClusterGroupInfoByClusterGroupId(clusterEntity.getClusterGroupId()))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                              clusterEntity.getClusterId(),
                                              new HashMap<>());

        // Assert
        verify(queueManager).deleteQueue(DUMMY_CREATION_QUEUE_URL);
        verify(queueManager).deleteQueue(DUMMY_CLUSTER_CREATION_QUEUE_URL);
        verify(queueManager).deleteQueue(DUMMY_BYOC_TERMINATION_QUEUE_URL);
    }

    @Test
    void deleteClusterForReconfiguration_singleClusterInGroup_noActiveInstances_success() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntity = new ClusterByGroupIdAndIdEntity();

        when(clusterRepository.getClustersFromClusterGroup(clusterEntity.getClusterGroupId()))
                .thenReturn(List.of(clusterByGroupIdAndIdEntity));
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(true);

        // Act
        clusterTerminateService.deleteClusterForReconfiguration(
                clusterEntity,
                clusterEntity.getClusterId(),
                List.of(),
                new HashMap<>());

        // Assert
        verify(nvcaClusterConfigurationRepository).deleteByClusterId(clusterEntity.getClusterId());
        verify(nvcaClusterRepository).deleteClusterInfo(clusterEntity);
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
    }

    @Test
    void deleteClusterForReconfiguration_multipleClustersInGroup_success() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        ClusterByGroupIdAndIdEntity cluster1 = new ClusterByGroupIdAndIdEntity();
        ClusterByGroupIdAndIdEntity cluster2 = new ClusterByGroupIdAndIdEntity();

        when(clusterRepository.getClustersFromClusterGroup(clusterEntity.getClusterGroupId()))
                .thenReturn(List.of(cluster1, cluster2));
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(true);

        // Act - with active instances, but multiple clusters so it should succeed
        clusterTerminateService.deleteClusterForReconfiguration(
                clusterEntity,
                clusterEntity.getClusterId(),
                List.of(DUMMY_RUNNING_INSTANCE_ID),
                new HashMap<>());

        // Assert
        verify(nvcaClusterConfigurationRepository).deleteByClusterId(clusterEntity.getClusterId());
        verify(nvcaClusterRepository).deleteClusterInfo(clusterEntity);
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
    }

    @Test
    void deleteClusterForReconfiguration_singleClusterWithActiveInstances_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntity = new ClusterByGroupIdAndIdEntity();

        when(clusterRepository.getClustersFromClusterGroup(clusterEntity.getClusterGroupId()))
                .thenReturn(List.of(clusterByGroupIdAndIdEntity));

        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () ->
                        clusterTerminateService.deleteClusterForReconfiguration(
                                clusterEntity,
                                clusterEntity.getClusterId(),
                                List.of(DUMMY_RUNNING_INSTANCE_ID, DUMMY_STARTING_INSTANCE_ID),
                                new HashMap<>()));

        // Assert
        assertEquals("Cluster reconfiguration failed, terminate following active instances "
                             + "[dummy_running_instance_id, dummy_starting_instance_Id]",
                     exception.getBody().getDetail());
        verifyNoInteractions(nvcaClusterRepository);
        verifyNoInteractions(auditService);
        verifyNoInteractions(nvcaClusterConfigurationRepository);
    }

    @Test
    void deleteClusterForReconfiguration_withNatsDisabled_deletesQueues() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntity = new ClusterByGroupIdAndIdEntity();
        CreationQueueUdt creationQueue = CreationQueueUdt.builder()
                .url(DUMMY_CREATION_QUEUE_URL)
                .queueType("queue_type")
                .build();
        clusterEntity.setCreationQueues(Map.of("AZURE", creationQueue));

        when(clusterRepository.getClustersFromClusterGroup(clusterEntity.getClusterGroupId()))
                .thenReturn(List.of(clusterByGroupIdAndIdEntity));
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);
        when(clusterRepository.getClusterGroupInfoByClusterGroupId(clusterEntity.getClusterGroupId()))
                .thenReturn(Optional.empty());
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterTerminateService.deleteClusterForReconfiguration(
                clusterEntity,
                clusterEntity.getClusterId(),
                List.of(),
                new HashMap<>());

        // Assert
        verify(queueManager).deleteQueue(DUMMY_CREATION_QUEUE_URL);
    }

    @Test
    void deleteClusterForReconfiguration_withNatsEnabled_doesNotDeleteQueues() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntity = new ClusterByGroupIdAndIdEntity();

        when(clusterRepository.getClustersFromClusterGroup(clusterEntity.getClusterGroupId()))
                .thenReturn(List.of(clusterByGroupIdAndIdEntity));
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(true);
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterTerminateService.deleteClusterForReconfiguration(
                clusterEntity,
                clusterEntity.getClusterId(),
                List.of(),
                new HashMap<>());

        // Assert
        verifyNoInteractions(queueManager);
    }

    @Test
    void validateNcaIdToDelete_withMatchingNcaIds_success() {
        // Act & Assert - should not throw any exception
        assertDoesNotThrow(() ->
                clusterTerminateService.validateNcaIdToDelete("ncaId", "ncaId"));
    }

    @Test
    void validateNcaIdToDelete_withMismatchedNcaIds_throwsException() {
        // Act
        IcmsConflictException exception = assertThrows(
                IcmsConflictException.class, () ->
                        clusterTerminateService.validateNcaIdToDelete("expectedNcaId", "actualNcaId"));

        // Assert
        assertEquals("The provided cluster ID is registered with different ncaId. Specified ncaId actualNcaId, "
                             + "ncaId associated for cluster expectedNcaId",
                     exception.getBody().getDetail());
    }

    @Test
    void deleteCluster_withNullTerminationQueueUrl_doesNotDeleteTerminationQueue() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setTerminationQueueUrl(null);
        clusterEntity.setCreationQueues(null);

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());

        // Act
        clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                              clusterEntity.getClusterId(),
                                              new HashMap<>());

        // Assert
        verify(queueManager, never()).deleteQueue(any());
    }

    @Test
    void deleteClusterOperations_sendsAuditAndTelemetryEvents() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        Map<String, Object> auditProps = new HashMap<>();

        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(true);

        // Act
        clusterTerminateService.deleteClusterOperations(clusterEntity,
                                                        clusterEntity.getClusterId(),
                                                        auditProps);

        // Assert
        verify(nvcaClusterConfigurationRepository).deleteByClusterId(clusterEntity.getClusterId());
        verify(nvcaClusterRepository).deleteClusterInfo(clusterEntity);
        verify(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void deleteCluster_telemetryFailure_doesNotThrowException() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();

        when(clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(instanceServiceHelper.getActiveInstancesFromZone(clusterEntity.getClusterId()))
                .thenReturn(List.of());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(true);
        doNothing().when(nvcaClusterRepository).deleteClusterInfo(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        doThrow(new RuntimeException("Telemetry error"))
                .when(telemetryEventClient).triggerEvent(anyList());

        // Act & Assert - should not throw exception despite telemetry failure
        assertDoesNotThrow(() ->
                clusterTerminateService.deleteCluster(clusterEntity.getNcaId(),
                                                      clusterEntity.getClusterId(),
                                                      new HashMap<>()));
    }
}
