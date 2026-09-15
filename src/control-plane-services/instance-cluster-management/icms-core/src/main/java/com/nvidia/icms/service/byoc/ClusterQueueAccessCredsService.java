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

import com.amazonaws.services.sqs.model.QueueDeletedRecentlyException;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.configuration.aws.AwsQueueProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.byoc.AwsQueueAccessInfo;
import com.nvidia.icms.inbound.rest.model.byoc.BartAccessCreds;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationCredentialsResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.outbound.sts.CredentialsGenerationService;
import com.nvidia.icms.service.InstanceServiceHelper;

import java.util.HashMap;
import java.util.Map;
import java.util.Optional;

import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import software.amazon.awssdk.services.sts.model.Credentials;

import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.isClusterTargetingEnabled;

@Service
@Slf4j
@AllArgsConstructor
public class ClusterQueueAccessCredsService {

    private final ClusterRepository clusterRepository;
    private final CredentialsGenerationService credentialsGenerationService;
    private final QueueManager queueManager;
    private final AwsQueueProperties awsQueueProperties;
    private final InstanceServiceHelper instanceServiceHelper;

    @Observed
    public BartRegistrationCredentialsResponse getClusterQueuesInfo(String clusterId) {

        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);

        if (optionalClusterEntity.isEmpty()) {
            log.error("Could not find any cluster registered with id {}", clusterId);
            throw new IcmsNotFoundException(
                    "Could not find any cluster registered with id " + clusterId);
        }

        ClusterEntity clusterEntity = optionalClusterEntity.get();

        // New creds can be returned only if cluster is of bart flow
        validateClusterForBartFlow(clusterEntity);

