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

import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_GROUP_ID;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_GROUP_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_PROVIDER;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_REGISTRATION_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CREATION_QUEUE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TERMINATION_QUEUE;
import static com.nvidia.icms.service.telemetry.model.Events.BYOC_CLUSTER_REGISTERED;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_CREATION_QUEUE_URL;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_RUNNING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_STARTING_INSTANCE_ID;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.cfg.DateTimeFeature;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.byoc.AwsQueueAccessInfo;
import com.nvidia.icms.inbound.rest.model.byoc.BartAccessCreds;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationCredentialsResponse;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.TestUtil;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Assertions;
import com.nvidia.icms.service.workers.WorkerIdentifierService;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

import com.nvidia.icms.inbound.rest.model.byoc.Gpu;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceType;

@Slf4j
@ExtendWith(MockitoExtension.class)
public class ClusterRegistrationServiceTest {

    private static final String QUEUE_NAME_FORMAT = "q_gdn_spot_byoc_%s.fifo";

    private static final String K8S_MIN_SUPPORTED_VERSION = "v1.25.0";

    private static final String K8S_MAX_SUPPORTED_VERSION = "v1.29.2";

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    ClusterQueueAccessCredsService clusterQueueAccessCredsService;

    @Mock
    QueueManager queueManager;

    @Mock
    TelemetryEventClient telemetryEventClient;

    @Mock
    ByocConfigurationProperties byocConfigurationProperties;

    @Mock
    AppAuditService auditService;

    @Mock
    InstanceServiceHelper instanceServiceHelper;

    @Mock
    ByocServiceHelper byocServiceHelper;

    ObjectMapper objectMapper;

    @Mock

    private WorkerIdentifierService workerIdentifierService;


    private ClusterRegistrationService clusterRegistrationService;

    @BeforeEach
    void init() {
        objectMapper = JsonMapper.builder()
                .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                .disable(DeserializationFeature.FAIL_ON_NULL_FOR_PRIMITIVES)
                .disable(DateTimeFeature.WRITE_DATES_AS_TIMESTAMPS)
                .build();

        clusterRegistrationService =
                new ClusterRegistrationService(clusterRepository,
                        clusterQueueAccessCredsService,
                        queueManager,
                        objectMapper,
                        telemetryEventClient,
                        byocConfigurationProperties,
                        auditService,
                        instanceServiceHelper,
                        byocServiceHelper,
                        ComputePlatformTestFixtures.nonByocComputePlatformService(),
                        workerIdentifierService);
    }

