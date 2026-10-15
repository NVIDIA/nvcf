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

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_GROUP_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CREATION_QUEUE_URL;
import static com.nvidia.icms.util.TestUtil.buildGpuUdts;
import static com.nvidia.icms.util.TestUtil.buildGpus;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.*;

import com.amazonaws.services.sqs.model.QueueAttributeName;
import com.nvidia.icms.configuration.aws.AwsQueueProperties;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationCredentialsResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.outbound.sts.CredentialsGenerationService;
import com.nvidia.icms.service.InstanceServiceHelper;
import java.time.Instant;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.ExecutionException;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import software.amazon.awssdk.services.sts.model.Credentials;

@ExtendWith(MockitoExtension.class)
public class ClusterQueueAccessCredsServiceTest {

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    CredentialsGenerationService credentialGenerationService;

    @Mock
    QueueManager queueManager;

    @Mock
    AwsQueueProperties awsQueueProperties;

    @Mock
    InstanceServiceHelper instanceServiceHelper;

    private ClusterQueueAccessCredsService clusterQueueAccessCredsService;

    @BeforeEach
    void init() {
        clusterQueueAccessCredsService =
                new ClusterQueueAccessCredsService(clusterRepository, credentialGenerationService,
                                                   queueManager, awsQueueProperties, instanceServiceHelper);
    }

    @Test
    void getClusterQueuesInfo_success()
            throws Exception {

        // Prepare
        Mockito.when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
                .thenReturn(Optional.of(getClusterEntity()));
        Mockito.when(credentialGenerationService.getCredentialsForQueue("creation_queue_url"))
                .thenReturn(getDummyCredentialsForCreate());
        Mockito.when(credentialGenerationService.getCredentialsForQueue("termination_queue_url"))
                .thenReturn(getDummyCredentialsForTerminate());
        when(queueManager.queueExists("creation_queue_url")).thenReturn(true);
        when(queueManager.queueExists("termination_queue_url")).thenReturn(true);

        // Act
        BartRegistrationCredentialsResponse bartRegistrationCredentialsResponse =
                clusterQueueAccessCredsService.getClusterQueuesInfo(DUMMY_CLUSTER_ID);

        // Assert
        Assertions.assertNotNull(bartRegistrationCredentialsResponse);
        Assertions.assertNotNull(bartRegistrationCredentialsResponse.getCredentials());
        Assertions.assertEquals("creation_queue_url",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getCreationQueue().getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getCreationQueue()
                                        .getQueueType());
        Assertions.assertEquals("creation_accessKey",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getCreationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("creation_secretKey",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getCreationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("creation_sessionToken",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getCreationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationCredentialsResponse.getCredentials().getCreationQueue()
                        .getExpiresAt());
        Assertions.assertEquals("termination_queue_url",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getTerminationQueue()
                                        .getUrl());
        Assertions.assertEquals("FifoQueue",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getTerminationQueue()
                                        .getQueueType());
        Assertions.assertEquals("termination_accessKey",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getTerminationQueue()
                                        .getAccessKeyId());
        Assertions.assertEquals("termination_secretKey",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getTerminationQueue()
                                        .getSecretAccessKey());
        Assertions.assertEquals("termination_sessionToken",
                                bartRegistrationCredentialsResponse.getCredentials()
                                        .getTerminationQueue()
                                        .getSessionToken());
        Assertions.assertNotNull(
                bartRegistrationCredentialsResponse.getCredentials().getTerminationQueue()
                        .getExpiresAt());

        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(credentialGenerationService).getCredentialsForQueue("creation_queue_url");
        verify(credentialGenerationService).getCredentialsForQueue("termination_queue_url");
        verify(queueManager).queueExists("creation_queue_url");
        verify(queueManager).queueExists("termination_queue_url");
    }

    @Test
    void getClusterQueuesInfo_clusterDoesNotExist_throwsError()
            throws Exception {

        // Prepare
        Mockito.when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
                .thenReturn(Optional.empty());

        // Act
        IcmsNotFoundException exception = assertThrows(IcmsNotFoundException.class,
                                                      () -> clusterQueueAccessCredsService.getClusterQueuesInfo(
                                                              DUMMY_CLUSTER_ID));

        // Assert
        Assertions.assertEquals("Could not find any cluster registered with id " + DUMMY_CLUSTER_ID,
                                exception.getBody().getDetail());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
    }

    @Test
    void getClusterQueuesInfo_exceptionGeneratingCreds_throwsError()
            throws Exception {

        // Prepare
        Mockito.when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
                .thenReturn(Optional.of(getClusterEntity()));
        Mockito.when(credentialGenerationService.getCredentialsForQueue("creation_queue_url"))
                .thenReturn(getDummyCredentialsForCreate());
        Mockito.when(credentialGenerationService.getCredentialsForQueue("termination_queue_url"))
                .thenThrow(new RuntimeException("dummy_error"));
        when(queueManager.queueExists("creation_queue_url")).thenReturn(true);
        when(queueManager.queueExists("termination_queue_url")).thenReturn(true);

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> clusterQueueAccessCredsService.getClusterQueuesInfo(
                                                                    DUMMY_CLUSTER_ID));

        // Assert
        Assertions.assertEquals(
                "Exception while generating creds for queue termination_queue_url, error dummy_error",
                exception.getBody().getDetail());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(credentialGenerationService).getCredentialsForQueue("creation_queue_url");
        verify(credentialGenerationService).getCredentialsForQueue("termination_queue_url");
        verify(queueManager).queueExists("creation_queue_url");
        verify(queueManager).queueExists("termination_queue_url");
    }

    @Test
    void getClusterQueuesInfo_forAbandonedClusters_throwsError()
            throws Exception {

        // Prepare
        ClusterEntity clusterEntity = getClusterEntity();
        clusterEntity.setClusterStatus(ClusterStatusEnum.ABANDONED);
        Mockito.when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
                .thenReturn(Optional.of(clusterEntity));

        // Act
        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                                                      () -> clusterQueueAccessCredsService.getClusterQueuesInfo(
                                                              DUMMY_CLUSTER_ID));

        // Assert
        Assertions.assertEquals("Cannot generate creds for a cluster with ABANDONED status",
                                exception.getBody().getDetail());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
    }