        return generateCredsForQueues(clusterEntity);

    }

    private void validateClusterForBartFlow(ClusterEntity clusterEntity) {

        if (clusterEntity.getNvcaVersion() != null) {
            String errorMsg = String.format(
                    "New creds for bart cannot be generated for clusterId %s since "
                            + "it is registered with NVCA 2.0 flow", clusterEntity.getClusterId());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
    }

    BartRegistrationCredentialsResponse generateCredsForQueues(ClusterEntity clusterEntity) {
        // validate cluster status
        validateClusterStatus(clusterEntity.getClusterStatus());

        // generate creds
        Credentials creationQueueCreds = getQueueCreds(clusterEntity.getCreationQueueUrl());
        Credentials terminationQueueCreds = getQueueCreds(clusterEntity.getTerminationQueueUrl());

        // validate generated creds
        validateCreds(creationQueueCreds, clusterEntity.getCreationQueueUrl());
        validateCreds(terminationQueueCreds, clusterEntity.getTerminationQueueUrl());

        // form response
        return formCredentialsResponse(clusterEntity, creationQueueCreds, terminationQueueCreds);
    }

    public NvcaAccessCreds generateCredsForNvcaQueues(ClusterEntity clusterEntity) {
        // No need to generate creds when nats enabled
        // Returning empty responses
        if (instanceServiceHelper.isNatsEnabled()) {
            return NvcaAccessCreds.builder()
                    .terminationQueue(new AwsQueueAccessInfo())
                    .creationQueue(Map.of())
                    .clusterCreationQueue(Map.of())
                    .clusterCreationQueueForTasks(Map.of())
                    .build();
        }

        // validate cluster status
        validateClusterStatus(clusterEntity.getClusterStatus());

        boolean isAutoTargetingEnabled = isClusterTargetingEnabled(clusterEntity.getAllowClusterTargeting());
        boolean isClusterCreationQueuesForTasksEnabled = instanceServiceHelper.isTaskClusterCreationQueuesAllowed(
                clusterEntity.getAllowTaskClusterCreationQueues());

        // Generating creation queue credentials
        Map<String, AwsQueueAccessInfo> creationQueueCreds = new HashMap<>();
        Map<String, AwsQueueAccessInfo> clusterCreationQueueCreds = new HashMap<>();
        Map<String, AwsQueueAccessInfo> clusterCreationQueueCredsForTasks = new HashMap<>();
        var creationQueues = clusterEntity.getCreationQueues();
        var clusterCreationQueues = clusterEntity.getClusterCreationQueues();
        var clusterCreationQueueForTasks = clusterEntity.getClusterCreationQueueForTasks();

        if (creationQueues == null || creationQueues.isEmpty()) {
            String errMsg = String.format(
                    "Creation queue generation failed: for '%s' clusterId Gpus not configured",
                    clusterEntity.getClusterId());
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }

        for (Map.Entry<String, CreationQueueUdt> entry : creationQueues.entrySet()) {
            // Update Queue attributes
            updateQueueAttributes(entry.getValue().getUrl(), awsQueueProperties.getByocQueueAttributes());

            creationQueueCreds.put(entry.getKey(), generateAwsQueueAccessInfo(entry));
        }

        if (isAutoTargetingEnabled) {
            if (clusterCreationQueues != null) {
                for (Map.Entry<String, CreationQueueUdt> entry : clusterCreationQueues.entrySet()) {
                    // Update Queue attributes
                    updateQueueAttributes(entry.getValue().getUrl(), awsQueueProperties.getByocQueueAttributes());

                    clusterCreationQueueCreds.put(entry.getKey(), generateAwsQueueAccessInfo(entry));
                }
            }
            // Generate creds for task specific queues
            if (isClusterCreationQueuesForTasksEnabled && clusterCreationQueueForTasks != null) {
                for (Map.Entry<String, CreationQueueUdt> entry : clusterCreationQueueForTasks.entrySet()) {
                    // Update Queue attributes
                    updateQueueAttributes(entry.getValue().getUrl(), awsQueueProperties.getByocTasksQueueAttributes());

                    clusterCreationQueueCredsForTasks.put(entry.getKey(), generateAwsQueueAccessInfo(entry));
                }
            }
        }

        // Generating termination queue credentials
        Credentials terminationQueueCreds = getQueueCreds(clusterEntity.getTerminationQueueUrl());
        validateCreds(terminationQueueCreds, clusterEntity.getTerminationQueueUrl());
        AwsQueueAccessInfo terminationQueueAccessInfo =
                getQueueAccessInfoForNvca(clusterEntity.getTerminationQueueUrl(),
                        clusterEntity.getTerminationQueueType(),
                        terminationQueueCreds);

        // form response
        return NvcaAccessCreds.builder()
                .terminationQueue(terminationQueueAccessInfo)
                .creationQueue(creationQueueCreds)
                .clusterCreationQueue(clusterCreationQueueCreds)
                .clusterCreationQueueForTasks(clusterCreationQueueCredsForTasks)
                .build();
    }

    public String createNvcaTasksCreationQueue(String queueName, String clusterId, boolean updateQueueAttributesIfQueueExists) {
        try {
            return createQueueIfNotExists(queueName, clusterId, awsQueueProperties.getByocTasksQueueAttributes(), updateQueueAttributesIfQueueExists);
        } catch (Exception exception) {
            log.error("Failed to create a task specific queue {} for cluster {}, error - {}", queueName,
                    clusterId, exception.getMessage(), exception);

            // Re throwing same exception as exception handling is done in createQueue function
            throw exception;
        }
    }

    public String createNvcaFunctionCreationQueue(String queueName, String clusterId, boolean updateQueueAttributesIfQueueExists) {
        try {
            return createQueueIfNotExists(queueName, clusterId, awsQueueProperties.getByocQueueAttributes(), updateQueueAttributesIfQueueExists);
        } catch (Exception exception) {
            log.error("Failed to create a function specific queue {} for cluster {}, error - {}", queueName,
                    clusterId, exception.getMessage(), exception);

            // Re throwing same exception as exception handling is done in createQueue function
            throw exception;
        }
    }

    public String createNvcaTerminationQueue(String queueName, String clusterId, boolean updateQueueAttributesIfQueueExists) {
        try {
            return createQueueIfNotExists(queueName, clusterId, awsQueueProperties.getByocQueueAttributes(), updateQueueAttributesIfQueueExists);
        } catch (Exception exception) {
            log.error("Failed to create a termination specific queue {} for cluster {}, error - {}", queueName,
                    clusterId, exception.getMessage(), exception);

            // Re throwing same exception as exception handling is done in createQueue function
            throw exception;
        }
    }

    public String createNonByocClusterCreationQueue(String queueName, String clusterId, boolean updateQueueAttributesIfQueueExists) {
        try {
            return createQueueIfNotExists(queueName, clusterId, awsQueueProperties.getQueueAttributes(), updateQueueAttributesIfQueueExists);
        } catch (Exception exception) {
            log.error("Failed to create a queue {} for cluster {}, error - {}", queueName,
                    clusterId, exception.getMessage(), exception);

            // Re throwing same exception as exception handling is done in createQueue function
            throw exception;
        }
    }

    // TODO: Remove this function after removing BART registration flow
    public String createQueue(String queueName, String clusterName) {
        try {
            return queueManager.createQueue(queueName, awsQueueProperties.getByocQueueAttributes());
        } catch (Exception exception) {
            log.error("Failed to create a queue {} for cluster {}, error - {}", queueName,
                    clusterName, exception.getMessage(), exception);

            // Re throwing same exception as exception handling is done in createQueue function
            throw exception;
        }
    }

    @VisibleForTesting
    String createQueueIfNotExists(String queueName, String clusterId, Map<String, String> queueAttributes, boolean updateQueueAttributesIfQueueExists) {

        // For already present queue, updating queue attributes if needed
        if (queueManager.queueExists(queueName)) {
            log.info("createQueueIfNotExists: clusterId: {}, Queue with name {} already exists, skip attempt to create", clusterId, queueName);

            if (updateQueueAttributesIfQueueExists) {
                updateQueueAttributes(queueName, queueAttributes);
            }

            return queueManager.getQueueUrl(queueName, true);

        }

        // Creating new queue
        return createQueue(queueName, clusterId, queueAttributes);
    }

    private String createQueue(String queueName, String clusterId, Map<String, String> queueAttributes) {
        try {
            String queueUrl = queueManager.createQueue(queueName, queueAttributes);
            log.info("createQueue: clusterId: {}, created queue with queueUrl: {}", clusterId, queueUrl);
            return queueUrl;

        } catch (Exception exception) {
            log.error("createQueue: Failed to create a queue {} for cluster {}, error - {}, exception: ", queueName,
                    clusterId, exception.getMessage(), exception);

            // Re throwing same exception as exception handling is done in createQueue function
            throw exception;
        }
    }

    private void updateQueueAttributes(String queueUrl, Map<String, String> queueAttributes) {

        if (queueManager.isQueueAttributesUpdateNeeded(queueUrl, queueAttributes)) {
            queueManager.updateQueueAttributes(queueUrl, queueAttributes);
        }
    }

    private AwsQueueAccessInfo getQueueAccessInfoForNvca(
            String queueUrl, String queueType,
            Credentials credentials) {
        return AwsQueueAccessInfo.builder()
                .queueType(queueType)
                .url(queueUrl)
                .accessKeyId(credentials.accessKeyId())
                .secretAccessKey(credentials.secretAccessKey())
                .sessionToken(credentials.sessionToken())
                .expiresAt(credentials.expiration())
                .build();
    }

    private Credentials getQueueCreds(String queueUrl) {
        String errorMsg;
        if (!queueManager.queueExists(getQueueNameFromQueueUrl(queueUrl))) {
            errorMsg = String.format(
                    "Queue with url %s does not exists, so cannot generate creds for accessing this queue",
                    queueUrl);
            log.error(errorMsg);
            throw new IcmsInternalServerException(errorMsg);
        }
        try {
            return credentialsGenerationService.getCredentialsForQueue(queueUrl);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            errorMsg = String.format("Interrupted while fetching the queue credentials, error: %s", e.getMessage());
            log.error("error: {}", errorMsg, e);
            throw new IcmsInternalServerException(errorMsg);
        } catch (Exception e) {
            errorMsg = String.format("Exception while generating creds for queue %s, error %s",
                    queueUrl, e.getMessage());
            log.error(errorMsg, e);
            throw new IcmsInternalServerException(errorMsg, e);
        }
    }

    private void validateCreds(Credentials credentials, String queueUrl) {
        if (credentials == null) {
            log.error("Null or Empty creds generated for {}", queueUrl);
            throw new IcmsInternalServerException("Null or Empty creds generated for " + queueUrl);
        }
        if (StringUtils.isBlank(credentials.accessKeyId()) ||
                StringUtils.isEmpty(credentials.secretAccessKey()) ||
                StringUtils.isBlank(credentials.sessionToken()) ||
                null == credentials.expiration()) {
            log.error("Invalid creds generated for {}", queueUrl);
            throw new IcmsInternalServerException("Invalid creds generated for " + queueUrl);
        }
    }

    private BartRegistrationCredentialsResponse formCredentialsResponse(
            ClusterEntity clusterEntity,
            Credentials creationQueueCreds,
            Credentials terminationQueueCreds) {
        AwsQueueAccessInfo creationQueueAccessInfo = AwsQueueAccessInfo.builder()
                .queueType(clusterEntity.getCreationQueueType())
                .url(clusterEntity.getCreationQueueUrl())
                .accessKeyId(creationQueueCreds.accessKeyId())
                .secretAccessKey(creationQueueCreds.secretAccessKey())
                .sessionToken(creationQueueCreds.sessionToken())
                .expiresAt(creationQueueCreds.expiration())
                .build();

        AwsQueueAccessInfo terminationQueueAccessInfo = AwsQueueAccessInfo.builder()
                .queueType(clusterEntity.getTerminationQueueType())
                .url(clusterEntity.getTerminationQueueUrl())
                .accessKeyId(terminationQueueCreds.accessKeyId())
                .secretAccessKey(terminationQueueCreds.secretAccessKey())
                .sessionToken(terminationQueueCreds.sessionToken())
                .expiresAt(terminationQueueCreds.expiration())
                .build();

        return BartRegistrationCredentialsResponse.builder().credentials(
                        BartAccessCreds.builder().creationQueue(creationQueueAccessInfo)
                                .terminationQueue(terminationQueueAccessInfo).build())
                .build();
    }

    private void validateClusterStatus(ClusterStatusEnum clusterStatusEnum) {
        String errorMsg;
        if (clusterStatusEnum == ClusterStatusEnum.ABANDONED) {
            errorMsg = String.format("Cannot generate creds for a cluster with %s status",
                    ClusterStatusEnum.ABANDONED);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    private String getQueueNameFromQueueUrl(String queueUrl) {
        String[] strs = StringUtils.split(queueUrl, "/");
        return strs[strs.length - 1];
    }

    private AwsQueueAccessInfo generateAwsQueueAccessInfo(Map.Entry<String, CreationQueueUdt> entry) {
        CreationQueueUdt queueInfo = entry.getValue();
        Credentials queueCreds = getQueueCreds(queueInfo.getUrl());
        validateCreds(queueCreds, queueInfo.getUrl());

        return getQueueAccessInfoForNvca(queueInfo.getUrl(), queueInfo.getQueueType(), queueCreds);
    }

}
