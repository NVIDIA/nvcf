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
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_PROVIDER;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_REGISTRATION_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CREATION_QUEUE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TERMINATION_QUEUE;
import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForDeletingCluster;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForRegisteringNewCluster;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForUpdateCluster;

import com.amazonaws.services.sqs.model.QueueAttributeName;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.converters.NvcaRequestSchemaToUdtConverter;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationCredentialsResponse;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import java.util.regex.Pattern;

import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ClusterRegistrationService {

    private final ClusterRepository clusterRepository;
    private final ClusterQueueAccessCredsService clusterQueueAccessCredsService;
    private final QueueManager queueManager;
    private final ObjectMapper objectMapper;
    private final TelemetryEventClient telemetryEventClient;
    private final ByocConfigurationProperties byocConfigurationProperties;
    private final AppAuditService auditService;
    private final InstanceServiceHelper instanceServiceHelper;
    private final ByocServiceHelper byocServiceHelper;
    private final ComputePlatformService computePlatformService;


    @Observed
    public BartRegistrationResponse registerCluster(
            BartRegistrationRequest bartRegistrationRequest,
            String clusterId,
            Map<String, Object> auditProps) {

        if (StringUtils.isBlank(bartRegistrationRequest.getClusterGroup())) {
            bartRegistrationRequest.setClusterGroup(bartRegistrationRequest.getClusterName());
        }

        if (bartRegistrationRequest.getAuthorizedNcaIds() == null) {
            bartRegistrationRequest.setAuthorizedNcaIds(new HashSet<>());
        }

        // check if cluster exists then do the re-registration
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterByAccountAndName(bartRegistrationRequest.getNcaId(),
                                                             bartRegistrationRequest.getClusterName());

        if (optionalClusterEntity.isPresent()) {
            ClusterEntity clusterEntity = optionalClusterEntity.get();
            validateClusterIdForReRegistering(clusterEntity, clusterId);

            // Cluster can be only re-registered if it is of BART flow
            validateClusterForBartFlow(clusterEntity);
            log.info("Cluster with id {}, already exists, doing re-registration", clusterId);
            return reRegisterCluster(clusterEntity, bartRegistrationRequest, auditProps);
        }

        // cluster does not exist but clusterGroup exists - add to the cluster group
        Optional<ClusterGroupsByAccountEntity> optionalClusterGroupsByAccountEntity =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        bartRegistrationRequest.getNcaId(),
                        bartRegistrationRequest.getClusterGroup());
        if (optionalClusterGroupsByAccountEntity.isPresent()) {
            log.info(
                    "Specified cluster group {} for cluster {} already exists, adding cluster to the same group",
                    bartRegistrationRequest.getClusterGroup(),
                    bartRegistrationRequest.getClusterName());
            return addClusterToGroup(optionalClusterGroupsByAccountEntity.get(),
                                     bartRegistrationRequest, clusterId, auditProps);
        }

        // Neither cluster nor cluster group exists
        log.info("Registering a new cluster {}", bartRegistrationRequest.getClusterName());
        return registerNewCluster(bartRegistrationRequest, clusterId, auditProps);
    }

    @Observed
    public void deleteCluster(String clusterId, Map<String, Object> auditProps) {
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);

        if (optionalClusterEntity.isEmpty()) {
            log.error("Could not find any cluster registered with id {}", clusterId);
            throw new IcmsNotFoundException(
                    "Could not find any cluster registered with id " + clusterId);
        }
        ClusterEntity clusterEntity = optionalClusterEntity.get();

        // Cluster can be unregistered only if it's belongs to BART flow
        validateClusterForBartFlow(clusterEntity);

        List<String> activeInstanceIds = instanceServiceHelper.getActiveInstancesFromZone(clusterId);
        if (!activeInstanceIds.isEmpty()) {
            String errMsg =
                    String.format(
                            "cluster un-registration failed, terminate following active instances %s",
                            activeInstanceIds);
            log.error(
                    "Active instances exists for cluster-id {} hence avoiding cluster un-registration {}",
                    clusterId, activeInstanceIds);
            throw new IcmsConflictException(errMsg);
        }

        try {
            clusterRepository.deleteClusterInfo(clusterEntity);

            // Audit log changes in DB
            populateAuditValuesForDeletingCluster(auditProps, clusterId);
            auditService.sendAuditEventForClusterEntity(auditProps, clusterEntity,
                                                        new ClusterEntity());

            queueManager.deleteQueue(clusterEntity.getTerminationQueueUrl());
            Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                    clusterRepository.getClusterGroupInfoByClusterGroupId(
                            clusterEntity.getClusterGroupId());
            if (optionalClusterGroupByGroupIdEntity.isEmpty()) {
                queueManager.deleteQueue(clusterEntity.getCreationQueueUrl());
            }

            // delete cluster creation queues as well if exists
            if (clusterEntity.getClusterCreationQueues() != null
                    && !clusterEntity.getClusterCreationQueues().isEmpty()) {
                clusterEntity.getClusterCreationQueues()
                        .values()
                        .forEach(queue -> queueManager.deleteQueue(queue.getUrl()));
            }

        } catch (IcmsInternalServerException icmsInternalServerException) {
            log.error("Failed to delete cluster info, cluster name - {}, internalServerError: error - {}",
                    optionalClusterEntity.get().getClusterName(),
                    icmsInternalServerException.getBody().getDetail());

            // rethrowing same exception
            throw icmsInternalServerException;

        } catch (Exception e) {
            log.error("Failed to delete cluster info, cluster name - {}, error - {}",
                    optionalClusterEntity.get().getClusterName(), e.getMessage());
            throw new IcmsInternalServerException(
                    String.format("Failed to un-register cluster, error: %s", e.getMessage()));
        }
    }

    private void validateClusterForBartFlow(ClusterEntity clusterEntity) {

        if (clusterEntity.getNvcaVersion() != null) {
            String errorMsg = String.format(
                    "BART flow can not be invoked as cluster %s was registered with NVCA 2.0 flow",
                    clusterEntity.getClusterId());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
    }

    private BartRegistrationResponse registerNewCluster(
            BartRegistrationRequest bartRegistrationRequest, String clusterId,
            Map<String, Object> auditProps) {

        // validations
        if (bartRegistrationRequest.getAuthorizedNcaIds() == null) {
            bartRegistrationRequest.setAuthorizedNcaIds(new HashSet<>());
        }
        validateNcaIdDoNotBelongsToAuthorizedNcaIds(bartRegistrationRequest.getAuthorizedNcaIds(),
                                                    bartRegistrationRequest.getNcaId());

        validateAuthorizedNcaIdsForNewCluster(bartRegistrationRequest.getAuthorizedNcaIds());

        Set<GpuUdt> requestedGpus = NvcaRequestSchemaToUdtConverter.toGpuUdts(bartRegistrationRequest.getGpus());

        validateGpusForNewCluster(requestedGpus);

        validateClusterStatus(bartRegistrationRequest.getStatus());

        String clusterGroupId = generateClusterGroupId();

        ClusterEntity clusterEntity =
                toClusterEntity(bartRegistrationRequest, clusterId, clusterGroupId);

        String creationQueueUrl = clusterQueueAccessCredsService.createQueue(
                String.format(byocConfigurationProperties.getQueueNameFormat(), clusterGroupId),
                clusterEntity.getClusterName());
        String terminationQueueUrl = clusterQueueAccessCredsService.createQueue(
                String.format(byocConfigurationProperties.getQueueNameFormat(),
                              clusterId.substring(10)),
                clusterEntity.getClusterName());

        clusterEntity.setCreationQueueType(String.valueOf(QueueAttributeName.FifoQueue));
        clusterEntity.setCreationQueueUrl(creationQueueUrl);
        clusterEntity.setTerminationQueueType(String.valueOf(QueueAttributeName.FifoQueue));
        clusterEntity.setTerminationQueueUrl(terminationQueueUrl);

        // save the information in DB as soon as we have all the things needed for cluster entity
        // This is to avoid creation of queues again in case this call fails and
        // cluster attempt to re-register
        clusterRepository.saveClusterInfo(clusterEntity);

        // generate creds for newly created queues
        BartRegistrationCredentialsResponse credentialsResponse =
                clusterQueueAccessCredsService.generateCredsForQueues(clusterEntity);

        // send telemetry
        sendTelemetryEvent(clusterEntity, "clusterRegistered");

        // Audit log changes in DB
        populateAuditValuesForRegisteringNewCluster(auditProps, clusterId);
        auditService.sendAuditEventForClusterEntity(auditProps, new ClusterEntity(), clusterEntity);

        return BartRegistrationResponse.builder()
                .clusterId(clusterId)
                .clusterGroupId(clusterGroupId)
                .credentials(credentialsResponse.getCredentials())
                .build();
    }

    private BartRegistrationResponse reRegisterCluster(
            ClusterEntity existingClusterEntity,
            BartRegistrationRequest bartRegistrationRequest,
            Map<String, Object> auditProps) {
        // do the validations
        if (bartRegistrationRequest.getAuthorizedNcaIds() == null) {
            bartRegistrationRequest.setAuthorizedNcaIds(new HashSet<>());
        }
        validateClusterGroup(existingClusterEntity.getClusterGroupName(),
                             bartRegistrationRequest.getClusterGroup());

        validateNcaIdDoNotBelongsToAuthorizedNcaIds(existingClusterEntity.getAuthorizedNcaIds(),
                                                    bartRegistrationRequest.getNcaId());

        validateClusterProvider(existingClusterEntity.getClusterProvider(),
                                bartRegistrationRequest.getClusterProvider());

        boolean isAuthorizedNcaIdUpdated =
                isAuthorizedNcaIdUpdateAllowed(existingClusterEntity.getAuthorizedNcaIds(),
                                               bartRegistrationRequest.getAuthorizedNcaIds(),
                                               existingClusterEntity.getClusterGroupId());
        Set<String> oldAuthorizedNcaId = new HashSet<>();

        if (isAuthorizedNcaIdUpdated) {
            validateAuthorizedNcaIdsForNewCluster(bartRegistrationRequest.getAuthorizedNcaIds());
            // Storing old nca-ids
            oldAuthorizedNcaId = existingClusterEntity.getAuthorizedNcaIds();
        } else {
            validateAuthorizedNcaIds(existingClusterEntity.getAuthorizedNcaIds(),
                                     bartRegistrationRequest.getAuthorizedNcaIds());
        }

        Set<GpuUdt> requestedGpus = NvcaRequestSchemaToUdtConverter.toGpuUdts(bartRegistrationRequest.getGpus());

        if (computePlatformService.isComputePlatformBackend(existingClusterEntity.getClusterGroupName())) {
            validateGpusForNewCluster(requestedGpus);
        } else {
            validateGpus(existingClusterEntity.getGpus(), requestedGpus);
        }

        validateClusterStatus(bartRegistrationRequest.getStatus());

        // update cluster entity
        ClusterEntity entityBefore = CopyUtil.deepCopy(existingClusterEntity);

        // If cluster status is marked as Abandoned, then it means that we have already deleted the
        // queues for that cluster and hence we will need to create the queues again when a client is
        // trying to re-register the cluster.
        if (existingClusterEntity.getClusterStatus() == ClusterStatusEnum.ABANDONED) {
            String creationQueueUrl = clusterQueueAccessCredsService.createQueue(
                    String.format(byocConfigurationProperties.getQueueNameFormat(),
                                  existingClusterEntity.getClusterGroupId()),
                    existingClusterEntity.getClusterName());
            String terminationQueueUrl = clusterQueueAccessCredsService.createQueue(
                    String.format(byocConfigurationProperties.getQueueNameFormat(),
                                  existingClusterEntity.getClusterId().substring(10)),
                    existingClusterEntity.getClusterName());

            existingClusterEntity.setCreationQueueType(
                    String.valueOf(QueueAttributeName.FifoQueue));
            existingClusterEntity.setCreationQueueUrl(creationQueueUrl);
            existingClusterEntity.setTerminationQueueType(
                    String.valueOf(QueueAttributeName.FifoQueue));
            existingClusterEntity.setTerminationQueueUrl(terminationQueueUrl);
        }
        if (computePlatformService.isComputePlatformBackend(existingClusterEntity.getClusterGroupName())) {
            existingClusterEntity.setGpus(requestedGpus);
        }
        if (isAuthorizedNcaIdUpdated) {
            // Setting new nca-ids
            existingClusterEntity.setAuthorizedNcaIds(
                    bartRegistrationRequest.getAuthorizedNcaIds());
        }

        existingClusterEntity.setClusterDescription(
                bartRegistrationRequest.getClusterDescription());
        existingClusterEntity.setClusterStatus(bartRegistrationRequest.getStatus());
        existingClusterEntity.setRegistrationTime(Instant.now());
        try {
            existingClusterEntity.setRequestDump(
                    objectMapper.writeValueAsString(bartRegistrationRequest));
        } catch (JacksonException jsonProcessingException) {
            String errorMsg = String.format(
                    "Json Processing exception when forming cluster entity from registration request for cluster: %s, error: %s. ",
                    bartRegistrationRequest.getClusterName(), jsonProcessingException.getMessage());
            log.error(errorMsg, jsonProcessingException);
            throw new IcmsInternalServerException(errorMsg);
        }

        // update the cluster information in DB
        clusterRepository.updateClusterInfo(existingClusterEntity, oldAuthorizedNcaId,
                                            isAuthorizedNcaIdUpdated);

        // generate creds for cluster queues
        BartRegistrationCredentialsResponse credentialsResponse =
                clusterQueueAccessCredsService.generateCredsForQueues(existingClusterEntity);

        // send telemetry
        sendTelemetryEvent(existingClusterEntity, "clusterReRegistered");

        // Audit log changes in DB
        populateAuditValuesForUpdateCluster(auditProps, existingClusterEntity.getClusterId());
        auditService.sendAuditEventForClusterEntity(auditProps, entityBefore,
                                                    existingClusterEntity);

        return BartRegistrationResponse.builder()
                .clusterId(existingClusterEntity.getClusterId())
                .clusterGroupId(existingClusterEntity.getClusterGroupId())
                .credentials(credentialsResponse.getCredentials())
                .build();

    }

    private BartRegistrationResponse addClusterToGroup(
            ClusterGroupsByAccountEntity clusterGroupEntity,
            BartRegistrationRequest bartRegistrationRequest,
            String clusterId, Map<String, Object> auditProps) {

        // do the validations
        validateNcaIdDoNotBelongsToAuthorizedNcaIds(bartRegistrationRequest.getAuthorizedNcaIds(),
                                                    bartRegistrationRequest.getNcaId());

        validateNcaId(clusterGroupEntity.getKey().getNcaId(), bartRegistrationRequest.getNcaId());

        validateAuthorizedNcaIds(clusterGroupEntity.getAuthorizedNcaIds(),
                                 bartRegistrationRequest.getAuthorizedNcaIds());

        Set<GpuUdt> requestedGpus = NvcaRequestSchemaToUdtConverter.toGpuUdts(bartRegistrationRequest.getGpus());
        validateGpus(clusterGroupEntity.getGpus(), requestedGpus);

        validateClusterStatus(bartRegistrationRequest.getStatus());

        validateClusterProvider(clusterGroupEntity.getClusterGroupId(),
                                bartRegistrationRequest.getClusterProvider());

        ClusterEntity clusterEntity =
                toClusterEntity(bartRegistrationRequest, clusterId,
                                clusterGroupEntity.getClusterGroupId());

        String terminationQueueUrl = clusterQueueAccessCredsService.createQueue(
                String.format(byocConfigurationProperties.getQueueNameFormat(),
                              clusterId.substring(10)),
                clusterEntity.getClusterName());

        clusterEntity.setCreationQueueType(clusterGroupEntity.getCreationQueueType());
        clusterEntity.setCreationQueueUrl(clusterGroupEntity.getCreationQueueUrl());
        clusterEntity.setTerminationQueueType(String.valueOf(QueueAttributeName.FifoQueue));
        clusterEntity.setTerminationQueueUrl(terminationQueueUrl);

        // save the information in DB
        clusterRepository.saveClusterInfo(clusterEntity);

        // generate creds for queues
        BartRegistrationCredentialsResponse credentialsResponse =
                clusterQueueAccessCredsService.generateCredsForQueues(clusterEntity);

        // send telemetry
        sendTelemetryEvent(clusterEntity, "clusterRegistered");

        // Audit log changes in DB
        populateAuditValuesForRegisteringNewCluster(auditProps, clusterId);
        auditService.sendAuditEventForClusterEntity(auditProps, new ClusterEntity(), clusterEntity);

        return BartRegistrationResponse.builder()
                .clusterId(clusterId)
                .clusterGroupId(clusterEntity.getClusterGroupId())
                .credentials(credentialsResponse.getCredentials())
                .build();
    }

    private void sendTelemetryEvent(ClusterEntity clusterEntity, String clusterRegistrationStatus) {
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

        try {
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withMetadata(metaData)
                                                               .withClusterId(clusterEntity.getClusterId())
                                                               .withClusterName(clusterEntity.getClusterName())
                                                               .withEventName(
                                                                       Events.BYOC_CLUSTER_REGISTERED.toString())));
        } catch (Exception e) {
            // Do not throw exceptions for telemetry failures
            log.warn(
                    "Error sending telemetry for the registration of cluster {}, with cluster group {}",
                    clusterEntity.getClusterName(), clusterEntity.getClusterGroupName());
        }
    }

    private void validateClusterIdForReRegistering(ClusterEntity clusterEntity, String actual) {
        String errorMsg;
        if (!Objects.equals(clusterEntity.getClusterId(), actual)) {
            errorMsg = String.format(
                    "There is already a cluster registered with %s clusterName and "
                            + "%s ncaId with another sub", clusterEntity.getClusterName(),
                    clusterEntity.getNcaId());
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    private void validateClusterGroup(String expected, String actual) {
        String errorMsg;
        if (!expected.equals(actual)) {
            errorMsg = String.format(
                    "There exists an entry for the cluster with different group name. Specified group name %s, existing group name %s",
                    actual,
                    expected);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    public static void validateNcaId(String expected, String actual) {
        String errorMsg;
        if (!expected.equals(actual)) {
            errorMsg = String.format(
                    "There exists an entry for the cluster with different ncaId. Specified ncaId %s, existing ncaId %s",
                    actual,
                    expected);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    private void validateClusterStatus(ClusterStatusEnum clusterStatusEnum) {
        String errorMsg;

        // Cluster status should not be provided as abandoned by client while trying to register a cluster
        // It should only be set as abandoned by ICMS for inactive clusters
        if (clusterStatusEnum == ClusterStatusEnum.ABANDONED) {
            errorMsg = String.format("Cannot register a cluster with %s status",
                                     ClusterStatusEnum.ABANDONED);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    public static void validateNcaIdDoNotBelongsToAuthorizedNcaIds(
            Set<String> authorizedNcaIds,
            String ncaId) {
        String errorMsg;
        if (authorizedNcaIds == null) {
            errorMsg = "Specified list authorizedNcaIds is null.";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }

        if (authorizedNcaIds.contains(ncaId)) {
            errorMsg = String.format(
                    "Specified ncaId %s is duplicated in the set of authorized ncaIds.",
                    ncaId);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    private void validateClusterProvider(ClusterProviderEnum expected, ClusterProviderEnum actual) {
        String errorMsg;
        if (expected != actual) {
            errorMsg = String.format(
                    "There exists an entry for the cluster with different provider. Specified cluster provider %s, existing cluster provider %s",
                    actual.toString(),
                    expected.toString());
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    private void validateClusterProvider(
            String clusterGroupId,
            ClusterProviderEnum providedValue) {
        Set<ClusterEntity> clusterEntitySet =
                clusterRepository.getAllClustersInAGroup(clusterGroupId);
        for (ClusterEntity clusterEntity : clusterEntitySet) {
            if (!clusterEntity.getClusterProvider().equals(providedValue)) {
                log.error(
                        "Specified clusterProvider {} is different that clusterProvider of clusterGroup {}",
                        providedValue, clusterEntity.getClusterProvider());
                throw new IcmsConflictException(String.format(
                        "Specified clusterProvider %s is different that clusterProvider of clusterGroup",
                        providedValue));
            }
        }
    }

    private void validateAuthorizedNcaIds(@NotNull Set<String> expected, @Nullable Set<String> actual) {
        String errorMsg;
        if (actual != null && expected.size() == actual.size()) {
            Set<String> expectedNcaIds = new HashSet<>(actual);
            expectedNcaIds.removeAll(expected);
            if (expectedNcaIds.isEmpty()) {
                return;
            }
        }
        // If size is not same then nca-ids have been updated
        errorMsg =
                "Specified authorized nca ids are not matching with the authorized nca ids of the cluster group";
        log.error(errorMsg);
        throw new IcmsConflictException(errorMsg);
    }

    public static void validateAuthorizedNcaIdsForNewCluster(@Nullable Set<String> actual) {
        String errorMsg;
        if (actual == null) {
            errorMsg =
                    "List of authorized nca ids is null.";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }

        if (actual.contains("*") && actual.size() != 1) {
            errorMsg =
                    "If specified authorized nca ids contains * then it should not have other entries.";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
        for (String ncaId : actual) {
            if (StringUtils.isBlank(ncaId)) {
                errorMsg =
                        "Specified authorized nca ids should not contain empty string.";
                log.error(errorMsg);
                throw new IcmsConflictException(errorMsg);
            }
        }
    }

    // With this we should validate that we have all the required values and for each gpu there is
    // exactly one default instance type
    private void validateGpusForNewCluster(Set<GpuUdt> gpus) {
        String errorMsg;
        for (GpuUdt gpu : gpus) {
            int defaultInstanceTypes = 0;
            for (InstanceTypeUdt instanceType : gpu.getInstanceTypes()) {
                if (Boolean.TRUE.equals(instanceType.getIsDefault())) {
                    defaultInstanceTypes++;
                }
            }
            if (defaultInstanceTypes != 1) {
                errorMsg = String.format(
                        "There should be exactly one default instance type for each gpu, however gpu %s is having %d default instance types",
                        gpu.getName(), defaultInstanceTypes);
                log.error(errorMsg);
                throw new IcmsConflictException(errorMsg);
            }
        }
    }

    private void validateGpus(Set<GpuUdt> expected, Set<GpuUdt> actual) {
        String errorMsg;
        if (expected.size() != actual.size()) {
            errorMsg =
                    "Specified gpus size are not matching with the size of gpus of the cluster group";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
        Set<String> expectedNames = expected.stream().map(GpuUdt::getName).collect(Collectors.toSet());
        Set<String> actualNames = actual.stream().map(GpuUdt::getName).collect(Collectors.toSet());
        if (expectedNames.size() != actualNames.size()) {
            errorMsg =
                    "Specified gpus names are different than the names of gpus of the cluster group";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
        int matching = 0;
        for (String name : actualNames) {
            if (expectedNames.contains(name)) {
                matching++;
            }
        }
        if (matching != actualNames.size()) {
            errorMsg =
                    "Specified gpus does not contain all the gpus of the cluster group";
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
        for (GpuUdt expectedGpu : expected) {
            for (GpuUdt actualGpu : actual) {
                if (expectedGpu.getName().equals(actualGpu.getName())) {
                    validateInstanceTypes(expectedGpu.getInstanceTypes(),
                                           actualGpu.getInstanceTypes(), false,
                                           expectedGpu.getName());
                }
            }
        }
    }

    public static void validateInstanceTypes(
            Set<InstanceTypeUdt> expected, Set<InstanceTypeUdt> actual,
            boolean isNvca2Flow, String gpuName) {
        validateInstanceTypesV5(
                expected.stream().map(NvcaConverter::toInstanceTypeV5)
                        .collect(Collectors.toSet()),
                actual.stream().map(NvcaConverter::toInstanceTypeV5)
                        .collect(Collectors.toSet()),
                isNvca2Flow, gpuName);
    }

    public static void validateInstanceTypesV5(
            Set<InstanceTypeV5Udt> expected, Set<InstanceTypeV5Udt> actual,
            boolean isNvca2Flow, String gpuName) {
        String errorMsg;
        Set<InstanceTypeV5Udt> expectedInstanceSet = new HashSet<>(actual);
        for (InstanceTypeV5Udt exp : expected) {
            InstanceTypeV5Udt found = null;
            for (InstanceTypeV5Udt act : expectedInstanceSet) {
                if (act.getName().equals(exp.getName()) &&
                        act.getValue().equals(exp.getValue()) &&
                        act.getDescription().equals(exp.getDescription()) &&
                        Objects.equals(act.getIsDefault(), exp.getIsDefault()) &&
                        act.getGpuMemory().equals(exp.getGpuMemory()) &&
                        act.getSystemMemory().equals(exp.getSystemMemory()) &&
                        act.getCpuCores() == exp.getCpuCores()) {
                    found = act;
                }
            }
            if (found == null) {
                if (isNvca2Flow) {
                    errorMsg = String.format(
                            "Specified instance type spec is not matching with the %s instance type "
                                    + "spec of the cluster sharing %s gpu in cluster group",
                            exp.getName(), gpuName);
                } else {
                    errorMsg =
                            "Specified instance type spec is not matching with the instance type "
                                    + "spec of the cluster group";
                }
                log.error(errorMsg);
                throw new IcmsConflictException(errorMsg);
            }
            expectedInstanceSet.remove(found);
        }

        if (!expectedInstanceSet.isEmpty()) {
            if (isNvca2Flow) {
                errorMsg = String.format(
                        "Number of instance types is not matching with the number of existing "
                                + "instance types of the cluster sharing %s gpus in cluster group",
                        gpuName);
            } else {
                errorMsg =
                        "Number of instance types is not matching with the number of existing "
                                + "instance types of the cluster group";
            }
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    private ClusterEntity toClusterEntity(
            BartRegistrationRequest bartRegistrationRequest,
            String clusterId, String clusterGroupId) {
        try {
            return ClusterEntity.builder()
                    .clusterName(bartRegistrationRequest.getClusterName())
                    .clusterId(clusterId)
                    .ncaId(bartRegistrationRequest.getNcaId())
                    .clusterDescription(bartRegistrationRequest.getClusterDescription())
                    .clusterGroupName(bartRegistrationRequest.getClusterGroup())
                    .clusterGroupId(clusterGroupId)
                    .clusterProvider(bartRegistrationRequest.getClusterProvider())
                    .clusterStatus(bartRegistrationRequest.getStatus())
                    .k8sVersion(bartRegistrationRequest.getK8sVersion())
                    .gpus(NvcaRequestSchemaToUdtConverter.toGpuUdts(bartRegistrationRequest.getGpus()))
                    .authorizedNcaIds(bartRegistrationRequest.getAuthorizedNcaIds())
                    .requestDump(objectMapper.writeValueAsString(bartRegistrationRequest))
                    .registrationTime(Instant.now())
                    .build();
        } catch (JacksonException jsonProcessingException) {
            String errorMsg = String.format(
                    "Json Processing exception when forming cluster entity from registration request for cluster: %s, error: %s.",
                    bartRegistrationRequest.getClusterName(), jsonProcessingException.getMessage());
            log.error(errorMsg, jsonProcessingException);
            throw new IcmsInternalServerException(errorMsg);
        }
    }

    private String generateClusterGroupId() {
        return generateRandomUUID();
    }

    private boolean isAuthorizedNcaIdUpdateAllowed(
            Set<String> expected, Set<String> actual, String clusterGroupId) {

        boolean isAuthorizedNcaIdUpdated = false;
        String errorMsg;
        if (byocConfigurationProperties.isAuthorizedNcaIdUpdateEnabled()) {
            if (expected.size() == actual.size()) {
                Set<String> expectedNcaIds = new HashSet<>(actual);
                expectedNcaIds.removeAll(expected);
                isAuthorizedNcaIdUpdated = !expectedNcaIds.isEmpty();
            } else {
                // If size is not same then nca-ids have been updated
                isAuthorizedNcaIdUpdated = true;
            }
        }

        if (isAuthorizedNcaIdUpdated) {
            // Check if this cluster is only cluster in cluster group
            Set<ClusterEntity> clustersInClusterGroup =
                    clusterRepository.getAllClustersInAGroup(clusterGroupId);
            if (clustersInClusterGroup.size() > 1) {
                errorMsg =
                        "Can not update authorization nca-id of cluster group having multiple clusters";
                log.error(errorMsg);
                throw new IcmsConflictException(errorMsg);
            }
        }
        return isAuthorizedNcaIdUpdated;
    }
}
