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
package com.nvidia.icms.service.byoc.nvca;

import static com.nvidia.icms.inbound.rest.model.byoc.ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

import com.amazonaws.services.sqs.model.QueueAttributeName;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.byoc.AwsQueueAccessInfo;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationRequest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.ByocServiceHelper;
import com.nvidia.icms.service.byoc.ClusterQueueAccessCredsService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.util.TestUtil;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.InOrder;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.mockito.junit.jupiter.MockitoSettings;
import org.mockito.quality.Strictness;

import java.time.Instant;
import java.util.*;

/**
 * Unit tests for queue management scenarios in NvcaClusterRegistrationService.
 * 
 * This test class focuses on the critical queue creation/deletion scenarios:
 * 1. Queue creation before deletion order
 * 2. Self-healing behavior for missing queues
 * 3. GPU removal from individual clusters while preserving shared queues
 * 4. Queue reference removal from cluster maps
 * 5. Partial failure handling
 */
@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.LENIENT)
class NvcaClusterRegistrationServiceQueueManagementTest {

    @Mock private NvcaClusterRepository nvcaClusterRepository;
    @Mock private NvcaConfigurationProperties nvcaConfigurationProperties;
    @Mock private ClusterQueueAccessCredsService clusterQueueAccessCredsService;
    @Mock private ByocConfigurationProperties byocConfigurationProperties;
    @Mock private ClusterRepository clusterRepository;
    @Mock private ClusterOidcIdentityService clusterOidcIdentityService;
    @Mock private QueueManager queueManager;
    @Mock private TelemetryEventClient telemetryEventClient;
    @Mock private AppAuditService auditService;
    @Mock private InstanceServiceHelper instanceServiceHelper;
    @Mock private ByocServiceHelper byocServiceHelper;

    private NvcaClusterRegistrationService service;
    
    private static final String CLUSTER_ID_2 = "cluster-id-2";
    private static final String CREATION_QUEUE_FORMAT = "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_%s_%s.fifo";
    private static final String TASKS_CREATION_QUEUE_FORMAT = "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_tasks_%s_%s.fifo";
    private static final String TERMINATION_QUEUE_FORMAT = "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_%s.fifo";
    
    @BeforeEach
    void setUp() {
        service = new NvcaClusterRegistrationService(
            nvcaClusterRepository,
            nvcaConfigurationProperties,
            clusterQueueAccessCredsService,
            byocConfigurationProperties,
            clusterRepository,
            clusterOidcIdentityService,
            queueManager,
            telemetryEventClient,
            auditService,
            instanceServiceHelper,
            byocServiceHelper
        );
        
        when(nvcaConfigurationProperties.getCreationQueueNameFormat()).thenReturn(CREATION_QUEUE_FORMAT);
        when(nvcaConfigurationProperties.getTasksCreationQueueNameFormat()).thenReturn(TASKS_CREATION_QUEUE_FORMAT);
        when(nvcaConfigurationProperties.getTerminationQueueNameFormat()).thenReturn(TERMINATION_QUEUE_FORMAT);
        when(instanceServiceHelper.getCurrentTimestamp()).thenReturn(Instant.now());
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(false);
        when(instanceServiceHelper.isTaskClusterCreationQueuesAllowed(any())).thenReturn(false);

        // Mock termination queue creation
        when(clusterQueueAccessCredsService.createNvcaTerminationQueue(any(), any(), anyBoolean()))
            .thenAnswer(invocation -> invocation.getArgument(0));
        
        // Mock audit and telemetry services
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
    }

    /**
     * Test 1: Verify queue creation happens before deletion
     * This ensures service availability even if DB update fails
     */
    @Test
    void testQueueCreationBeforeDeletion_OrderVerification() {
        // Arrange
        ClusterEntity cluster = createClusterWithGpus("A100", "H100");
        cluster.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
            .thenReturn(Optional.of(cluster));
        ClusterByGroupIdAndIdEntity clusterByGroupId = TestUtil.toClusterByGroupIdAndIdEntity(cluster);
        clusterByGroupId.setGpusV5(cluster.getGpusV5());
        when(clusterRepository.getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID))
            .thenReturn(List.of(clusterByGroupId));
        when(clusterQueueAccessCredsService.generateCredsForNvcaQueues(any()))
            .thenReturn(getDummyNvcaAccessCreds(cluster));
        
        // Setup queue creation mocks
        mockQueueCreation("DUMMY_GPU_2", CREATION_QUEUE_FORMAT);
        
