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
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toGpusV4;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getMetadataForCluster;
import static com.nvidia.icms.util.TestUtil.*;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;

import com.amazonaws.services.sqs.model.QueueAttributeName;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;

import com.nvidia.icms.inbound.rest.model.byoc.AwsQueueAccessInfo;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeRequestSchema;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;

import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationRequest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV4Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.ByocServiceHelper;
import com.nvidia.icms.service.byoc.ClusterQueueAccessCredsService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.AuthUtils;
import com.nvidia.icms.util.TestUtil;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;

@ExtendWith(MockitoExtension.class)
class NvcaClusterRegistrationServiceTest {

    @Mock
    private NvcaClusterRepository nvcaClusterRepository;

    @Mock
    private NvcaConfigurationProperties nvcaConfigurationProperties;

    @Mock
    private ClusterQueueAccessCredsService clusterQueueAccessCredsService;

    @Mock
    private ByocConfigurationProperties byocConfigurationProperties;

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    private ClusterOidcIdentityService clusterOidcIdentityService;

    @Mock
    private QueueManager queueManager;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private AppAuditService auditService;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    @Mock
    private ByocServiceHelper byocServiceHelper;

    private NvcaClusterRegistrationService nvcaClusterRegistrationService;

    private final Instant dummyInstant = Instant.now();
    private final String TERMINATION_QUEUE_URL = "q_gdn_spot_byoc_cluster_id.fifo";
    private final String CREATION_QUEUE_FORMAT = "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_%s_%s.fifo";
    private final String TASKS_CLUSTER_CREATION_QUEUE_FORMAT = "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_tasks_%s_%s.fifo";
    private final String TERMINATION_QUEUE_FORMAT = "q_gdn_spot_byoc_%s.fifo";
    private final String LONG_TERMINATION_QUEUE_URL = "q_gdn_spot_byoc_ter_id_111111111111111111111111111111111111.fifo";

    private final String A100GpuName = "A100";
    private final String A200GpuName = "A200";

    @BeforeEach
    void init() {
        nvcaClusterRegistrationService = new NvcaClusterRegistrationService(nvcaClusterRepository,
                nvcaConfigurationProperties,
                clusterQueueAccessCredsService,
                byocConfigurationProperties,
                clusterRepository,
                clusterOidcIdentityService,
                queueManager,
                                                                            telemetryEventClient,
                auditService,
                instanceServiceHelper,
                byocServiceHelper);
    }