    @Test
    void getClusterQueuesInfo_nullOrEmptyCredsGenerated_throwsError()
            throws Exception {

        // Prepare
        Mockito.when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
                .thenReturn(Optional.of(getClusterEntity()));
        Mockito.when(credentialGenerationService.getCredentialsForQueue("creation_queue_url"))
                .thenReturn(getDummyCredentialsForCreate());
        Mockito.when(credentialGenerationService.getCredentialsForQueue("termination_queue_url"))
                .thenReturn(null);
        when(queueManager.queueExists("creation_queue_url")).thenReturn(true);
        when(queueManager.queueExists("termination_queue_url")).thenReturn(true);

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> clusterQueueAccessCredsService.getClusterQueuesInfo(
                                                                    DUMMY_CLUSTER_ID));

        // Assert
        Assertions.assertEquals("Null or Empty creds generated for termination_queue_url",
                                exception.getBody().getDetail());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(credentialGenerationService).getCredentialsForQueue("creation_queue_url");
        verify(credentialGenerationService).getCredentialsForQueue("termination_queue_url");
        verify(queueManager).queueExists("creation_queue_url");
        verify(queueManager).queueExists("termination_queue_url");
    }

    @Test
    void getClusterQueuesInfo_invalidCredsGenerated_throwsError()
            throws Exception {

        // Prepare
        Mockito.when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
                .thenReturn(Optional.of(getClusterEntity()));
        Mockito.when(credentialGenerationService.getCredentialsForQueue("creation_queue_url"))
                .thenReturn(getDummyCredentialsForCreate());
        Mockito.when(credentialGenerationService.getCredentialsForQueue("termination_queue_url"))
                .thenReturn(Credentials.builder().build());
        when(queueManager.queueExists("creation_queue_url")).thenReturn(true);
        when(queueManager.queueExists("termination_queue_url")).thenReturn(true);

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> clusterQueueAccessCredsService.getClusterQueuesInfo(
                                                                    DUMMY_CLUSTER_ID));

        // Assert
        Assertions.assertEquals("Invalid creds generated for termination_queue_url",
                                exception.getBody().getDetail());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(credentialGenerationService).getCredentialsForQueue("creation_queue_url");
        verify(credentialGenerationService).getCredentialsForQueue("termination_queue_url");
        verify(queueManager).queueExists("creation_queue_url");
        verify(queueManager).queueExists("termination_queue_url");
    }

    @Test
    void getClusterQueuesInfo_InterruptedException_throwsError()
            throws Exception {
        // Prepare
        Mockito.when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
                .thenReturn(Optional.of(getClusterEntity()));
        Mockito.when(credentialGenerationService.getCredentialsForQueue("creation_queue_url"))
                .thenThrow(new InterruptedException("Thread interrupted"));
        when(queueManager.queueExists("creation_queue_url")).thenReturn(true);

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> clusterQueueAccessCredsService.getClusterQueuesInfo(
                                                                    DUMMY_CLUSTER_ID));

        // Assert
        Assertions.assertEquals("Interrupted while fetching the queue credentials, error: Thread interrupted",
                                exception.getBody().getDetail());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false);
        verify(credentialGenerationService).getCredentialsForQueue("creation_queue_url");
    }

    @Test
    void generateCredsForNvcaQueues_returnSuccess()
            throws ExecutionException, InterruptedException {
        // Prepare
        String gpuName = "Standard_ND96amsr_A100_v4_1x";
        String createUrl = "q_gdn_spot_byoc_cluster_group_Standard_ND96amsr_A100_v4_1x.fifo";
        String clusterCreateUrl = "q_gdn_spot_byoc_cluster_id_Standard_ND96amsr_A100_v4_1x.fifo";
        String taskClusterCreateUrl = "q_gdn_spot_byoc_tasks_cluster_id_Standard_ND96amsr_A100_v4_1x.fifo";
        String terminationUrl = "q_gdn_spot_byoc_cluster_id.fifo";
        ClusterEntity clusterEntity = getClusterEntityWithQueuesTargetingAndTask(gpuName);

        when(queueManager.queueExists(createUrl)).thenReturn(true);
        when(queueManager.queueExists(clusterCreateUrl)).thenReturn(true);
        when(queueManager.queueExists(taskClusterCreateUrl)).thenReturn(true);
        when(queueManager.queueExists(terminationUrl)).thenReturn(true);
        doReturn(true).when(instanceServiceHelper).isTaskClusterCreationQueuesAllowed(Boolean.TRUE);
        doReturn(false).when(instanceServiceHelper).isNatsEnabled();

        when(credentialGenerationService
                     .getCredentialsForQueue(
                             clusterEntity.getCreationQueues().get(gpuName).getUrl()))
                .thenReturn(getDummyCredentialsForCreate());
        when(credentialGenerationService
                     .getCredentialsForQueue(
                             clusterEntity.getClusterCreationQueues().get(gpuName).getUrl()))
                .thenReturn(getDummyCredentialsForCreate());
        when(credentialGenerationService
                     .getCredentialsForQueue(
                             clusterEntity.getClusterCreationQueueForTasks().get(gpuName).getUrl()))
                .thenReturn(getDummyCredentialsForCreate());
        when(credentialGenerationService
                     .getCredentialsForQueue(
                             clusterEntity.getTerminationQueueUrl()))
                .thenReturn(getDummyCredentialsForCreate());

        // Act
        NvcaAccessCreds nvcaAccessCreds =
                clusterQueueAccessCredsService.generateCredsForNvcaQueues(clusterEntity);

        // Assert
        assertThat(nvcaAccessCreds).isNotNull();
        assertThat(nvcaAccessCreds.getCreationQueue()).isNotEmpty();
        assertThat(nvcaAccessCreds.getClusterCreationQueue()).isNotEmpty();
        assertThat(nvcaAccessCreds.getClusterCreationQueueForTasks()).isNotEmpty();
        assertThat(nvcaAccessCreds.getCreationQueue().size()).isEqualTo(1);
        assertThat(nvcaAccessCreds.getClusterCreationQueue().size()).isEqualTo(1);
        assertThat(nvcaAccessCreds.getClusterCreationQueueForTasks().size()).isEqualTo(1);
    }

    @Test
    void createQueueIfNotExists_withQueueNotPresent_returnSuccess() {

        // Prepare
        String queueName = "dummy_queue_name";
        String clusterName = DUMMY_BYOC_CLUSTER_NAME;
        String queueUrl = DUMMY_CREATION_QUEUE_URL;
        Map<String, String> attributes = Map.of("MessageRetentionPeriod", "100");

        when(queueManager.queueExists(queueName)).thenReturn(false);
        when(queueManager.createQueue(queueName, attributes)).thenReturn(queueUrl);

        // Act
        String output = clusterQueueAccessCredsService.createQueueIfNotExists(queueName,
                                                                              clusterName, attributes, true);
        // Assert
        assertEquals(output, queueUrl);
        verify(queueManager).queueExists(queueName);
        verify(queueManager).createQueue(queueName, attributes);
    }

    @Test
    void createQueueIfNotExists_withQueueExits_returnSuccess() {

        // Prepare
        String queueName = "dummy_queue_name";
        String clusterName = DUMMY_BYOC_CLUSTER_NAME;
        String queueUrl = DUMMY_CREATION_QUEUE_URL;
        Map<String, String> attributes = Map.of("MessageRetentionPeriod", "100");

        when(queueManager.queueExists(queueName)).thenReturn(true);
        when(queueManager.getQueueUrl(queueName, true)).thenReturn(queueUrl);

        when(queueManager.isQueueAttributesUpdateNeeded(queueName, attributes)).thenReturn(true);

        // Act
        String output = clusterQueueAccessCredsService.createQueueIfNotExists(queueName,
                                                                              clusterName,
                                                                              attributes, true);
        // Assert
        assertEquals(output, queueUrl);
        verify(queueManager).queueExists(queueName);
        verify(queueManager).isQueueAttributesUpdateNeeded(queueName, attributes);
        verify(queueManager).updateQueueAttributes(queueName, attributes);
        verify(queueManager).getQueueUrl(queueName, true);
    }

    @Test
    void createQueueIfNotExists_withQueueExitsAndAttributeUpdateDisabled_returnSuccess() {

        // Prepare
        String queueName = "dummy_queue_name";
        String clusterName = DUMMY_BYOC_CLUSTER_NAME;
        String queueUrl = DUMMY_CREATION_QUEUE_URL;
        Map<String, String> attributes = Map.of("MessageRetentionPeriod", "100");

        when(queueManager.queueExists(queueName)).thenReturn(true);
        when(queueManager.getQueueUrl(queueName, true)).thenReturn(queueUrl);

        when(queueManager.isQueueAttributesUpdateNeeded(queueName, attributes)).thenReturn(false);

        // Act
        String output = clusterQueueAccessCredsService.createQueueIfNotExists(queueName,
                clusterName,
                attributes, true);
        // Assert
        assertEquals(output, queueUrl);
        verify(queueManager).queueExists(queueName);
        verify(queueManager).isQueueAttributesUpdateNeeded(queueName, attributes);
        verify(queueManager, never()).updateQueueAttributes(queueName, attributes);
        verify(queueManager).getQueueUrl(queueName, true);
    }

    @Test
    void generateCredsForNvcaQueues_withNatsEnabled_returnEmptyResponse() {
        // Prepare
        String gpuName = "Standard_ND96amsr_A100_v4_1x";
        ClusterEntity clusterEntity = getClusterEntityWithQueuesTargetingAndTask(gpuName);
        doReturn(true).when(instanceServiceHelper).isNatsEnabled();

        // Act
        NvcaAccessCreds nvcaAccessCreds =
                clusterQueueAccessCredsService.generateCredsForNvcaQueues(clusterEntity);

        // Assert
        assertThat(nvcaAccessCreds).isNotNull();
        assertThat(nvcaAccessCreds.getCreationQueue()).isEmpty();
        assertThat(nvcaAccessCreds.getClusterCreationQueue()).isEmpty();
        assertThat(nvcaAccessCreds.getClusterCreationQueueForTasks()).isEmpty();
    }

    private Credentials getDummyCredentialsForCreate() {
        return Credentials.builder().accessKeyId("creation_accessKey")
                .secretAccessKey("creation_secretKey")
                .sessionToken("creation_sessionToken").expiration(
                        Instant.now().plusMillis(1000)).build();
    }

    private Credentials getDummyCredentialsForTerminate() {
        return Credentials.builder().accessKeyId("termination_accessKey")
                .secretAccessKey("termination_secretKey")
                .sessionToken("termination_sessionToken").expiration(
                        Instant.now().plusMillis(1000)).build();
    }

    private ClusterEntity getClusterEntity() {
        return ClusterEntity.builder()
                .clusterName("name")
                .clusterId("id")
                .ncaId("ncaId")
                .terminationQueueUrl("termination_queue_url")
                .terminationQueueType("FifoQueue")
                .clusterDescription("cluster_description")
                .clusterProvider(ClusterProviderEnum.GDN)
                .clusterStatus(ClusterStatusEnum.READY)
                .clusterGroupName("group_name")
                .clusterGroupId("group_id")
                .creationQueueUrl("creation_queue_url")
                .creationQueueType("FifoQueue")
                .gpus(buildGpuUdts())
                .authorizedNcaIds(Set.of("ncaId", "ncaId1"))
                .requestDump("request")
                .build();
    }

    private ClusterEntity getClusterEntityWithQueuesTargetingAndTask(String gpuName) {
        ClusterEntity clusterEntity = ClusterEntity.builder()
                .clusterName("name")
                .clusterId("id")
                .ncaId("ncaId")
                .terminationQueueUrl("termination_queue_url")
                .terminationQueueType("FifoQueue")
                .clusterDescription("cluster_description")
                .clusterProvider(ClusterProviderEnum.GDN)
                .clusterStatus(ClusterStatusEnum.READY)
                .clusterGroupName("group_name")
                .clusterGroupId("group_id")
                .creationQueueUrl("creation_queue_url")
                .creationQueueType("FifoQueue")
                .gpus(buildGpuUdts())
                .authorizedNcaIds(Set.of("ncaId", "ncaId1"))
                .requestDump("request")
                .allowClusterTargeting(true)
                .allowTaskClusterCreationQueues(true)
                .build();

        Map<String, CreationQueueUdt> creationQueueMap = new HashMap<>();
        creationQueueMap.put(gpuName, getCreationQueue(gpuName, DUMMY_CLUSTER_GROUP_ID));
        clusterEntity.setCreationQueues(creationQueueMap);

        Map<String, CreationQueueUdt> clusterCreationQueueMap = new HashMap<>();
        clusterCreationQueueMap.put(gpuName, getCreationQueue(gpuName, DUMMY_CLUSTER_ID));
        clusterEntity.setClusterCreationQueues(clusterCreationQueueMap);

        Map<String, CreationQueueUdt> taskClusterCreationQueueMap = new HashMap<>();
        taskClusterCreationQueueMap.put(gpuName, getCreationQueueForTasks(gpuName, DUMMY_CLUSTER_ID));
        clusterEntity.setClusterCreationQueuesForTasks(taskClusterCreationQueueMap);

        clusterEntity.setTerminationQueueUrl(
                String.format("q_gdn_spot_byoc_%s.fifo", DUMMY_CLUSTER_ID));
        clusterEntity.setTerminationQueueType(QueueAttributeName.FifoQueue.toString());

        return clusterEntity;
    }

    private CreationQueueUdt getCreationQueue(String gpuName, String clusterGroupId) {
        return CreationQueueUdt.builder()
                .url(String.format(
                        "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_%s_%s.fifo",
                        clusterGroupId, gpuName))
                .queueType(QueueAttributeName.FifoQueue.toString())
                .build();
    }

    private CreationQueueUdt getCreationQueueForTasks(String gpuName, String clusterId) {
        return CreationQueueUdt.builder()
                .url(String.format(
                        "https://sqs.us-west-2.amazonaws.com/123456/q_gdn_spot_byoc_tasks_%s_%s.fifo",
                        clusterId, gpuName))
                .queueType(QueueAttributeName.FifoQueue.toString())
                .build();
    }
}