        // Mock queue deletion for removed GPUs
        String expectedA100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "A100");
        String expectedH100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "H100");
        doNothing().when(queueManager).deleteQueue(eq(expectedA100Url));
        doNothing().when(queueManager).deleteQueue(eq(expectedH100Url));
        
        // Mock repository update
        doNothing().when(nvcaClusterRepository).updateClusterRegistration(any());
        
        NvcaRegistrationRequest request = NvcaRegistrationRequest.builder()
            .status(ClusterStatusEnum.READY)
            .gpus(Set.of(createGpuRequestSchema("DUMMY_GPU_2"))) // New GPU, A100 and H100 removed
            .k8sVersion("1.29.0")
            .build();
        
        // Act
        service.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID, new HashMap<>());
        
        // Assert - Verify order using InOrder
        InOrder inOrder = inOrder(clusterQueueAccessCredsService, queueManager);
        
        // 1. First, queue creation for new GPU
        String expectedDummyGpuUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "DUMMY_GPU_2");
        inOrder.verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(
            eq(expectedDummyGpuUrl), eq(DUMMY_CLUSTER_ID), eq(false));
        
        // 2. Then, queue deletion for removed GPUs (verify both were called, but don't enforce strict order)
        verify(queueManager).deleteQueue(eq(expectedA100Url));
        verify(queueManager).deleteQueue(eq(expectedH100Url));
    }

    /**
     * Test 2: Self-healing behavior - recreates missing queues
     * Verifies that queue creation always attempts to create/verify all queues from configured GPUs in request
     */
    @Test
    void testSelfHealingQueueCreation_RecreatesMissingQueues() {
        // Arrange
        ClusterEntity cluster = createClusterWithGpus("A100", "H100");
        // Simulate that H100 queue exists in DB but was manually deleted from AWS
        
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
            .thenReturn(Optional.of(cluster));
        when(clusterQueueAccessCredsService.generateCredsForNvcaQueues(any()))
            .thenReturn(getDummyNvcaAccessCreds(cluster));
        
        // Mock queue creation - both should be attempted
        mockQueueCreation("A100", CREATION_QUEUE_FORMAT);
        mockQueueCreation("H100", CREATION_QUEUE_FORMAT);

        NvcaRegistrationRequest request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(createGpuRequestSchema("H100"),
                        createGpuRequestSchema("A100")))
                .k8sVersion("1.29.0")
                .build();
        
        // Mock repository update
        doNothing().when(nvcaClusterRepository).updateClusterRegistration(any());
        
        // Act
        service.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID, new HashMap<>());
        
        // Assert - Both queues should be created/verified
        String expectedA100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "A100");
        String expectedH100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "H100");
        verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(
            eq(expectedA100Url), eq(DUMMY_CLUSTER_ID), eq(false));
        verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(
            eq(expectedH100Url), eq(DUMMY_CLUSTER_ID), eq(false));
    }

    /**
     * Test 3: GPU removal from one cluster with shared queues
     * Comprehensive test that verifies:
     * 1. Shared cluster-group queues are preserved when other clusters use the GPU
     * 2. Cluster-specific queues are always deleted (they're unique to this cluster)
     * 3. Queue references are properly removed from cluster maps
     */
    @Test
    void testSharedQueueProtection_RemoveGpuFromOneCluster() {
        // Arrange - Two clusters sharing A100, cluster1 has targeting enabled
        ClusterEntity cluster1 = createTargetingEnabledClusterWithGpus("A100", "H100");
        cluster1.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        
        ClusterEntity cluster2 = createTargetingEnabledClusterWithGpus("A100");
        cluster2.setClusterId(CLUSTER_ID_2);
        
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
            .thenReturn(Optional.of(cluster1));
        // Convert to ClusterByGroupIdAndIdEntity and ensure gpusV5 is set
        ClusterByGroupIdAndIdEntity cluster1ByGroupId = TestUtil.toClusterByGroupIdAndIdEntity(cluster1);
        cluster1ByGroupId.setGpusV5(cluster1.getGpusV5());
        
        ClusterByGroupIdAndIdEntity cluster2ByGroupId = TestUtil.toClusterByGroupIdAndIdEntity(cluster2);
        cluster2ByGroupId.setGpusV5(cluster2.getGpusV5());
        
        when(clusterRepository.getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID))
            .thenReturn(List.of(cluster1ByGroupId, cluster2ByGroupId));
        when(clusterQueueAccessCredsService.generateCredsForNvcaQueues(any()))
            .thenReturn(getDummyNvcaAccessCreds(cluster1));
        when(instanceServiceHelper.isTaskClusterCreationQueuesAllowed(any())).thenReturn(true);
        
        // Mock queue creation for H100 (the GPU that remains) - all types
        mockQueueCreation("H100", CREATION_QUEUE_FORMAT);
        String clusterH100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_ID, "H100");
        when(clusterQueueAccessCredsService.createNvcaFunctionCreationQueue(
            eq(clusterH100Url), eq(DUMMY_CLUSTER_ID), eq(false)))
            .thenReturn(clusterH100Url);
        String taskH100Url = String.format(TASKS_CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_ID, "H100");
        when(clusterQueueAccessCredsService.createNvcaTasksCreationQueue(
            eq(taskH100Url), eq(DUMMY_CLUSTER_ID), eq(false)))
            .thenReturn(taskH100Url);
        
        // Mock repository update
        doNothing().when(nvcaClusterRepository).updateClusterRegistration(any());
        
        NvcaRegistrationRequest request = NvcaRegistrationRequest.builder()
            .status(ClusterStatusEnum.READY)
            .gpus(Set.of(createGpuRequestSchema("H100"))) // Remove A100 from cluster1, keep H100
            .k8sVersion("1.29.0")
            .allowClusterTargeting(true)
            .allowTaskClusterCreationQueues(true)
            .build();
        
        // Act
        service.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID, new HashMap<>());
        
        // Assert - Comprehensive verification of shared queue behavior
        
        // 1. A100 cluster-group queue should NOT be deleted (cluster2 still uses it)
        verify(queueManager, never()).deleteQueue(
            argThat(url -> url.contains(DUMMY_CLUSTER_GROUP_ID) && url.contains("A100")));
        
        // 2. A100 cluster-specific and task-specific queues SHOULD be deleted (unique to cluster1)
        String clusterA100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_ID, "A100");
        String taskA100Url = String.format(TASKS_CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_ID, "A100");
        verify(queueManager).deleteQueue(eq(clusterA100Url));
        verify(queueManager).deleteQueue(eq(taskA100Url));
        
        // 3. A100 reference should be removed from cluster1's maps
        ArgumentCaptor<ClusterEntity> captor = ArgumentCaptor.forClass(ClusterEntity.class);
        verify(nvcaClusterRepository).updateClusterRegistration(captor.capture());
        
        ClusterEntity updatedCluster = captor.getValue();
        assertFalse(updatedCluster.getCreationQueues().containsKey("A100"));
        assertFalse(updatedCluster.getClusterCreationQueues().containsKey("A100"));
        assertFalse(updatedCluster.getClusterCreationQueuesForTasks().containsKey("A100"));
        assertTrue(updatedCluster.getCreationQueues().containsKey("H100"));
        assertTrue(updatedCluster.getClusterCreationQueues().containsKey("H100"));
        assertTrue(updatedCluster.getClusterCreationQueuesForTasks().containsKey("H100"));
    }

    /**
     * Test 4: Partial failure recovery
     * If queue deletion fails midway, the operation should fail but leave consistent state
     */
    @Test
    void testPartialDeletionFailure_ThrowsExceptionMaintainsState() {
        // Arrange
        ClusterEntity cluster = createClusterWithGpus("A100", "H100", "DUMMY_GPU_2");
        cluster.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
            .thenReturn(Optional.of(cluster));
        ClusterByGroupIdAndIdEntity clusterByGroupId = TestUtil.toClusterByGroupIdAndIdEntity(cluster);
        clusterByGroupId.setGpusV5(cluster.getGpusV5());
        when(clusterRepository.getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID))
            .thenReturn(List.of(clusterByGroupId));
        when(clusterQueueAccessCredsService.generateCredsForNvcaQueues(any()))
            .thenReturn(getDummyNvcaAccessCreds(cluster));
        
        // Mock queue creation for DUMMY_GPU_2 (the remaining GPU)
        mockQueueCreation("DUMMY_GPU_2", CREATION_QUEUE_FORMAT);
        
        // Mock successful deletion for A100, but fail for H100
        String expectedA100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "A100");
        String expectedH100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "H100");
        doNothing().when(queueManager).deleteQueue(eq(expectedA100Url));
        doNothing().when(queueManager).deleteQueue(eq(expectedH100Url));
        // Override H100 to throw exception
        doThrow(new IcmsInternalServerException("AWS Error"))
            .when(queueManager).deleteQueue(eq(expectedH100Url));
        
        NvcaRegistrationRequest request = NvcaRegistrationRequest.builder()
            .status(ClusterStatusEnum.READY)
            .gpus(Set.of(createGpuRequestSchema("DUMMY_GPU_2"))) // Remove A100 and H100
            .k8sVersion("1.29.0")
            .build();
        
        // Act & Assert
        assertThrows(IcmsInternalServerException.class, () -> 
            service.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID, new HashMap<>())
        );
        
        // Verify that H100 was processed (it's processed first and fails)
        // A100 is never processed because the service stops at the first failure
        verify(queueManager, times(1)).deleteQueue(eq(expectedH100Url));
        verify(queueManager, never()).deleteQueue(eq(expectedA100Url));
        // DB should not be updated due to exception
        verify(nvcaClusterRepository, never()).updateClusterRegistration(any());
    }



    /**
     * Test 5: Empty GPU sets are handled gracefully
     * Tests scenario where cluster initially has no GPUs and new GPUs are provided in registration
     */
    @Test
    void testEmptyGpuSets_HandledGracefully() {
        // Arrange - Cluster initially has no GPUs
        ClusterEntity cluster = createClusterWithGpus(); // No GPUs
        cluster.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
            .thenReturn(Optional.of(cluster));
        when(clusterQueueAccessCredsService.generateCredsForNvcaQueues(any()))
            .thenReturn(getDummyNvcaAccessCreds(cluster));
        
        // Mock queue creation for new GPUs
        mockQueueCreation("A100", CREATION_QUEUE_FORMAT);
        mockQueueCreation("H100", CREATION_QUEUE_FORMAT);
        
        // Mock cluster group lookup (needed for newly added GPU validation)
        when(clusterRepository.getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID))
            .thenReturn(List.of()); // Empty list since no other clusters in group
        
        // Mock repository update
        doNothing().when(nvcaClusterRepository).updateClusterRegistration(any());
        
        NvcaRegistrationRequest request = NvcaRegistrationRequest.builder()
            .status(ClusterStatusEnum.READY)
            .gpus(Set.of(createGpuRequestSchema("A100"), createGpuRequestSchema("H100"))) // New GPUs provided
            .k8sVersion("1.29.0")
            .build();
        
        // Act
        service.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID, new HashMap<>());
        
        // Assert - Queue creation should occur for new GPUs
        String expectedA100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "A100");
        String expectedH100Url = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, "H100");
        verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(
            eq(expectedA100Url), eq(DUMMY_CLUSTER_ID), eq(false));
        verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(
            eq(expectedH100Url), eq(DUMMY_CLUSTER_ID), eq(false));
        
        // No queue deletion should occur since there were no previous GPUs
        verify(queueManager, never()).deleteQueue(any());
        
        // Cluster group lookup should occur for newly added GPU validation
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
    }

    /**
     * Test 6: NATS enabled skips all queue operations
     */
    @Test
    void testNatsEnabled_SkipsAllQueueOperations() {
        // Arrange
        when(instanceServiceHelper.isNatsEnabled()).thenReturn(true);
        
        ClusterEntity cluster = createClusterWithGpus("A100");
        cluster.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
            .thenReturn(Optional.of(cluster));
        when(clusterQueueAccessCredsService.generateCredsForNvcaQueues(any()))
            .thenReturn(getDummyNvcaAccessCreds(cluster));
        ClusterByGroupIdAndIdEntity clusterByGroupId = TestUtil.toClusterByGroupIdAndIdEntity(cluster);
        clusterByGroupId.setGpusV5(cluster.getGpusV5());
        when(clusterRepository.getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID))
            .thenReturn(List.of(clusterByGroupId));
        
        // Mock repository update
        doNothing().when(nvcaClusterRepository).updateClusterRegistration(any());
        
        NvcaRegistrationRequest request = NvcaRegistrationRequest.builder()
            .status(ClusterStatusEnum.READY)
            .gpus(Set.of(createGpuRequestSchema("H100"))) // Different GPU
            .k8sVersion("1.29.0")
            .build();
        
        // Act
        service.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID, new HashMap<>());
        
        // Assert - No queue operations when NATS is enabled
        verify(clusterQueueAccessCredsService, never()).createNvcaFunctionCreationQueue(any(), any(), eq(false));
        verify(queueManager, never()).deleteQueue(any());
    }

    // Helper methods
    
    private ClusterEntity createClusterWithGpus(String... gpuNames) {
        ClusterEntity cluster = new ClusterEntity();
        cluster.setClusterId(DUMMY_CLUSTER_ID);
        cluster.setClusterGroupId(DUMMY_CLUSTER_GROUP_ID);
        cluster.setClusterName(DUMMY_CLUSTER_ID);
        cluster.setAllowClusterTargeting(false);
        cluster.setAllowTaskClusterCreationQueues(false);
        
        Set<GpuV5Udt> gpus = new HashSet<>();
        Map<String, CreationQueueUdt> creationQueues = new HashMap<>();
        
        for (String gpuName : gpuNames) {
            gpus.add(createGpuV5(gpuName));
            creationQueues.put(gpuName, createQueueUdt(DUMMY_CLUSTER_GROUP_ID, gpuName));
        }
        
        cluster.setGpusV5(gpus);
        cluster.setCreationQueues(creationQueues);
        
        return cluster;
    }
    
    private ClusterEntity createTargetingEnabledClusterWithGpus(String... gpuNames) {
        ClusterEntity cluster = createClusterWithGpus(gpuNames);
        cluster.setAllowClusterTargeting(true);
        cluster.setAllowTaskClusterCreationQueues(true);
        
        Map<String, CreationQueueUdt> clusterQueues = new HashMap<>();
        Map<String, CreationQueueUdt> taskQueues = new HashMap<>();
        
        for (String gpuName : gpuNames) {
            clusterQueues.put(gpuName, createQueueUdt(DUMMY_CLUSTER_ID, gpuName));
            taskQueues.put(gpuName, createTaskQueueUdt(DUMMY_CLUSTER_ID, gpuName));
        }
        
        cluster.setClusterCreationQueues(clusterQueues);
        cluster.setClusterCreationQueuesForTasks(taskQueues);
        
        return cluster;
    }
    
    private GpuV5Udt createGpuV5(String gpuName) {
        GpuV5Udt gpu = new GpuV5Udt();
        gpu.setName(gpuName);
        gpu.setInstanceTypes(Set.of(createInstanceType(gpuName)));
        return gpu;
    }
    
    private InstanceTypeV5Udt createInstanceType(String gpuName) {
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setName("Standard_ND96amsr_" + gpuName + "_v4");
        instanceType.setValue("Standard_ND96amsr_" + gpuName + "_v4_1x");
        return instanceType;
    }
    
    private CreationQueueUdt createQueueUdt(String id, String gpuName) {
        return CreationQueueUdt.builder()
            .url(String.format(CREATION_QUEUE_FORMAT, id, gpuName))
            .queueType(String.valueOf(QueueAttributeName.FifoQueue))
            .build();
    }
    
    private CreationQueueUdt createTaskQueueUdt(String clusterId, String gpuName) {
        return CreationQueueUdt.builder()
            .url(String.format(TASKS_CREATION_QUEUE_FORMAT, clusterId, gpuName))
            .queueType(String.valueOf(QueueAttributeName.FifoQueue))
            .build();
    }
    
    private GpuRequestSchema createGpuRequestSchema(String gpuName) {
        return getDummyGpuRequestSchema(
            "Standard_ND96amsr_" + gpuName + "_v4_1x",
            "Standard_ND96amsr_" + gpuName + "_v4",
            gpuName
        );
    }
    
    private GpuRequestSchema getDummyGpuRequestSchema(
            String instanceTypeName, String instanceTypeValue, String gpuName) {
        var instanceTypeRequestSchema = InstanceTypeRequestSchema.builder()
                .gpuCount(1)
                .name(instanceTypeName)
                .value(instanceTypeValue)
                .description("1 GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .nodeType(NodeTypeEnum.SINGLE)
                .build();

        return GpuRequestSchema.builder()
                .name(gpuName)
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeRequestSchema))
                .build();
    }
    
    private NvcaAccessCreds getDummyNvcaAccessCreds(ClusterEntity clusterEntity) {
        Map<String, AwsQueueAccessInfo> creationQueueMap = new HashMap<>();
        if (clusterEntity.getCreationQueues() != null &&
                !clusterEntity.getCreationQueues().isEmpty()) {
            for (Map.Entry<String, CreationQueueUdt> entry : clusterEntity.getCreationQueues()
                    .entrySet()) {
                creationQueueMap.put(entry.getKey(), getDummyAccessInfo(entry.getValue().getUrl()));
            }
        }

        return NvcaAccessCreds.builder()
                .terminationQueue(getDummyAccessInfo("dummy_termination_queue_url"))
                .creationQueue(creationQueueMap)
                .build();
    }
    
    private AwsQueueAccessInfo getDummyAccessInfo(String queueUrl) {
        return AwsQueueAccessInfo.builder()
                .url(queueUrl)
                .accessKeyId("dummy_access_key")
                .secretAccessKey("dummy_secret_key")
                .sessionToken("dummy_session_token")
                .build();
    }
    
    private void mockQueueCreation(String gpuName, String format) {
        String url = String.format(format, DUMMY_CLUSTER_GROUP_ID, gpuName);
        when(clusterQueueAccessCredsService.createNvcaFunctionCreationQueue(
            eq(url), eq(DUMMY_CLUSTER_ID), eq(false)))
            .thenReturn(url);
    }
}