    @Test
    void nvcaClusterRegistration_GpusNotProvidedAndAlreadyConfiguredForFirstTimeRegistration_returnsSuccess() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                         false, false);
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);

        var creationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(nvcaConfigurationProperties).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @Test
    void nvcaClusterRegistration_FirstTimeRegistrationWithTasks_returnsSuccess() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_LONG_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          true, true);
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_LONG_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                         true, true);
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        doReturn(true).when(instanceServiceHelper).isTaskClusterCreationQueuesAllowed(Boolean.TRUE);
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_LONG_CLUSTER_ID, false);
        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);

        var creationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        var clusterCreationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_LONG_CLUSTER_ID, A100GpuName);
        var clusterTaskCreationQueueUrl =
                String.format(TASKS_CLUSTER_CREATION_QUEUE_FORMAT, DUMMY_LONG_CLUSTER_ID_TRUNCATED, A100GpuName);

        doReturn(true).when(nvcaConfigurationProperties)
                .isTasksCreationQueuesEnabled();
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(TASKS_CLUSTER_CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTasksCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();


        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_LONG_CLUSTER_ID, false);
        doReturn(clusterCreationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(clusterCreationQueueUrl, DUMMY_LONG_CLUSTER_ID, false);
        doReturn(clusterTaskCreationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaTasksCreationQueue(clusterTaskCreationQueueUrl, DUMMY_LONG_CLUSTER_ID, false);

        doReturn(LONG_TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(LONG_TERMINATION_QUEUE_URL, DUMMY_LONG_CLUSTER_ID, false);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .allowClusterTargeting(Boolean.TRUE)
                .allowTaskClusterCreationQueues(Boolean.TRUE)
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_LONG_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_LONG_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_LONG_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(nvcaConfigurationProperties, times(2)).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @Test
    void nvcaClusterRegistration_FirstTimeRegistrationWithTasks_withNatsEnabled_returnsEmptySuccess() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_LONG_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          true, true);
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_LONG_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                         true, true);
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_LONG_CLUSTER_ID, false);
        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);

        NvcaAccessCreds nvcaAccessCreds = NvcaAccessCreds.builder()
                .terminationQueue(new AwsQueueAccessInfo())
                .creationQueue(Map.of())
                .clusterCreationQueue(Map.of())
                .clusterCreationQueueForTasks(Map.of())
                .build();
        doReturn(nvcaAccessCreds).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);

        doReturn(true).when(nvcaConfigurationProperties)
                .isTasksCreationQueuesEnabled();
        doReturn(true).when(instanceServiceHelper).isNatsEnabled();

        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .allowClusterTargeting(Boolean.TRUE)
                .allowTaskClusterCreationQueues(Boolean.TRUE)
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_LONG_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_LONG_CLUSTER_ID, response.getClusterId());
        assertEquals(nvcaAccessCreds, response.getCredentials());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_LONG_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(nvcaConfigurationProperties, times(0)).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties, times(0)).getTerminationQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @Test
    void nvcaClusterRegistration_GpusProvidedInRequestForFirstTimeRegistration__withMultiNodeType_returnsSuccess() {
        // Prepare
        GpuV5Udt gpuV5Udt = getDummyA100GpuV5();
        gpuV5Udt.getInstanceTypes().forEach(
                instanceTypeV5Udt -> instanceTypeV5Udt.setNodeType(NodeTypeEnum.MULTI.toString()));

        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, null, false,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(gpuV5Udt), true,
                                                         false, false);
        updatedClusterEntity.setCapabilities(existingClusterEntity.getCapabilities());
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        // Returning non configured cluster entity
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);
        doReturn(List.of(TestUtil.toClusterByGroupIdAndIdEntity(existingClusterEntity))).when(
                clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var creationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        GpuRequestSchema gpuRequestSchema = getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                     A100GpuName);
        gpuRequestSchema.getInstanceTypes().forEach(
                instanceTypeRequestSchema -> instanceTypeRequestSchema.setNodeType(
                        NodeTypeEnum.MULTI));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(gpuRequestSchema))
                .k8sVersion("1.29.0")
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(nvcaConfigurationProperties).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @Test
    void nvcaClusterRegistration_GpusProvidedInRequestForFirstTimeRegistration__withNodeTypeNotSet_returnsSuccess() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, null, false,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                         false, false);
        updatedClusterEntity.setCapabilities(existingClusterEntity.getCapabilities());
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        // Returning non configured cluster entity
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);
        doReturn(List.of(TestUtil.toClusterByGroupIdAndIdEntity(existingClusterEntity))).when(
                clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var creationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        GpuRequestSchema gpuRequestSchema = getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                                     DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                     A100GpuName);
        gpuRequestSchema.getInstanceTypes().forEach(
                instanceTypeRequestSchema -> instanceTypeRequestSchema.setNodeType(null));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(gpuRequestSchema))
                .k8sVersion("1.29.0")
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(nvcaConfigurationProperties).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @Test
    void nvcaClusterRegistration_sameConfiguredGpusProvidedAfterInitialRegistration_returnsSuccess() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                         false, false);
        updatedClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        // Returning non configured cluster entity
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var creationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      A100GpuName)))
                .k8sVersion("1.29.0")
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(nvcaConfigurationProperties).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(clusterRepository, times(0)).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @Test
    void nvcaClusterRegistration_GpusRemovedAfterInitialRegistration_returnsSuccess() {
        // Prepare
        var queueUrlToBeDeleted = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID,
                                                A100GpuName);
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA200GpuV5()), true,
                                                         false, false);
        updatedClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());


        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        // Returning non configured cluster entity
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);
        doReturn(List.of(TestUtil.toClusterByGroupIdAndIdEntity(existingClusterEntity))).when(
                clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        doNothing().when(queueManager).deleteQueue(queueUrlToBeDeleted);
        doReturn(Set.of()).when(instanceServiceHelper)
                .getActiveInstancesFromZoneForInstanceType(DUMMY_CLUSTER_ID,
                                                           Set.of("Standard_ND96amsr_A100_v4_1x"));
        doReturn(false).when(instanceServiceHelper).isTaskClusterCreationQueuesAllowed(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var creationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A200GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        // Self-healing only creates queues for provided GPUs (A200), not removed ones (A100)
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema("Standard_ND96amsr_A200_v4_1x",
                                                      "Standard_ND96amsr_A200_v4",
                                                      "A200")))
                .k8sVersion("1.29.0")
                .build();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        // With self-healing, we only create queues for provided GPUs (A200), not removed ones (A100)
        verify(nvcaConfigurationProperties).getCreationQueueNameFormat();
        verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        verify(queueManager).deleteQueue(queueUrlToBeDeleted);
        verify(instanceServiceHelper).getActiveInstancesFromZoneForInstanceType(DUMMY_CLUSTER_ID,
                                                                        Set.of("Standard_ND96amsr_A100_v4_1x"));
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @Test
    void nvcaClusterRegistration_GpusRemovedAndActiveInstancesPresent_throwsException() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA200GpuV5()), true,
                                                         false, false);
        updatedClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));

        // Returning non configured cluster entity
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doReturn(List.of(TestUtil.toClusterByGroupIdAndIdEntity(existingClusterEntity))).when(
                clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        doReturn(Set.of("instance_id_1", "instance_id2")).when(instanceServiceHelper)
                .getActiveInstancesFromZoneForInstanceType(DUMMY_CLUSTER_ID,
                                                           Set.of("Standard_ND96amsr_A100_v4_1x"));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema("Standard_ND96amsr_A200_v4_1x",
                                                      "Standard_ND96amsr_A200_v4",
                                                      "A200")))
                .build();
        // Act
        var exception = assertThrows(IcmsConflictException.class, () -> {
            nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                   new HashMap<>());
        });

        // Assert
        assertNotNull(exception);
        assertTrue(exception.getBody().getDetail().contains(
                "cluster registration failed, active instances exists for removed [Standard_ND96amsr_A100_v4_1x] instanceTypes"));

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verifyNoInteractions(nvcaConfigurationProperties);
        verifyNoInteractions(queueManager);
        verifyNoInteractions(clusterQueueAccessCredsService);
        verifyNoInteractions(nvcaClusterRepository);
        verify(instanceServiceHelper).getActiveInstancesFromZoneForInstanceType(DUMMY_CLUSTER_ID,
                                                                        Set.of("Standard_ND96amsr_A100_v4_1x"));
    }

    @Test
    void nvcaClusterRegistration_GpusAddedAfterInitialRegistration_returnsSuccess() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        var updatedClusterEntity =
                getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5(), getDummyA200GpuV5()), true,
                                      false, false);
        updatedClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        // Returning non configured cluster entity
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);
        doReturn(List.of(TestUtil.toClusterByGroupIdAndIdEntity(existingClusterEntity))).when(
                clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
        doReturn(false).when(instanceServiceHelper).isTaskClusterCreationQueuesAllowed(Mockito.any());

        var creationQueueUrlForA100 =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        var creationQueueUrlForA200 =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A200GpuName);

        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();

        doReturn(creationQueueUrlForA100).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrlForA100, DUMMY_CLUSTER_ID, false);
        doReturn(creationQueueUrlForA200).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrlForA200, DUMMY_CLUSTER_ID, false);

        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      A100GpuName),
                             getDummyGpuRequestSchema("Standard_ND96amsr_A200_v4_1x",
                                                      "Standard_ND96amsr_A200_v4",
                                                      "A200")))
                .k8sVersion("1.29.0")
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(nvcaConfigurationProperties, times(2)).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());

        //Yury: This validation is incorrect because order if GPUs and instance types are not predictable
        // and be different between runs. Dat ais the same, but sting comparison for arguments fails
        // We should add more complex logic for validation if we need it
        /*verify(telemetryClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));*/
    }

    @Test
    void nvcaClusterRegistration_instanceTypeUpdatedAfterInitialRegistration_returnsSuccess() {
        // Prepare
        var updatedInstanceTypeName = "Standard_ND96amsr_A100A_v4";
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        var updatedClusterEntity =
                getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5(updatedInstanceTypeName)),
                                      true, false, false);
        updatedClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        // Returning non configured cluster entity
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);
        doReturn(List.of(TestUtil.toClusterByGroupIdAndIdEntity(existingClusterEntity))).when(
                clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        doReturn(false).when(instanceServiceHelper).isTaskClusterCreationQueuesAllowed(Mockito.any());
        doReturn(Set.of()).when(instanceServiceHelper)
                .getActiveInstancesFromZoneForInstanceType(DUMMY_CLUSTER_ID,
                                                           Set.of("Standard_ND96amsr_A100_v4_1x"));
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        // Mock queue creation for self-healing
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema(updatedInstanceTypeName,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      A100GpuName)))
                .k8sVersion("1.29.0")
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        // Self-healing will create/verify queues
        verify(nvcaConfigurationProperties).getCreationQueueNameFormat();
        verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verify(queueManager, times(0)).deleteQueue(Mockito.any());
        verify(instanceServiceHelper).getActiveInstancesFromZoneForInstanceType(DUMMY_CLUSTER_ID,
                                                                        Set.of("Standard_ND96amsr_A100_v4_1x"));
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    // Error cases
    @Test
    void nvcaClusterRegistration_GpusProvidedAndAuthorizedNcaIdDifferent_throwsException() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, null, false,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        var otherClusterWithDifferentAuthorizedNcaId =
                getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                      false, false);
        otherClusterWithDifferentAuthorizedNcaId.setAuthorizedNcaIds(Set.of("ncaId1", "ncaId3"));
        otherClusterWithDifferentAuthorizedNcaId.setClusterId("dummy_cluster_id_1");

        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doReturn(List.of(TestUtil.toClusterByGroupIdAndIdEntity(
                otherClusterWithDifferentAuthorizedNcaId))).when(
                clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      A100GpuName)))
                .build();
        // Act
        var exception = assertThrows(IcmsConflictException.class, () -> {
            nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                   new HashMap<>());
        });

        // Assert
        assertNotNull(exception);
        assertTrue(exception.getBody().getDetail().contains(
                "A100 GPU is already present in cluster-name cluster from same group_name clusterGroup"));

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(clusterRepository).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verifyNoInteractions(nvcaClusterRepository);
        verify(clusterQueueAccessCredsService, times(0)).generateCredsForNvcaQueues(Mockito.any());
        verify(nvcaConfigurationProperties, times(0)).getTerminationQueueNameFormat();
        verify(nvcaConfigurationProperties, times(0)).getCreationQueueNameFormat();
    }

    @Test
    void nvcaClusterRegistration_GpusNotProvidedAndDynamicGpuDiscoveryEnabled_throwsException() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, null, false,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));

        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of())
                .build();
        // Act
        var exception = assertThrows(IcmsBadRequestException.class, () -> {
            nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                   new HashMap<>());
        });

        // Assert
        assertNotNull(exception);
        assertEquals("GPUs are not provided in the request",
                     exception.getBody().getDetail());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(clusterRepository, times(0)).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verifyNoInteractions(nvcaClusterRepository);
        verify(clusterQueueAccessCredsService, times(0)).generateCredsForNvcaQueues(Mockito.any());
        verify(nvcaConfigurationProperties, times(0)).getTerminationQueueNameFormat();
        verify(nvcaConfigurationProperties, times(0)).getCreationQueueNameFormat();
    }

    @Test
    void nvcaClusterRegistration_ClusterInfoNotFound_throwsException() {
        // Prepare
        doReturn(Optional.empty()).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      A100GpuName)))
                .build();
        // Act
        var exception = assertThrows(IcmsNotFoundException.class, () -> {
            nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                   new HashMap<>());
        });

        // Assert
        assertNotNull(exception);
        assertEquals("Cluster with clusterId cluster_id doesn't exists",
                     exception.getBody().getDetail());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(clusterRepository, times(0)).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verifyNoInteractions(nvcaClusterRepository);
        verify(clusterQueueAccessCredsService, times(0)).generateCredsForNvcaQueues(Mockito.any());
        verify(nvcaConfigurationProperties, times(0)).getTerminationQueueNameFormat();
        verify(nvcaConfigurationProperties, times(0)).getCreationQueueNameFormat();
    }

    @Test
    void nvcaClusterRegistration_invalidStatusProvided_throwsException() {
        // Prepare
        doReturn(Optional.of(getDummyClusterEntity(DUMMY_CLUSTER_ID, null, false,
                                                   false, false))).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .gpus(Set.of(getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      A100GpuName)))
                .k8sVersion("1.30.0")
                .status(ClusterStatusEnum.FAILED)
                .build();
        // Act
        var exception = assertThrows(IcmsBadRequestException.class, () -> {
            nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                   new HashMap<>());
        });

        // Assert
        assertNotNull(exception);
        assertEquals("Cluster status must be one of [READY, CORDON, CORDON_AND_DRAIN]", exception.getBody().getDetail());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(clusterRepository, times(0)).getClustersFromClusterGroup(DUMMY_CLUSTER_GROUP_ID);
        verifyNoInteractions(nvcaClusterRepository);
        verifyNoInteractions(clusterQueueAccessCredsService);
        verifyNoInteractions(byocConfigurationProperties);
        verifyNoInteractions(nvcaConfigurationProperties);
    }

    @Test
    void nvcaClusterRegistration_GpuSizeIsInvalid_throwsException() {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, new HashSet<>(), false,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        var uuid = UUID.randomUUID().toString();
        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      uuid)))
                .k8sVersion("1.29.0")
                .build();
        // Act

        IcmsBadRequestException badRequestException =
                assertThrows(IcmsBadRequestException.class,
                             () -> nvcaClusterRegistrationService.nvcaClusterRegistration(request,
                                                                                          DUMMY_CLUSTER_ID,
                                                                                          new HashMap<>()));

        // Assert
        assertEquals(
                String.format("GPU name chars must be <=22, provided %s GPU name is of 36 size",
                              uuid),
                badRequestException.getBody().getDetail());
    }

    @Test
    void nvcaClusterRegistration_GpuNameContainsInvalidChars_throwsException() {
        // Prepare
        String invalidGpuName = "ON-PREM.GPU.IB-H200";
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, new HashSet<>(), false,
                                                          false, false);
        existingClusterEntity.setCapabilities(Set.of(DYNAMIC_GPU_DISCOVERY.toString()));
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      invalidGpuName)))
                .k8sVersion("1.29.0")
                .build();
        // Act
        IcmsBadRequestException badRequestException =
                assertThrows(IcmsBadRequestException.class,
                             () -> nvcaClusterRegistrationService.nvcaClusterRegistration(request,
                                                                                          DUMMY_CLUSTER_ID,
                                                                                          new HashMap<>()));

        // Assert
        assertEquals("GPU name 'ON-PREM.GPU.IB-H200' contains invalid characters. Only alphanumeric characters, hyphens (-), and underscores (_) are allowed.", badRequestException.getBody().getDetail());
    }

    @Test
    void nvcaClusterRegistration_Targeted_returnSuccess() {
        // Prepare
        var clusterEntity = getDummyClusterEntityTargetingEnabled();

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        // Returning non configured cluster entity
        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();

        doReturn(getDummyNvcaAccessCreds(clusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(clusterEntity);

        // Mock queue creation for self-healing with targeting enabled
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        var clusterCreationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_ID, A100GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(clusterCreationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(clusterCreationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        doReturn(false).when(instanceServiceHelper).isTaskClusterCreationQueuesAllowed(Mockito.any());

        doNothing().when(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(
                telemetryEventClient).triggerEvent(getDummyGenericMetricForClusterRegistration(clusterEntity));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .gpus(Set.of(getDummyGpuRequestSchema(DUMMY_BYOC_INSTANCE_TYPE,
                                                      DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                      A100GpuName)))
                .k8sVersion("1.29.0")
                .allowClusterTargeting(Boolean.TRUE)
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(clusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(clusterEntity);
        // With self-healing and targeting enabled, queues are created
        verify(nvcaConfigurationProperties, times(2)).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        verify(clusterQueueAccessCredsService).createNvcaFunctionCreationQueue(clusterCreationQueueUrl, DUMMY_CLUSTER_ID, false);
        verify(clusterQueueAccessCredsService).createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        verify(queueManager, times(0)).deleteQueue(Mockito.any());
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        verify(telemetryEventClient).triggerEvent(getDummyGenericMetricForClusterRegistration(clusterEntity));
    }

    @Test
    void nvcaClusterRegistration_updatesNvcaVersion_whenPresentInRequest() {
        // Prepare
        String expectedNvcaVersion = "2.0.0";
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        existingClusterEntity.setNvcaVersion("1.0.0"); // Set initial version

        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                         false, false);
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setNvcaVersion(expectedNvcaVersion);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);

        var creationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .nvcaVersion(expectedNvcaVersion)
                .build();

        // Act
        var response = nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());
        assertEquals(expectedNvcaVersion, updatedClusterEntity.getNvcaVersion());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(nvcaConfigurationProperties).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @ParameterizedTest
    @EnumSource(value = ClusterStatusEnum.class, names = {"CORDON", "CORDON_AND_DRAIN"})
    void nvcaClusterRegistration_acceptsCordonStatuses_returnsSuccess(ClusterStatusEnum status) {
        // Prepare
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                         false, false);
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());
        updatedClusterEntity.setClusterStatus(status);

        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);

        var creationQueueUrl =
                String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties)
                .getTerminationQueueNameFormat();
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var request = NvcaRegistrationRequest.builder()
                .status(status)
                .k8sVersion("1.29.0")
                .build();
        // Act
        var response =
                nvcaClusterRegistrationService.nvcaClusterRegistration(request, DUMMY_CLUSTER_ID,
                                                                       new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        assertEquals(getDummyNvcaAccessCreds(updatedClusterEntity), response.getCredentials());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(nvcaClusterRepository).updateClusterRegistration(updatedClusterEntity);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(updatedClusterEntity);
        verify(nvcaConfigurationProperties).getCreationQueueNameFormat();
        verify(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        verify(telemetryEventClient).triggerEvent(
                getDummyGenericMetricForClusterRegistration(updatedClusterEntity));
    }

    @ParameterizedTest
    @EnumSource(value = ClusterStatusEnum.class, names = {"CORDON", "CORDON_AND_DRAIN"})
    void renewAccessCredentials_acceptsCordonStatuses_returnsCreds(ClusterStatusEnum status) {
        // Prepare
        var clusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                  false, false);
        clusterEntity.setClusterStatus(status);

        doReturn(Optional.of(clusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);

        var expectedCreds = getDummyNvcaAccessCreds(clusterEntity);
        doReturn(expectedCreds).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(clusterEntity);

        // Act
        var creds = nvcaClusterRegistrationService.renewAccessCredentials(DUMMY_CLUSTER_ID);

        // Assert
        assertNotNull(creds);
        assertEquals(expectedCreds, creds);

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(clusterQueueAccessCredsService).generateCredsForNvcaQueues(clusterEntity);
        verifyNoInteractions(nvcaClusterRepository);
        verifyNoInteractions(nvcaConfigurationProperties);
        verifyNoInteractions(queueManager);
        verifyNoInteractions(auditService);
        verifyNoInteractions(telemetryEventClient);
    }

    private ClusterEntity getDummyClusterEntityTargetingEnabled() {
        var entity = ClusterEntity.builder()
                .clusterName(DUMMY_BYOC_CLUSTER_NAME)
                .clusterId(DUMMY_CLUSTER_ID)
                .ncaId("ncaId")
                .clusterDescription("cluster_description")
                .clusterProvider(ClusterProviderEnum.GDN)
                .clusterStatus(ClusterStatusEnum.READY)
                .clusterGroupName("group_name")
                .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                .k8sVersion("1.29.0")
                .registrationTime(dummyInstant)
                .authorizedNcaIds(Set.of("ncaId1", "ncaId2"))
                .requestDump("request")
                .gpusV4(toGpusV4(Set.of(getDummyA100GpuV5())))
                .gpusV5(Set.of(getDummyA100GpuV5()))
                .region(DUMMY_REGION)
                .customAttributes(DUMMY_CUSTOM_ATTRIBUTES)
                .attributes(DUMMY_ATTRIBUTES)
                .allowClusterTargeting(true)
                .build();

        Map<String, CreationQueueUdt> creationQueueMap = new HashMap<>();
        for (GpuV4Udt gpu : entity.getGpusV4()) {
            creationQueueMap.put(gpu.getName(),
                                 getCreationQueue(gpu.getName(), DUMMY_CLUSTER_GROUP_ID));
        }
        entity.setCreationQueues(creationQueueMap);

        Map<String, CreationQueueUdt> clusterCeationQueueMap = new HashMap<>();
        for (GpuV4Udt gpu : entity.getGpusV4()) {
            clusterCeationQueueMap.put(gpu.getName(),
                                 getCreationQueue(gpu.getName(), DUMMY_CLUSTER_ID));
        }
        entity.setClusterCreationQueues(clusterCeationQueueMap);

        return entity;
    }

    private ClusterEntity getDummyClusterEntity(String clusterId,
                                                Set<GpuV5Udt> gpuV5Set,
                                                boolean queuesGenerated,
                                                boolean isTargetingEnabled,
                                                boolean isTaskCreationQueueEnabled) {
        var entity = ClusterEntity.builder()
                .clusterName(DUMMY_BYOC_CLUSTER_NAME)
                .clusterId(clusterId)
                .ncaId("ncaId")
                .clusterDescription("cluster_description")
                .clusterProvider(ClusterProviderEnum.GDN)
                .clusterStatus(ClusterStatusEnum.READY)
                .clusterGroupName("group_name")
                .clusterGroupId(DUMMY_CLUSTER_GROUP_ID)
                .k8sVersion("1.29.0")
                .registrationTime(dummyInstant)
                .authorizedNcaIds(Set.of("ncaId1", "ncaId2"))
                .requestDump("request")
                .build();

        if (gpuV5Set != null && !gpuV5Set.isEmpty()) {
            entity.setGpusV5(gpuV5Set);
            entity.setGpusV4(toGpusV4(gpuV5Set));

            Map<String, CreationQueueUdt> creationQueueMap = new HashMap<>();
            Map<String, CreationQueueUdt> clusterCreationQueueMap = new HashMap<>();
            Map<String, CreationQueueUdt> taskClusterCreationQueueMap = new HashMap<>();
            for (GpuV5Udt gpu : gpuV5Set) {
                if (queuesGenerated) {
                    creationQueueMap.put(gpu.getName(),
                                         getCreationQueue(gpu.getName(), DUMMY_CLUSTER_GROUP_ID));
                    if (isTargetingEnabled) {
                        clusterCreationQueueMap.put(gpu.getName(),
                                                    getCreationQueue(gpu.getName(), clusterId));
                        if (isTaskCreationQueueEnabled) {
                            String truncatedClusterId = clusterId;
                            if (truncatedClusterId.startsWith("nvssa")) {
                                truncatedClusterId = clusterId.substring(clusterId.length() - 36);
                            }
                            taskClusterCreationQueueMap.put(gpu.getName(),
                                                            getCreationQueueForTasks(gpu.getName(),
                                                                                     truncatedClusterId));
                        }
                    }
                }
            }
            entity.setCreationQueues(creationQueueMap);
            if (isTargetingEnabled) {
                entity.setAllowClusterTargeting(Boolean.TRUE);
                entity.setClusterCreationQueues(clusterCreationQueueMap);
                if (isTaskCreationQueueEnabled) {
                    entity.setAllowTaskClusterCreationQueues(Boolean.TRUE);
                    entity.setClusterCreationQueuesForTasks(taskClusterCreationQueueMap);
                }
            }

            if (queuesGenerated) {
                String trimmedClusterId = clusterId;
                if (clusterId.startsWith("nvssa")) {
                    trimmedClusterId = clusterId.substring(10);
                }
                entity.setTerminationQueueUrl(
                        String.format("q_gdn_spot_byoc_%s.fifo", trimmedClusterId));
                entity.setTerminationQueueType(QueueAttributeName.FifoQueue.toString());
            }
        }
        return entity;
    }

    private GpuV5Udt getDummyA100GpuV5() {
        return getDummyA100GpuV5(DUMMY_BYOC_INSTANCE_TYPE);
    }

    private GpuV5Udt getDummyA100GpuV5(String instanceTypeName) {
        var instanceTypeV5Udt = InstanceTypeV5Udt.builder()
                .gpuCount(1)
                .name(instanceTypeName)
                .value(DUMMY_BYOC_INSTANCE_TYPE_VALUE)
                .description("1 GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .nodeType(NodeTypeEnum.SINGLE.toString())
                .build();

        return GpuV5Udt.builder()
                .name("A100")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeV5Udt))
                .build();
    }

    private GpuV5Udt getDummyA200GpuV5() {
        var instanceTypeV5Udt = InstanceTypeV5Udt.builder()
                .gpuCount(1)
                .name("Standard_ND96amsr_A200_v4_1x")
                .value("Standard_ND96amsr_A200_v4")
                .description("1 GPU")
                .cpuCores(4)
                .gpuMemory("20Gi")
                .systemMemory("10Gi")
                .isDefault(true)
                .nodeType(NodeTypeEnum.SINGLE.toString())
                .build();

        return GpuV5Udt.builder()
                .name("A200")
                .capacity(8)
                .instanceTypes(Set.of(instanceTypeV5Udt))
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

        NvcaAccessCreds.NvcaAccessCredsBuilder credsBuilder = NvcaAccessCreds.builder()
                .terminationQueue(getDummyAccessInfo("dummy_termination_queue_url"))
                .creationQueue(creationQueueMap);

        if (clusterEntity.getAllowClusterTargeting() != null
                && Boolean.TRUE.equals(clusterEntity.getAllowClusterTargeting())) {
            Map<String, AwsQueueAccessInfo> clusterCreationQueueMap = new HashMap<>();
            if (clusterEntity.getClusterCreationQueues() != null &&
                    !clusterEntity.getClusterCreationQueues().isEmpty()) {
                for (Map.Entry<String, CreationQueueUdt> entry : clusterEntity.getClusterCreationQueues()
                        .entrySet()) {
                    clusterCreationQueueMap.put(entry.getKey(), getDummyAccessInfo(entry.getValue().getUrl()));
                }
            }
            credsBuilder.clusterCreationQueue(clusterCreationQueueMap);
        }

        return credsBuilder.build();
    }

    private AwsQueueAccessInfo getDummyAccessInfo(String url) {
        return AwsQueueAccessInfo.builder()
                .queueType(QueueAttributeName.FifoQueue.toString())
                .url(url)
                .accessKeyId("dummy_access_key_id")
                .secretAccessKey("dummy_access_secret_key")
                .sessionToken("dummy_session_token")
                .expiresAt(dummyInstant)
                .build();
    }

    private List<GenericMetric> getDummyGenericMetricForClusterRegistration(
            ClusterEntity clusterEntity) {
        Map<String, Object> metaData = getMetadataForCluster(clusterEntity,
                                                             "nvcaClusterRegistered");
        List<GenericMetric> l =  List.of(new GenericMetric()
                               .withMetadata(metaData)
                               .withClusterId(clusterEntity.getClusterId())
                               .withClusterName(clusterEntity.getClusterName())
                               .withEventName(Events.NVCA_CLUSTER_UPDATE.toString()));

        return l;
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

    // --- OIDC issuer registration tests ---

    @Test
    void nvcaClusterRegistration_withOidcIssuerOnly_doesNotWriteOidcRow() {
        // An oidcIssuer without a JWKS carries no identity material that
        // introspect or audience-based auth can act on — the OIDC row is only
        // persisted when a JWKS is present. Register must still succeed.
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        var updatedClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), true,
                                                         false, false);
        updatedClusterEntity.setNvcaLastConnected(dummyInstant);
        updatedClusterEntity.setAllowClusterTargeting(Boolean.FALSE);
        updatedClusterEntity.setClusterCreationQueuesForTasks(new HashMap<>());
        updatedClusterEntity.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        updatedClusterEntity.setClusterSource(ClusterSource.NGC_MANAGED.toString());

        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        doNothing().when(nvcaClusterRepository)
                .updateClusterRegistration(updatedClusterEntity);
        doReturn(getDummyNvcaAccessCreds(updatedClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(updatedClusterEntity);

        // Queue mocks needed for the code path before OIDC check
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient)
                .triggerEvent(getDummyGenericMetricForClusterRegistration(updatedClusterEntity));

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .oidcIssuer("https://k8s.example.com/oidc")
                .build();

        // Act — should succeed
        var response = nvcaClusterRegistrationService.nvcaClusterRegistration(
                request, DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        Mockito.verifyNoInteractions(clusterOidcIdentityService);
    }

    // --- JWKS size limit tests ---

    /** Enable the oidcClusterIdentityEnabled feature flag on the mocked config so JWKS
     *  handling kicks in. Flag-off behavior (short-circuit without touching
     *  cluster OIDC state) is covered separately. */
    private void enableOidcClusterIdentityFlag() {
        doReturn(true).when(nvcaConfigurationProperties).isOidcClusterIdentityEnabled();
    }

    @Test
    void nvcaClusterRegistration_jwksTooLarge_throwsBadRequest() {
        // Prepare
        enableOidcClusterIdentityFlag();
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();

        // Queue mocks
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .jwks("x".repeat(65_537))
                .build();

        // Act & Assert
        IcmsBadRequestException ex = assertThrows(IcmsBadRequestException.class, () ->
                nvcaClusterRegistrationService.nvcaClusterRegistration(
                        request, DUMMY_CLUSTER_ID, new HashMap<>()));
        assertTrue(ex.getMessage().contains("exceeds maximum"));
    }

    @Test
    void nvcaClusterRegistration_jwksUnderLimit_getsFormatError() {
        // Prepare: JWKS under limit but invalid JSON => should get format error, not size error
        enableOidcClusterIdentityFlag();
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();

        // Queue mocks
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .jwks("not-valid-json")
                .build();

        // Act & Assert: should fail with format error, not size error
        IcmsBadRequestException ex = assertThrows(IcmsBadRequestException.class, () ->
                nvcaClusterRegistrationService.nvcaClusterRegistration(
                        request, DUMMY_CLUSTER_ID, new HashMap<>()));
        assertTrue(ex.getMessage().contains("Invalid JWKS format"));
    }

    // --- JWKS Fingerprint Uniqueness Tests ---

    private static final String VALID_JWKS = "{\"keys\":[{\"kty\":\"RSA\",\"n\":\"0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw\",\"e\":\"AQAB\",\"alg\":\"RS256\",\"kid\":\"test-key-1\"}]}";

    // Same key, different JSON formatting (extra whitespace)
    private static final String VALID_JWKS_REFORMATTED = "{  \"keys\" :  [ { \"kty\" : \"RSA\" , \"n\" : \"0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw\" , \"e\" : \"AQAB\" , \"alg\" : \"RS256\" , \"kid\" : \"test-key-1\" } ] }";

    // Different key entirely
    private static final String DIFFERENT_JWKS = "{\"keys\":[{\"kty\":\"EC\",\"crv\":\"P-256\",\"x\":\"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU\",\"y\":\"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0\",\"kid\":\"different-key\"}]}";

    @Test
    void nvcaClusterRegistration_uniqueJwks_appliesOidcFieldsToClusterRow() throws Exception {
        // Prepare: registration with unique JWKS should succeed and the OIDC
        // fields should be attached to the canonical cluster_by_cluster_id row.
        enableOidcClusterIdentityFlag();
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();
        // Queue mocks
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        doReturn(getDummyNvcaAccessCreds(existingClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(any());
        doNothing().when(nvcaClusterRepository).updateClusterRegistration(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .jwks(VALID_JWKS)
                .build();

        // Act
        var response = nvcaClusterRegistrationService.nvcaClusterRegistration(
                request, DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert
        assertNotNull(response);
        String expectedFingerprint = AuthUtils.computeJwksFingerprint(VALID_JWKS);
        Mockito.verify(clusterOidcIdentityService).validateFingerprintAvailable(
                Mockito.eq(expectedFingerprint), Mockito.eq(DUMMY_CLUSTER_ID));
        Mockito.verify(clusterOidcIdentityService).applyOidcIdentity(
                Mockito.eq(existingClusterEntity),
                Mockito.eq(VALID_JWKS),
                Mockito.isNull(),
                Mockito.eq(expectedFingerprint));
    }

    @Test
    void nvcaClusterRegistration_duplicateJwks_throwsConflict() throws Exception {
        // Prepare: another cluster already owns this fingerprint in the
        // existing cluster row with the same JWKS fingerprint.
        enableOidcClusterIdentityFlag();
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();

        String fingerprint = AuthUtils.computeJwksFingerprint(VALID_JWKS);
        String otherClusterId = "other-cluster-id";
        doThrow(new IcmsConflictException(String.format(
                "JWKS signing keys are already registered by cluster %s. Each cluster must have unique signing keys.",
                otherClusterId)))
                .when(clusterOidcIdentityService)
                .validateFingerprintAvailable(fingerprint, DUMMY_CLUSTER_ID);

        // Queue mocks
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .jwks(VALID_JWKS)
                .build();

        // Act & Assert: should throw conflict before the fan-out write
        IcmsConflictException ex = assertThrows(IcmsConflictException.class, () ->
                nvcaClusterRegistrationService.nvcaClusterRegistration(
                        request, DUMMY_CLUSTER_ID, new HashMap<>()));
        assertTrue(ex.getMessage().contains("JWKS signing keys are already registered by cluster"));
        assertTrue(ex.getMessage().contains(otherClusterId));
    }

    @Test
    void nvcaClusterRegistration_sameClusterUpdateJwks_succeeds() throws Exception {
        // Prepare: same cluster re-registering with same JWKS should succeed
        // (self-update — uniqueness validation permits this cluster to keep
        // its own fingerprint).
        enableOidcClusterIdentityFlag();
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();

        String fingerprint = AuthUtils.computeJwksFingerprint(VALID_JWKS);
        // Queue mocks
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        doReturn(getDummyNvcaAccessCreds(existingClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(any());
        doNothing().when(nvcaClusterRepository).updateClusterRegistration(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .jwks(VALID_JWKS)
                .build();

        // Act: should succeed (self-update, not a duplicate)
        var response = nvcaClusterRegistrationService.nvcaClusterRegistration(
                request, DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert
        assertNotNull(response);
        assertEquals(DUMMY_CLUSTER_ID, response.getClusterId());
        Mockito.verify(clusterOidcIdentityService).validateFingerprintAvailable(
                Mockito.eq(fingerprint), Mockito.eq(DUMMY_CLUSTER_ID));
        Mockito.verify(clusterOidcIdentityService).applyOidcIdentity(
                Mockito.eq(existingClusterEntity), Mockito.eq(VALID_JWKS),
                Mockito.isNull(), Mockito.eq(fingerprint));
    }

    @Test
    void nvcaClusterRegistration_flagOff_skipsOidcTablesEvenWithJwks() {
        // With the oidcClusterIdentityEnabled flag off, the service must not touch
        // cluster OIDC state even when the caller supplies a JWKS —
        // managed-NVCF parity.
        var existingClusterEntity = getDummyClusterEntity(DUMMY_CLUSTER_ID, Set.of(getDummyA100GpuV5()), false,
                                                          false, false);
        doReturn(Optional.of(existingClusterEntity)).when(clusterRepository)
                .getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        doReturn(dummyInstant).when(instanceServiceHelper).getCurrentTimestamp();

        // Queue mocks
        doReturn(CREATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getCreationQueueNameFormat();
        doReturn(TERMINATION_QUEUE_FORMAT).when(nvcaConfigurationProperties).getTerminationQueueNameFormat();
        var creationQueueUrl = String.format(CREATION_QUEUE_FORMAT, DUMMY_CLUSTER_GROUP_ID, A100GpuName);
        doReturn(creationQueueUrl).when(clusterQueueAccessCredsService)
                .createNvcaFunctionCreationQueue(creationQueueUrl, DUMMY_CLUSTER_ID, false);
        doReturn(TERMINATION_QUEUE_URL).when(clusterQueueAccessCredsService)
                .createNvcaTerminationQueue(TERMINATION_QUEUE_URL, DUMMY_CLUSTER_ID, false);
        doReturn(getDummyNvcaAccessCreds(existingClusterEntity)).when(clusterQueueAccessCredsService)
                .generateCredsForNvcaQueues(any());
        doNothing().when(nvcaClusterRepository).updateClusterRegistration(any());
        doNothing().when(auditService).sendAuditEventForClusterEntity(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());

        var request = NvcaRegistrationRequest.builder()
                .status(ClusterStatusEnum.READY)
                .k8sVersion("1.29.0")
                .jwks(VALID_JWKS)
                .build();

        nvcaClusterRegistrationService.nvcaClusterRegistration(
                request, DUMMY_CLUSTER_ID, new HashMap<>());

        Mockito.verifyNoInteractions(clusterOidcIdentityService);
    }

    @Test
    void computeJwksFingerprint_deterministicAcrossFormats() throws java.text.ParseException {
        // Same key in different JSON formatting should produce the same fingerprint
        String fingerprint1 = AuthUtils.computeJwksFingerprint(VALID_JWKS);
        String fingerprint2 = AuthUtils.computeJwksFingerprint(VALID_JWKS_REFORMATTED);

        assertNotNull(fingerprint1);
        assertNotNull(fingerprint2);
        assertEquals(fingerprint1, fingerprint2,
                "Same JWKS with different formatting should produce the same fingerprint");

        // Different key should produce a different fingerprint
        String fingerprint3 = AuthUtils.computeJwksFingerprint(DIFFERENT_JWKS);
        assertNotNull(fingerprint3);
        assertTrue(!fingerprint1.equals(fingerprint3),
                "Different JWKS should produce different fingerprints");
    }

}