    // Tests for adding new cluster
    @Test
    void registerNewCluster_success() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setK8sVersion("1.29.2-gke.1");
        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());
        when(clusterQueueAccessCredsService.generateCredsForQueues(Mockito.any())).thenReturn(
                getBartRegistrationCredentials());
        doNothing().when(clusterRepository).saveClusterInfo(Mockito.any());
        when(clusterQueueAccessCredsService.createQueue(Mockito.any(), Mockito.any())).thenReturn(
                        "creation_queue_url")
                .thenReturn("termination_queue_url");
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.anyList());
        when(byocConfigurationProperties.getQueueNameFormat()).thenReturn(QUEUE_NAME_FORMAT);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());

        // Act
        BartRegistrationResponse bartRegistrationResponse =
                clusterRegistrationService.registerCluster(bartRegistrationRequest,
                                                           DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert

        Assertions.assertNotNull(bartRegistrationResponse);
        Assertions.assertEquals(DUMMY_CLUSTER_ID, bartRegistrationResponse.getClusterId());
        Assertions.assertNotNull(bartRegistrationResponse.getClusterGroupId());
        Assertions.assertEquals("creation_queue_url",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getQueueType());
        Assertions.assertEquals("creation_accessKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("creation_secretKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("creation_sessionToken",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getCreationQueue()
                        .getExpiresAt());
        Assertions.assertEquals("termination_queue_url",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getQueueType());
        Assertions.assertEquals("termination_accessKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("termination_secretKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("termination_sessionToken",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getTerminationQueue()
                        .getExpiresAt());

        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
        verify(clusterQueueAccessCredsService).generateCredsForQueues(Mockito.any());
        verify(clusterRepository).saveClusterInfo(Mockito.any());
        verify(clusterQueueAccessCredsService, times(2)).createQueue(Mockito.any(), Mockito.any());
        verify(telemetryEventClient).triggerEvent(Mockito.anyList());
        verify(byocConfigurationProperties, times(2)).getQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
    }

    @Test
    void registerNewCluster_errorValidatingNcaId() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setNcaId("ncaId1");

        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());

        // Act and Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        Assertions.assertEquals(
                "Specified ncaId ncaId1 is duplicated in the set of authorized ncaIds.",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
    }

    @Test
    void registerNewCluster_errorValidatingGpus() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setGpus(Set.of(Gpu.builder().name("dummy").instanceTypes(Set.of(
                        InstanceType.builder().name("name").value("value").isDefault(false).build()))
                                                       .build()));

        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());

        // Act and Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        Assertions.assertEquals(
                "There should be exactly one default instance type for each gpu, however gpu dummy is having 0 default instance types",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
    }

    @Test
    void registerNewCluster_errorValidatingAuthorizedNcaIds() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setAuthorizedNcaIds(Set.of("*", "xyz"));

        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());

        // Act and Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        Assertions.assertEquals(
                "If specified authorized nca ids contains * then it should not have other entries.",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
    }

    @Test
    void registerNewCluster_errorValidatingEmptyAuthorizedNcaId() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setAuthorizedNcaIds(Set.of(""));

        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());

        // Act and Assert
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        Assertions.assertEquals(
                "Specified authorized nca ids should not contain empty string.",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
    }

    @Test
    void registerNewCluster_errorValidatingClusterStatus() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setStatus(ClusterStatusEnum.ABANDONED);
        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "Cannot register a cluster with ABANDONED status",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
    }

    @Test
    void registerNewCluster_errorCreatingCreateQueue() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());
        when(clusterQueueAccessCredsService.createQueue(Mockito.any(), Mockito.any())).thenThrow(
                new IcmsInternalServerException("dummy_error"));
        when(byocConfigurationProperties.getQueueNameFormat()).thenReturn(QUEUE_NAME_FORMAT);

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> clusterRegistrationService.registerCluster(
                                                                    bartRegistrationRequest,
                                                                    DUMMY_CLUSTER_ID,
                                                                    new HashMap<>()));

        // Assert
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
        verify(clusterQueueAccessCredsService).createQueue(Mockito.any(), Mockito.any());
        verify(byocConfigurationProperties).getQueueNameFormat();
        verifyNoInteractions(auditService);
    }

    @Test
    void registerNewCluster_errorCreatingTerminationQueue() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());
        when(clusterQueueAccessCredsService.createQueue(Mockito.any(), Mockito.any())).thenReturn(
                        "creation_queue_url")
                .thenThrow(new IcmsInternalServerException("dummy_error"));
        when(byocConfigurationProperties.getQueueNameFormat()).thenReturn(QUEUE_NAME_FORMAT);

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> clusterRegistrationService.registerCluster(
                                                                    bartRegistrationRequest,
                                                                    DUMMY_CLUSTER_ID,
                                                                    new HashMap<>()));

        // Assert
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
        verify(clusterQueueAccessCredsService, times(2)).createQueue(Mockito.any(), Mockito.any());
        verify(byocConfigurationProperties, times(2)).getQueueNameFormat();
        verifyNoInteractions(auditService);
    }

    @Test
    void registerNewCluster_errorCreatingQueueAccessCreds() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());
        when(clusterQueueAccessCredsService.generateCredsForQueues(Mockito.any())).thenThrow(
                new IcmsInternalServerException("dummy_error"));
        when(clusterQueueAccessCredsService.createQueue(Mockito.any(), Mockito.any())).thenReturn(
                        "creation_queue_url")
                .thenReturn("termination_queue_url");
        when(byocConfigurationProperties.getQueueNameFormat()).thenReturn(QUEUE_NAME_FORMAT);

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> clusterRegistrationService.registerCluster(
                                                                    bartRegistrationRequest,
                                                                    DUMMY_CLUSTER_ID,
                                                                    new HashMap<>()));

        // Assert
        Assertions.assertEquals("dummy_error", exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
        verify(clusterQueueAccessCredsService).generateCredsForQueues(Mockito.any());
        verify(clusterQueueAccessCredsService, times(2)).createQueue(Mockito.any(), Mockito.any());
        verify(byocConfigurationProperties, times(2)).getQueueNameFormat();
        verifyNoInteractions(auditService);
    }

    @Test
    void registerNewCluster_errorSavingClusterInfo() {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        when(clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                          bartRegistrationRequest.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(),
                bartRegistrationRequest.getClusterGroup())).thenReturn(Optional.empty());
        doThrow(new IcmsInternalServerException("dummy_error")).when(clusterRepository)
                .saveClusterInfo(Mockito.any());
        when(clusterQueueAccessCredsService.createQueue(Mockito.any(), Mockito.any())).thenReturn(
                        "creation_queue_url")
                .thenReturn("termination_queue_url");
        when(byocConfigurationProperties.getQueueNameFormat()).thenReturn(QUEUE_NAME_FORMAT);

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> clusterRegistrationService.registerCluster(
                                                                    bartRegistrationRequest,
                                                                    DUMMY_CLUSTER_ID,
                                                                    new HashMap<>()));

        // Assert
        Assertions.assertEquals("dummy_error", exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                bartRegistrationRequest.getNcaId(), bartRegistrationRequest.getClusterGroup());
        verify(clusterRepository).saveClusterInfo(Mockito.any());
        verify(clusterQueueAccessCredsService, times(2)).createQueue(Mockito.any(), Mockito.any());
        verify(byocConfigurationProperties, times(2)).getQueueNameFormat();
        verifyNoInteractions(auditService);
    }

    // Tests for re-registering cluster
    @Test
    void reRegisterCluster_success() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        var metrics = getMetricsForClusterRegistration(clusterEntity, "clusterReRegistered", null,
                                                       bartRegistrationRequest);
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));
        when(clusterQueueAccessCredsService.generateCredsForQueues(Mockito.any())).thenReturn(
                getBartRegistrationCredentials());
        doNothing().when(clusterRepository).updateClusterInfo(Mockito.any(), Mockito.anySet(),
                                                              eq(false));
        doNothing().when(telemetryEventClient).triggerEvent(metrics);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());

        // Act
        BartRegistrationResponse bartRegistrationResponse =
                clusterRegistrationService.registerCluster(getBartRegistrationRequest(),
                                                           DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert

        Assertions.assertNotNull(bartRegistrationResponse);
        Assertions.assertEquals(DUMMY_CLUSTER_ID, bartRegistrationResponse.getClusterId());
        Assertions.assertNotNull(bartRegistrationResponse.getClusterGroupId());
        Assertions.assertEquals("creation_queue_url",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getQueueType());
        Assertions.assertEquals("creation_accessKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("creation_secretKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("creation_sessionToken",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getCreationQueue()
                        .getExpiresAt());
        Assertions.assertEquals("termination_queue_url",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getQueueType());
        Assertions.assertEquals("termination_accessKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("termination_secretKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("termination_sessionToken",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getTerminationQueue()
                        .getExpiresAt());

        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verify(clusterQueueAccessCredsService).generateCredsForQueues(Mockito.any());
        verify(clusterRepository).updateClusterInfo(Mockito.any(), Mockito.anySet(),
                                                    eq(false));
        verify(telemetryEventClient).triggerEvent(metrics);
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
    }

    // Tests for re-registering cluster
    @Test
    void reRegisterCluster_withUpdatedNcaId_success()
            throws JacksonException {

        // Prepare
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        Set<String> updatedAuthorizedNcaId = Set.of("ncaId1", "ncaId3", "ncaId4");
        bartRegistrationRequest.setAuthorizedNcaIds(updatedAuthorizedNcaId);

        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));
        when(clusterQueueAccessCredsService.generateCredsForQueues(Mockito.any())).thenReturn(
                getBartRegistrationCredentials());
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        when(byocConfigurationProperties.isAuthorizedNcaIdUpdateEnabled()).thenReturn(true);

        doNothing().when(clusterRepository)
                .updateClusterInfo(Mockito.any(), eq(Set.of("ncaId1", "ncaId2")),
                                   eq(true));

        ClusterEntity clusterEntity1 = CopyUtil.deepCopy(clusterEntity);
        clusterEntity1.setAuthorizedNcaIds(updatedAuthorizedNcaId);
        clusterEntity1.setRequestDump(objectMapper.writeValueAsString(bartRegistrationRequest));
        var metrics = getMetricsForClusterRegistration(clusterEntity1, "clusterReRegistered", null,
                                                       bartRegistrationRequest);
        doNothing().when(telemetryEventClient).triggerEvent(metrics);

        // Act
        BartRegistrationResponse bartRegistrationResponse =
                clusterRegistrationService.registerCluster(bartRegistrationRequest,
                                                           DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert

        Assertions.assertNotNull(bartRegistrationResponse);
        Assertions.assertEquals(DUMMY_CLUSTER_ID, bartRegistrationResponse.getClusterId());
        Assertions.assertNotNull(bartRegistrationResponse.getClusterGroupId());
        Assertions.assertEquals("creation_queue_url",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getQueueType());
        Assertions.assertEquals("creation_accessKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("creation_secretKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("creation_sessionToken",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getCreationQueue()
                        .getExpiresAt());
        Assertions.assertEquals("termination_queue_url",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getQueueType());
        Assertions.assertEquals("termination_accessKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("termination_secretKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("termination_sessionToken",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getTerminationQueue()
                        .getExpiresAt());

        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verify(clusterQueueAccessCredsService).generateCredsForQueues(Mockito.any());
        verify(clusterRepository).updateClusterInfo(Mockito.any(), eq(Set.of("ncaId1", "ncaId2")),
                                                    eq(true));
        verify(telemetryEventClient).triggerEvent(metrics);
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
    }

    @Test
    void reRegisterCluster_withNonByocAsCluster_success() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        clusterEntity.setClusterName("STATIC-ZONE");
        clusterEntity.setClusterGroupName("OCI");
        clusterEntity.setNcaId("OCI");
        clusterEntity.setClusterProvider(ClusterProviderEnum.OCI);
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequestForNonByoc();
        var metrics = getMetricsForClusterRegistration(clusterEntity, "clusterReRegistered", null,
                                                       bartRegistrationRequest);
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));
        when(clusterQueueAccessCredsService.generateCredsForQueues(Mockito.any())).thenReturn(
                getBartRegistrationCredentials());
        doNothing().when(clusterRepository).updateClusterInfo(Mockito.any(), Mockito.anySet(),
                                                              eq(false));
        doNothing().when(telemetryEventClient).triggerEvent(metrics);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());

        // Act
        BartRegistrationResponse bartRegistrationResponse =
                clusterRegistrationService.registerCluster(bartRegistrationRequest,
                                                           DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert

        Assertions.assertNotNull(bartRegistrationResponse);
        Assertions.assertEquals(DUMMY_CLUSTER_ID, bartRegistrationResponse.getClusterId());
        Assertions.assertNotNull(bartRegistrationResponse.getClusterGroupId());
        Assertions.assertEquals("creation_queue_url",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getQueueType());
        Assertions.assertEquals("creation_accessKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("creation_secretKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("creation_sessionToken",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getCreationQueue()
                        .getExpiresAt());
        Assertions.assertEquals("termination_queue_url",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getQueueType());
        Assertions.assertEquals("termination_accessKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("termination_secretKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("termination_sessionToken",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getTerminationQueue()
                        .getExpiresAt());

        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verify(clusterQueueAccessCredsService).generateCredsForQueues(Mockito.any());
        verify(clusterRepository).updateClusterInfo(Mockito.any(), Mockito.anySet(),
                                                    eq(false));
        verify(telemetryEventClient).triggerEvent(metrics);
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
    }

    @Test
    void reRegisterCluster_withExistingStatusAbandoned_success() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        clusterEntity.setClusterStatus(ClusterStatusEnum.ABANDONED);
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        var metrics = getMetricsForClusterRegistration(clusterEntity, "clusterReRegistered",
                                                       ClusterStatusEnum.READY,
                                                       bartRegistrationRequest);
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));
        when(clusterQueueAccessCredsService.generateCredsForQueues(Mockito.any())).thenReturn(
                getBartRegistrationCredentials());
        doNothing().when(clusterRepository).updateClusterInfo(Mockito.any(), Mockito.anySet(),
                                                              eq(false));
        doNothing().when(telemetryEventClient).triggerEvent(metrics);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());
        when(clusterQueueAccessCredsService.createQueue(Mockito.any(), Mockito.any())).thenReturn(
                        "creation_queue_url")
                .thenReturn("termination_queue_url");
        when(byocConfigurationProperties.getQueueNameFormat()).thenReturn(QUEUE_NAME_FORMAT);

        // Act
        BartRegistrationResponse bartRegistrationResponse =
                clusterRegistrationService.registerCluster(bartRegistrationRequest,
                                                           DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert

        Assertions.assertNotNull(bartRegistrationResponse);
        Assertions.assertEquals(DUMMY_CLUSTER_ID, bartRegistrationResponse.getClusterId());
        Assertions.assertNotNull(bartRegistrationResponse.getClusterGroupId());
        Assertions.assertEquals("creation_queue_url",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getQueueType());
        Assertions.assertEquals("creation_accessKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("creation_secretKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("creation_sessionToken",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getCreationQueue()
                        .getExpiresAt());
        Assertions.assertEquals("termination_queue_url",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getQueueType());
        Assertions.assertEquals("termination_accessKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("termination_secretKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("termination_sessionToken",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getTerminationQueue()
                        .getExpiresAt());

        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verify(clusterQueueAccessCredsService).generateCredsForQueues(Mockito.any());
        verify(clusterRepository).updateClusterInfo(Mockito.any(), Mockito.anySet(),
                                                    eq(false));
        verify(telemetryEventClient).triggerEvent(metrics);
        verify(clusterQueueAccessCredsService, times(2)).createQueue(Mockito.any(), Mockito.any());
        verify(byocConfigurationProperties, times(2)).getQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
    }

    @Test
    void reRegisterCluster_clusterIdConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              "dummy-id", new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "There is already a cluster registered with byoc-cluster-name-1 clusterName "
                        + "and ncaId ncaId with another sub",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verifyNoInteractions(auditService);
    }

    @Test
    void reRegisterCluster_groupNameConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setClusterGroup("dummy_group");

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "There exists an entry for the cluster with different group name. Specified group name dummy_group, existing group name cluster-group-1",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verifyNoInteractions(auditService);
    }

    @Test
    void reRegisterCluster_ncaIdDoesNotBelongToAuthorizedListConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        clusterEntity.setAuthorizedNcaIds(Set.of("abc", clusterEntity.getNcaId()));
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "Specified ncaId ncaId is duplicated in the set of authorized ncaIds.",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verifyNoInteractions(auditService);
    }

    @Test
    void reRegisterCluster_clusterProviderConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setClusterProvider(ClusterProviderEnum.ONPREM);

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "There exists an entry for the cluster with different provider. Specified cluster provider ON-PREM, existing cluster provider GDN",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verifyNoInteractions(auditService);
    }

    @Test
    void reRegisterCluster_authorizedNcaIdsConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        clusterEntity.setAuthorizedNcaIds(Set.of("ncaId1", "ncaId2"));
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setAuthorizedNcaIds(Set.of("ncaId1"));

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "Specified authorized nca ids are not matching with the authorized nca ids of the cluster group",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verifyNoInteractions(auditService);
    }

    @Test
    void reRegisterCluster_gpusConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.of(clusterEntity));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setGpus(TestUtil.buildGpus());

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "Specified gpus size are not matching with the size of gpus of the cluster group",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verifyNoInteractions(auditService);
    }

    // Tests for adding new clusters to a group
    @Test
    void addClusterToGroup_success() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        var metrics = getMetricsForClusterRegistration(clusterEntity, "clusterRegistered", null,
                                                       bartRegistrationRequest);
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), clusterEntity.getClusterGroupName())).thenReturn(
                Optional.of(toClusterGroupsByAccountEntity(clusterEntity)));
        when(clusterQueueAccessCredsService.generateCredsForQueues(Mockito.any())).thenReturn(
                getBartRegistrationCredentials());
        doNothing().when(clusterRepository).saveClusterInfo(Mockito.any());
        when(clusterQueueAccessCredsService.createQueue(Mockito.any(), Mockito.any())).thenReturn(
                "termination_queue_url");
        doNothing().when(telemetryEventClient).triggerEvent(metrics);
        when(byocConfigurationProperties.getQueueNameFormat()).thenReturn(QUEUE_NAME_FORMAT);
        doNothing().when(auditService)
                .sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(), Mockito.any());

        // Act
        BartRegistrationResponse bartRegistrationResponse =
                clusterRegistrationService.registerCluster(bartRegistrationRequest,
                                                           DUMMY_CLUSTER_ID, new HashMap<>());

        // Assert

        Assertions.assertNotNull(bartRegistrationResponse);
        Assertions.assertEquals(DUMMY_CLUSTER_ID, bartRegistrationResponse.getClusterId());
        Assertions.assertNotNull(bartRegistrationResponse.getClusterGroupId());
        Assertions.assertEquals("creation_queue_url",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getQueueType());
        Assertions.assertEquals("creation_accessKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("creation_secretKey",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("creation_sessionToken",
                                bartRegistrationResponse.getCredentials().getCreationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getCreationQueue()
                        .getExpiresAt());
        Assertions.assertEquals("termination_queue_url",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getQueueType());
        Assertions.assertEquals("termination_accessKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("termination_secretKey",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("termination_sessionToken",
                                bartRegistrationResponse.getCredentials().getTerminationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationResponse.getCredentials().getTerminationQueue()
                        .getExpiresAt());

        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(),
                clusterEntity.getClusterGroupName());
        verify(clusterQueueAccessCredsService).generateCredsForQueues(Mockito.any());
        verify(clusterRepository).saveClusterInfo(Mockito.any());
        verify(clusterQueueAccessCredsService).createQueue(Mockito.any(), Mockito.any());
        verify(telemetryEventClient).triggerEvent(metrics);
        verify(byocConfigurationProperties).getQueueNameFormat();
        verify(auditService).sendAuditEventForClusterEntity(Mockito.any(), Mockito.any(),
                                                            Mockito.any());
    }

    @Test
    void addClusterToGroup_ncaIdBelongsToAuthorizedListConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), clusterEntity.getClusterGroupName())).thenReturn(
                Optional.of(toClusterGroupsByAccountEntity(clusterEntity)));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.getAuthorizedNcaIds().add(bartRegistrationRequest.getNcaId());

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "Specified ncaId ncaId is duplicated in the set of authorized ncaIds.",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(),
                clusterEntity.getClusterGroupName());
        verifyNoInteractions(auditService);
    }

    @Test
    void addClusterToGroup_authorizedNcaIdsConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), clusterEntity.getClusterGroupName())).thenReturn(
                Optional.of(toClusterGroupsByAccountEntity(clusterEntity)));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setAuthorizedNcaIds(Set.of("dummy"));

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "Specified authorized nca ids are not matching with the authorized nca ids of the cluster group",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(),
                clusterEntity.getClusterGroupName());
        verifyNoInteractions(auditService);
    }

    @Test
    void addClusterToGroup_gpusConflictError() {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                          clusterEntity.getClusterName())).thenReturn(
                Optional.empty());
        when(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(), clusterEntity.getClusterGroupName())).thenReturn(
                Optional.of(toClusterGroupsByAccountEntity(clusterEntity)));

        BartRegistrationRequest bartRegistrationRequest = getBartRegistrationRequest();
        bartRegistrationRequest.setGpus(TestUtil.buildGpus());

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterRegistrationService.registerCluster(
                                                              bartRegistrationRequest,
                                                              DUMMY_CLUSTER_ID, new HashMap<>()));

        // Assert
        Assertions.assertEquals(
                "Specified gpus size are not matching with the size of gpus of the cluster group",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterByAccountAndName(clusterEntity.getNcaId(),
                                                             clusterEntity.getClusterName());
        verify(clusterRepository).getClusterGroupInfoByAccountAndNameInMainAccount(
                clusterEntity.getNcaId(),
                clusterEntity.getClusterGroupName());
        verifyNoInteractions(auditService);
    }

    @Test
    void deleteCluster_withValidClusterId_returnsSuccess() {
        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getClusterEntity()));
        when(instanceServiceHelper.getActiveInstancesFromZone(DUMMY_CLUSTER_ID)).thenReturn(List.of());

        doNothing().when(clusterRepository).deleteClusterInfo(getClusterEntity());
        doNothing().when(queueManager).deleteQueue(getClusterEntity().getTerminationQueueUrl());
        when(clusterRepository.getClusterGroupInfoByClusterGroupId(
                clusterEntity.getClusterGroupId())).thenReturn(Optional.empty());
        doNothing().when(queueManager).deleteQueue(clusterEntity.getCreationQueueUrl());

        // Act
        clusterRegistrationService.deleteCluster(DUMMY_CLUSTER_ID, new HashMap<>());

        // Verify
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(instanceServiceHelper).getActiveInstancesFromZone(DUMMY_CLUSTER_ID);
        verify(queueManager).deleteQueue(clusterEntity.getCreationQueueUrl());
        verify(queueManager).deleteQueue(clusterEntity.getTerminationQueueUrl());
        verify(queueManager).deleteQueue(DUMMY_CLUSTER_CREATION_QUEUE_URL);
        verify(clusterRepository).getClusterGroupInfoByClusterGroupId(
                clusterEntity.getClusterGroupId());
    }

    @Test
    void deleteCluster_withDbDeletionFailed_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getClusterEntity()));
        when(instanceServiceHelper.getActiveInstancesFromZone(DUMMY_CLUSTER_ID)).thenReturn(List.of());

        doNothing().when(clusterRepository).deleteClusterInfo(getClusterEntity());
        doThrow(new RuntimeException("dummy_exception")).when(queueManager)
                .deleteQueue(getClusterEntity().getTerminationQueueUrl());

        // Act
        IcmsInternalServerException icmsInternalServerException =
                assertThrows(IcmsInternalServerException.class, () -> {
                    clusterRegistrationService.deleteCluster(DUMMY_CLUSTER_ID, new HashMap<>());
                });

        // Assert
        assertEquals("Failed to un-register cluster, error: dummy_exception",
                     icmsInternalServerException.getBody().getDetail());

        // Verify
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(instanceServiceHelper).getActiveInstancesFromZone(DUMMY_CLUSTER_ID);
    }

    @Test
    void deleteCluster_withQueueDeletionFailed_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getClusterEntity()));
        when(instanceServiceHelper.getActiveInstancesFromZone(DUMMY_CLUSTER_ID)).thenReturn(List.of());

        doThrow(new RuntimeException("dummy_error")).when(clusterRepository)
                .deleteClusterInfo(getClusterEntity());

        // Act
        IcmsInternalServerException icmsInternalServerException =
                assertThrows(IcmsInternalServerException.class, () -> {
                    clusterRegistrationService.deleteCluster(DUMMY_CLUSTER_ID, new HashMap<>());
                });

        // Assert
        assertEquals("Failed to un-register cluster, error: dummy_error",
                     icmsInternalServerException.getBody().getDetail());

        // Verify
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(instanceServiceHelper).getActiveInstancesFromZone(DUMMY_CLUSTER_ID);
    }

    @Test
    void deleteCluster_withActiveInstance_throwsException() {
        // Prepare
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.of(getClusterEntity()));

        when(instanceServiceHelper.getActiveInstancesFromZone(DUMMY_CLUSTER_ID)).thenReturn(
                List.of(DUMMY_STARTING_INSTANCE_ID, DUMMY_RUNNING_INSTANCE_ID));

        // Act
        IcmsConflictException icmsConflictException = assertThrows(IcmsConflictException.class, () -> {
            clusterRegistrationService.deleteCluster(DUMMY_CLUSTER_ID, new HashMap<>());
        });

        // Assert
        assertEquals(
                "cluster un-registration failed, terminate following active instances [dummy_starting_instance_Id, dummy_running_instance_id]",
                icmsConflictException.getBody().getDetail());

        // Verify
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(instanceServiceHelper).getActiveInstancesFromZone(DUMMY_CLUSTER_ID);
    }

    @Test
    void deleteCluster_clusterInfoNotPresent_throwsException() {
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false)).thenReturn(
                Optional.empty());

        IcmsNotFoundException icmsNotFoundException = assertThrows(IcmsNotFoundException.class, () -> {
            clusterRegistrationService.deleteCluster(DUMMY_CLUSTER_ID, new HashMap<>());
        });

        assertEquals("Could not find any cluster registered with id cluster_id",
                     icmsNotFoundException.getBody().getDetail());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
    }


    private BartRegistrationRequest getBartRegistrationRequest() {
        try {
            return this.objectMapper.readValue(getBartRegistrationRequestString(),
                                               BartRegistrationRequest.class);
        } catch (Exception e) {
            log.error("Failed to generate registration request");
        }
        return new BartRegistrationRequest();
    }

    private BartRegistrationRequest getBartRegistrationRequestForNonByoc() {
        try {
            return this.objectMapper.readValue(getBartRegistrationRequestStringForNonByocCluster(),
                                               BartRegistrationRequest.class);
        } catch (Exception e) {
            log.error("Failed to generate registration request");
        }
        return new BartRegistrationRequest();
    }

    private String getBartRegistrationRequestStringForNonByocCluster() {
        return "{\n" +
                "    \"ncaId\": \"OCI\",\n" +
                "    \"k8sVersion\": \"v1.27.0\",\n" +
                "    \"clusterName\": \"STATIC-ZONE\",\n" +
                "    \"clusterDescription\": \"Test cluster\",\n" +
                "    \"clusterGroup\": \"OCI\",\n" +
                "    \"authorizedNcaIds\": [\n" +
                "        \"ncaId1\",\n" +
                "        \"ncaId2\"\n" +
                "    ],\n" +
                "    \"clusterProvider\": \"OCI\",\n" +
                "    \"status\": \"READY\",\n" +
                "    \"gpus\": [\n" +
                "        {\n" +
                "            \"name\": \"DUMMY_GPU_4\",\n" +
                "            \"instanceTypes\": [\n" +
                "                {\n" +
                "                    \"name\": \"dummy_gpu_4.large\",\n" +
                "                    \"value\": \"dummy_gpu_4.large\",\n" +
                "                    \"description\": \"One Nvidia turing GPU\",\n" +
                "                    \"default\": true,\n" +
                "                    \"cpuCores\": 4,\n" +
                "                    \"systemMemory\": \"16G\",\n" +
                "                    \"gpuMemory\": \"14G\"\n" +
                "                }\n" +
                "            ]\n" +
                "        },\n" +
                "        {\n" +
                "            \"name\": \"DUMMY_GPU_1\",\n" +
                "            \"instanceTypes\": [\n" +
                "                {\n" +
                "                    \"name\": \"dummy_gpu_1.large\",\n" +
                "                    \"value\": \"dummy_gpu_1.large\",\n" +
                "                    \"description\": \"One Nvidia Ada GPU\",\n" +
                "                    \"default\": true,\n" +
                "                    \"cpuCores\": 8,\n" +
                "                    \"systemMemory\": \"24G\",\n" +
                "                    \"gpuMemory\": \"28G\"\n" +
                "                }\n" +
                "            ]\n" +
                "        }\n" +
                "    ]\n" +
                "}";
    }

    private String getBartRegistrationRequestString() {
        return "{\n" +
                "    \"ncaId\": \"ncaId\",\n" +
                "    \"k8sVersion\": \"v1.27.0\",\n" +
                "    \"clusterName\": \"byoc-cluster-name-1\",\n" +
                "    \"clusterDescription\": \"Test cluster\",\n" +
                "    \"clusterGroup\": \"cluster-group-1\",\n" +
                "    \"authorizedNcaIds\": [\n" +
                "        \"ncaId1\",\n" +
                "        \"ncaId2\"\n" +
                "    ],\n" +
                "    \"clusterProvider\": \"GDN\",\n" +
                "    \"status\": \"READY\",\n" +
                "    \"gpus\": [\n" +
                "        {\n" +
                "            \"name\": \"DUMMY_GPU_4\",\n" +
                "            \"instanceTypes\": [\n" +
                "                {\n" +
                "                    \"name\": \"dummy_gpu_4.large\",\n" +
                "                    \"value\": \"dummy_gpu_4.large\",\n" +
                "                    \"description\": \"One Nvidia turing GPU\",\n" +
                "                    \"default\": true,\n" +
                "                    \"cpuCores\": 4,\n" +
                "                    \"systemMemory\": \"16G\",\n" +
                "                    \"gpuMemory\": \"14G\"\n" +
                "                }\n" +
                "            ]\n" +
                "        },\n" +
                "        {\n" +
                "            \"name\": \"DUMMY_GPU_1\",\n" +
                "            \"instanceTypes\": [\n" +
                "                {\n" +
                "                    \"name\": \"dummy_gpu_1.large\",\n" +
                "                    \"value\": \"dummy_gpu_1.large\",\n" +
                "                    \"description\": \"One Nvidia Ada GPU\",\n" +
                "                    \"default\": true,\n" +
                "                    \"cpuCores\": 8,\n" +
                "                    \"systemMemory\": \"24G\",\n" +
                "                    \"gpuMemory\": \"28G\"\n" +
                "                }\n" +
                "            ]\n" +
                "        }\n" +
                "    ]\n" +
                "}";
    }

    private BartRegistrationCredentialsResponse getBartRegistrationCredentials() {
        return BartRegistrationCredentialsResponse.builder()
                .credentials(BartAccessCreds.builder().creationQueue(
                                getAccessInfoForCreate()).terminationQueue(getAccessInfoForTerminate())
                                     .build()
                ).build();
    }

    private AwsQueueAccessInfo getAccessInfoForCreate() {
        return AwsQueueAccessInfo.builder().url("creation_queue_url").queueType("FifoQueue")
                .accessKeyId("creation_accessKey")
                .secretAccessKey("creation_secretKey")
                .sessionToken("creation_sessionToken").expiresAt(
                        Instant.now().plusMillis(1000)).build();
    }

    private AwsQueueAccessInfo getAccessInfoForTerminate() {
        return AwsQueueAccessInfo.builder().url("termination_queue_url").queueType("FifoQueue")
                .accessKeyId("termination_accessKey")
                .secretAccessKey("termination_secretKey")
                .sessionToken("termination_sessionToken").expiresAt(
                        Instant.now().plusMillis(1000)).build();
    }

    private ClusterEntity getClusterEntity() {
        try {
            return ClusterEntity.builder()
                    .clusterName("byoc-cluster-name-1")
                    .clusterId(DUMMY_CLUSTER_ID)
                    .ncaId("ncaId")
                    .terminationQueueUrl("termination_queue_url")
                    .terminationQueueType("FifoQueue")
                    .clusterDescription("Test cluster")
                    .clusterProvider(ClusterProviderEnum.GDN)
                    .clusterStatus(ClusterStatusEnum.READY)
                    .k8sVersion("k8sVersion")
                    .clusterGroupName("cluster-group-1")
                    .clusterGroupId("group_id")
                    .creationQueueUrl("creation_queue_url")
                    .creationQueueType("FifoQueue")
                    .gpus(buildGpu())
                    .authorizedNcaIds(Set.of("ncaId1", "ncaId2"))
                    .requestDump(objectMapper.writeValueAsString(getBartRegistrationRequest()))
                    .clusterCreationQueues(
                            Map.of("H100",
                                   CreationQueueUdt.builder()
                                           .url(DUMMY_CLUSTER_CREATION_QUEUE_URL)
                                           .queueType("fifo")
                                           .build()))
                    .build();
        } catch (Exception exception) {
            log.error("Error while generating clusterEntity {}", exception.getMessage(), exception);
        }
        return null;
    }

    private ClusterGroupsByAccountEntity toClusterGroupsByAccountEntity(ClusterEntity entity) {
        return ClusterGroupsByAccountEntity.builder()
                .key(ClusterGroupsByAccountKey.builder()
                             .clusterGroupName(entity.getClusterGroupName())
                             .ncaId(entity.getNcaId())
                             .build())
                .clusterGroupId(entity.getClusterGroupId())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .gpus(entity.getGpus())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .build();
    }

    private ClusterGroupByGroupIdEntity toClusterGroupByGroupIdEntity(ClusterEntity entity) {
        return ClusterGroupByGroupIdEntity.builder()
                .clusterGroupName(entity.getClusterGroupName())
                .clusterGroupId(entity.getClusterGroupId())
                .creationQueueUrl(entity.getCreationQueueUrl())
                .creationQueueType(entity.getCreationQueueType())
                .ncaId(entity.getNcaId())
                .gpus(entity.getGpus())
                .authorizedNcaIds(entity.getAuthorizedNcaIds())
                .build();
    }

    private Set<GpuUdt> buildGpu() {
        GpuUdt gpu1 = GpuUdt.builder().name("DUMMY_GPU_4").instanceTypes(
                Set.of(InstanceTypeUdt.builder().name("dummy_gpu_4.large").gpuMemory("14G").value("dummy_gpu_4.large")
                               .systemMemory("16G").cpuCores(4).description("One Nvidia turing GPU")
                               .isDefault(true)
                               .build())).build();
        GpuUdt gpu2 = GpuUdt.builder().name("DUMMY_GPU_1").instanceTypes(
                Set.of(InstanceTypeUdt.builder().name("dummy_gpu_1.large").gpuMemory("28G")
                               .value("dummy_gpu_1.large")
                               .systemMemory("24G").cpuCores(8).description("One Nvidia Ada GPU")
                               .isDefault(true)
                               .build())).build();
        return Set.of(gpu1, gpu2);
    }

    private List<GenericMetric> getMetricsForClusterRegistration(
            ClusterEntity clusterEntity,
            String clusterRegistrationStatus,
            ClusterStatusEnum clusterStatusEnum,
            BartRegistrationRequest bartRegistrationRequest) {

        Map<String, Object> metaData = new HashMap<>();
        metaData.put(CLUSTER_GROUP_ID.getName(), clusterEntity.getClusterGroupId());
        metaData.put(CLUSTER_GROUP_NAME.getName(), clusterEntity.getClusterGroupName());
        metaData.put(CLUSTER_PROVIDER.getName(), clusterEntity.getClusterProvider());
        metaData.put(CLUSTER_STATUS.getName(), clusterEntity.getClusterStatus());
        metaData.put(CLUSTER_REGISTRATION_STATUS.getName(), clusterRegistrationStatus);
        metaData.put(CREATION_QUEUE.getName(), clusterEntity.getCreationQueueUrl());
        metaData.put(TERMINATION_QUEUE.getName(), clusterEntity.getTerminationQueueUrl());
        metaData.put(TelemetryEventClient.EventMetaData.CLUSTER_REGISTERED_NCA_ID.getName(),
                     clusterEntity.getNcaId());
        metaData.put(TelemetryEventClient.EventMetaData.CLUSTER_AUTHORIZED_NCA_ID.getName(),
                     clusterEntity.getAuthorizedNcaIds());

        if (clusterStatusEnum != null) {
            metaData.put(CLUSTER_STATUS.getName(), clusterStatusEnum);
        } else {
            metaData.put(CLUSTER_STATUS.getName(), clusterEntity.getClusterStatus());
        }
        metaData.put(CLUSTER_REGISTRATION_STATUS.getName(), clusterRegistrationStatus);

        return List.of(new GenericMetric()
                               .withEventName(BYOC_CLUSTER_REGISTERED.toString())
                               .withClusterId(clusterEntity.getClusterId())
                               .withClusterName(clusterEntity.getClusterName())
                               .withMetadata(metaData));
    }
}
