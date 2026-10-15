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

import static com.nvidia.icms.inbound.rest.model.byoc.ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.getGpusV5;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toGpusV4;
import static com.nvidia.icms.service.byoc.ClusterRegistrationService.validateAuthorizedNcaIdsForNewCluster;
import static com.nvidia.icms.service.byoc.ClusterRegistrationService.validateNcaId;
import static com.nvidia.icms.service.byoc.ClusterRegistrationService.validateNcaIdDoNotBelongsToAuthorizedNcaIds;
import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.getNewlyAddedGpus;
import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.getRemovedGpuNames;
import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.getRemovedInstanceTypeName;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getMetadataForCluster;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.validateAndGetClusterSource;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.validateAuthorizedNcaIdsForExternalCluster;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.validateAuthorizedNcaIdsFormat;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.validateRegion;
import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForReconfigurationOfCluster;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.converters.NvcaRequestSchemaToUdtConverter;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterUpdateRequest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterConfigurationRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.GsonCompatMapper;

import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;

import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ClusterReconfigurationService {

    private final NvcaClusterRepository nvcaClusterRepository;

    private final ClusterRepository clusterRepository;

    private final TelemetryEventClient telemetryEventClient;

    private final AppAuditService auditService;

    private ClusterCreationService clusterCreationService;

    private ClusterTerminateService clusterTerminateService;

    private NvcaClusterRegistrationService clusterRegistrationService;

    private final InstanceServiceHelper instanceServiceHelper;

    private final NvcaConfigurationProperties nvcaConfigurationProperties;

    private final NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    @Observed
    public void reconfigureCluster(
            ClusterUpdateRequest clusterUpdateRequest, String ncaId, String clusterId,
            Map<String, Object> auditProps) {

        log.info("Reconfiguration request: clusterId {}, clusterUpdateRequest {}", clusterId,
                 GsonCompatMapper.toJson(clusterUpdateRequest));
        // Set default empty values
        setDefaultValues(clusterUpdateRequest);

        // Validate attributes with known attributes
        clusterCreationService.validateAttributes(clusterUpdateRequest.getAttributes());

        // Validate region
        validateRegion(clusterUpdateRequest.getRegion().toLowerCase());
        clusterUpdateRequest.setRegion(clusterUpdateRequest.getRegion().toLowerCase());

        // Validate and set clusterSource
        clusterUpdateRequest.setClusterSource(
                validateAndGetClusterSource(clusterUpdateRequest.getClusterSource()));

        // Validate if cluster exists
        ClusterEntity existingClusterEntity = getClusterIfExists(clusterId);

        // Cluster can be reconfigured only if it is of NVCA 2.0 flow
        validateClusterForNvca2Flow(existingClusterEntity);

        // Validate new provided ncaId, authorized ncaIds and Gpus
        generalNcaIdsAndGpusValidation(clusterUpdateRequest, existingClusterEntity, ncaId);

        // If new gpus are added then we need to directly add support for them
        // If gpus are removed then validate them for any active instances
        // No need to check if existing and provided capability contains DYNAMIC_GPU_DISCOVERY
        Set<GpuV5Udt> newlyAddedGpus;
        Set<String> removedGpuNames = new HashSet<>();
        Set<String> removedInstanceTypeNames = new HashSet<>();
        Set<GpuV5Udt> requestedGpus = NvcaRequestSchemaToUdtConverter.toGpuV5Udts(clusterUpdateRequest.getGpus());

        if (!(clusterUpdateRequest.getCapabilities().contains(DYNAMIC_GPU_DISCOVERY.toString()) &&
                existingClusterEntity.getCapabilities()
                        .contains(DYNAMIC_GPU_DISCOVERY.toString()))) {
            log.info("ClusterId {}, Existing Capabilities {}, New Capabilities {}",
                     existingClusterEntity.getClusterId(),
                     existingClusterEntity.getCapabilities(),
                     clusterUpdateRequest.getCapabilities());
            newlyAddedGpus = getNewlyAddedGpus(getGpusV5(existingClusterEntity),
                                               requestedGpus);
            removedInstanceTypeNames = getRemovedInstanceTypeName(getGpusV5(existingClusterEntity),
                                                                  requestedGpus);
            removedGpuNames = getRemovedGpuNames(getGpusV5(existingClusterEntity),
                                                 requestedGpus);
            logUpdatedGpus(clusterUpdateRequest, existingClusterEntity, newlyAddedGpus,
                           removedGpuNames, removedInstanceTypeNames);
        }

        // If new authorized NcaIds are added then we need to directly add support for them
        // If authorized NcaIds are removed then validate them for any active instances
        var newlyAddedNcaIds = getNewlyAddedNcaIds(existingClusterEntity.getAuthorizedNcaIds(),
                                                   clusterUpdateRequest.getAuthorizedNCAIds());
        var removedNcaIds = getRemovedNcaIds(existingClusterEntity.getAuthorizedNcaIds(),
                                             clusterUpdateRequest.getAuthorizedNCAIds());
        logUpdatedAuthorizedNcaIds(clusterUpdateRequest, existingClusterEntity,
                                   newlyAddedNcaIds, removedNcaIds);

        boolean isGroupChanged = isClusterGroupNameChanged(
                existingClusterEntity.getClusterGroupName(),
                clusterUpdateRequest.getClusterGroupName());

        if (!isSetEmptyOrNull(removedInstanceTypeNames) || !isSetEmptyOrNull(removedNcaIds) ||
                isGroupChanged) {
            // Fetch active instances for clusterId if GPUs or NcaIds are removed or clusterGroup changed
            List<InstanceByZoneEntity> activeInstances =
                    instanceServiceHelper.getActiveInstanceEntitiesFromZone(
                            existingClusterEntity.getClusterId());

            if (!activeInstances.isEmpty() && !isSetEmptyOrNull(removedInstanceTypeNames)) {
                // Validate if instance-types can be removed or not
                validateInstanceTypesToRemove(removedInstanceTypeNames, existingClusterEntity);
            }
            if (!activeInstances.isEmpty() && !isSetEmptyOrNull(removedNcaIds)) {
                // Validate if Authorized ncaIds can be removed or not
                validateAuthNcaIdsToRemove(removedNcaIds, existingClusterEntity, activeInstances);
            }

            // Check if clusterGroupName changed, if it is changed then validate cluster for new cluster group
            // If validation passes then delete cluster from old group and create new cluster in new group
            if (isGroupChanged) {
                updateClusterGroup(clusterUpdateRequest, ncaId, existingClusterEntity.getClusterId(),
                        existingClusterEntity, auditProps, toInstanceIdList(activeInstances));
                // If cluster group is updated then all things are already taken care of so nothing further is needed
                // Since cluster deletion and cluster recreation is done
                return;
            }
        }

        // Validate for cloud provider not to be changed when cluster group is same
        validateForCloudProvider(clusterUpdateRequest, existingClusterEntity);

        // ClusterGroupName and cloudProvider are not changed so normal reconfiguration needs to be checked
        reconfigureClusterInSameGroup(clusterUpdateRequest, existingClusterEntity, removedGpuNames,
                                      auditProps);

    }

    private void reconfigureClusterInSameGroup(
            ClusterUpdateRequest clusterUpdateRequest,
            ClusterEntity existingClusterEntity,
            Set<String> removedGpuNames,
            Map<String, Object> auditProps) {
        // Entity before
        ClusterEntity entityBefore = CopyUtil.deepCopy(existingClusterEntity);

        // Fetching allCluster info ONLY if needed
        if (!isSetEmptyOrNull(removedGpuNames)) {
            List<ClusterByGroupIdAndIdEntity> clusterByGroupIdList = clusterRepository
                    .getClustersFromClusterGroup(existingClusterEntity.getClusterGroupId());

            // Deleting creation queue for removed GPU
            clusterRegistrationService.deleteQueueForRemovedGpus(removedGpuNames,
                                                                 clusterByGroupIdList,
                                                                 existingClusterEntity);
        }


        // Perform cluster creation request from generate operations
        // New cluster will be in same state as existing cluster
        ClusterEntity newClusterEntity =
                getClusterEntityFromUpdateRequest(clusterUpdateRequest, existingClusterEntity);

        // Save advanced BYOC cluster configuration maps if provided
        updateClusterConfigurationMap(existingClusterEntity.getClusterId(), clusterUpdateRequest);

        nvcaClusterRepository.updateClusterConfiguration(newClusterEntity,
                                                         entityBefore.getAuthorizedNcaIds());

        // Send audit and telemetry events
        sendClusterReconfigurationAuditAndTelemetryEvent(auditProps,
                                                         existingClusterEntity.getClusterId(),
                                                         entityBefore, newClusterEntity);
    }

    private ClusterEntity getClusterEntityFromUpdateRequest(
            ClusterUpdateRequest clusterUpdateRequest,
            ClusterEntity clusterEntity) {
        clusterEntity.setClusterProvider(clusterUpdateRequest.getCloudProvider());
        clusterEntity.setAuthorizedNcaIds(clusterUpdateRequest.getAuthorizedNCAIds());
        Set<GpuV5Udt> requestedGpus = NvcaRequestSchemaToUdtConverter.toGpuV5Udts(clusterUpdateRequest.getGpus());
        if (!(clusterUpdateRequest.getCapabilities().contains(DYNAMIC_GPU_DISCOVERY.toString()) &&
                clusterEntity.getCapabilities().contains(DYNAMIC_GPU_DISCOVERY.toString()))) {
            clusterEntity.setGpusV4(toGpusV4(requestedGpus));
            clusterEntity.setGpusV5(requestedGpus);
        }
        clusterEntity.setCapabilities(clusterUpdateRequest.getCapabilities());
        clusterEntity.setAttributes(clusterUpdateRequest.getAttributes());
        clusterEntity.setNvcaVersion(clusterUpdateRequest.getNvcaVersion());
        clusterEntity.setClusterDescription(clusterUpdateRequest.getClusterDescription());
        clusterEntity.setRegion(clusterUpdateRequest.getRegion());
        clusterEntity.setCustomAttributes(clusterUpdateRequest.getCustomAttributes());
        clusterEntity.setClusterKeyId(clusterUpdateRequest.getClusterKeyId());
        clusterEntity.setClusterSource(clusterUpdateRequest.getClusterSource());
        return clusterEntity;
    }

    private void validateForCloudProvider(
            ClusterUpdateRequest clusterUpdateRequest,
            ClusterEntity existingClusterEntity) {
        boolean isCloudProviderChanged =
                isCloudProviderChanged(existingClusterEntity.getClusterProvider().toString(),
                                       clusterUpdateRequest.getCloudProvider().toString());
        if (isCloudProviderChanged) {
            // Cloud provider alone can not be changed, it should be changed with cluster group
            String errMsg = "Cloud provider can not be changed when cluster group is same";
            log.error(errMsg);
            throw new IcmsConflictException(errMsg);
        }
    }

    private void validateInstanceTypesToRemove(
            Set<String> removedInstanceType, ClusterEntity clusterInfo) {

        // Check if active instances exists for removed GPUs
        Set<String> activeInstanceIds = instanceServiceHelper.getActiveInstancesFromZoneForInstanceType(
                clusterInfo.getClusterId(), removedInstanceType);
        if (!activeInstanceIds.isEmpty()) {
            String errMsg = String.format(
                    "Cluster reconfiguration failed, active instances exists for removed %s GPUs, activeInstanceIds %s",
                    removedInstanceType, activeInstanceIds);
            log.error(
                    "Active instances exists for cluster-id {} with {} instanceTypes "
                            + "hence avoiding GPUs reconfiguration {}",
                    clusterInfo.getClusterId(), removedInstanceType, activeInstanceIds);
            throw new IcmsConflictException(errMsg);
        }
    }

    private void validateAuthNcaIdsToRemove(
            Set<String> removedNcaIds, ClusterEntity clusterEntity,
            List<InstanceByZoneEntity> activeInstancesForCluster) {

        // Filter active instances for ncaIds
        Set<String> activeInstanceIds = activeInstancesForCluster.stream()
                .filter(entity -> removedNcaIds.contains(entity.getNcaId()))
                .map(InstanceByZoneEntity::getInstanceId)
                .collect(Collectors.toSet());

        if (!activeInstanceIds.isEmpty()) {
            String errMsg = String.format(
                    "Cluster reconfiguration failed, active instances exists for removed %s NcaIds, activeInstanceIds %s",
                    removedNcaIds, activeInstanceIds);
            log.error(
                    "Active instances exists for cluster-id {} with {} ncaIds hence avoiding reconfiguration {}",
                    clusterEntity.getClusterId(), removedNcaIds, activeInstanceIds);
            throw new IcmsConflictException(errMsg);
        }

    }

    private void logUpdatedGpus(
            ClusterUpdateRequest clusterUpdateRequest, ClusterEntity existingClusterEntity,
            Set<GpuV5Udt> newlyAddedNcaIds, Set<String> removedGpusNames,
            Set<String> removedInstanceTypes) {
        log.info(
                "From cluster reconfiguration: For clusterId: {} , clusterName: {} from clusterGroupId: {} from clusterGroupName: {} ,"
                        +
                        " ExistingGPUsV5: {} , ProvidedGPUs: {} , newlyAddedGPUs: {} , Removed GPU Names: {}, "
                        +
                        "Removed InstanceType Names: {}",
                existingClusterEntity.getClusterId(), existingClusterEntity.getClusterName(),
                existingClusterEntity.getClusterGroupId(),
                existingClusterEntity.getClusterGroupName(),
                setToJson(getGpusV5(existingClusterEntity)),
                setToJson(clusterUpdateRequest.getGpus()),
                setToJson(newlyAddedNcaIds),
                setToJson(removedGpusNames),
                setToJson(removedInstanceTypes));
    }

    private void logUpdatedAuthorizedNcaIds(
            ClusterUpdateRequest clusterUpdateRequest,
            ClusterEntity existingClusterEntity,
            Set<String> newlyAddedNcaIds, Set<String> removedNcaIds) {
        log.info(
                "ClusterReconfigurationService: For clusterId: {} , clusterName: {} from clusterGroupId: "
                        + "{} from clusterGroupName: {} , ExistingNcaIds: {} , ProvidedNcaIds: {} , "
                        + "newlyAddedNcaIds: {} , Removed NcaIds: {}",
                existingClusterEntity.getClusterId(), existingClusterEntity.getClusterName(),
                existingClusterEntity.getClusterGroupId(),
                existingClusterEntity.getClusterGroupName(),
                existingClusterEntity.getAuthorizedNcaIds(),
                clusterUpdateRequest.getAuthorizedNCAIds(),
                newlyAddedNcaIds, removedNcaIds);
    }

    private void updateClusterConfigurationMap(
            String clusterId,
            ClusterUpdateRequest clusterUpdateRequest) {
        /*
        1. Cluster configuration already present in DB:
            a. New values provided -> update in DB
            b. Configuration not provided -> remove existing values
        3. Cluster configuration already not configured in DB:
            a. New values provided -> update in DB
            b. Configuration not provided -> NO OP
         */

        Map<String, String> config = clusterUpdateRequest.getClusterConfigurations();
        Map<String, String> files = clusterUpdateRequest.getClusterConfigurationFiles();
        boolean hasConfig = config != null && !config.isEmpty();
        boolean hasFiles = files != null && !files.isEmpty();

        // If both values are removed and existing values present in DB then we should delete entry
        if (!hasConfig && !hasFiles) {
            if (nvcaClusterConfigurationRepository.findByClusterId(clusterId).isPresent()) {
                log.info("ClusterReconfigurationService: clusterId: {} cluster configuration values removed, deleting entry from DB",
                        clusterId);
                nvcaClusterConfigurationRepository.deleteByClusterId(clusterId);
            }
            return;
        }
        nvcaClusterConfigurationRepository.saveOrUpdateConfiguration(
                clusterId,
                hasConfig ? config : null,
                hasFiles ? files : null
        );
    }

    private Set<String> getNewlyAddedNcaIds(
            Set<String> existingNcaIds, Set<String> providedNcaIds) {
        if (isSetEmptyOrNull(providedNcaIds)) {
            return Collections.emptySet();
        }
        var copySet = new HashSet<>(providedNcaIds);
        copySet.removeAll(existingNcaIds);

        return copySet;
    }

    private Set<String> getRemovedNcaIds(Set<String> existingNcaIds, Set<String> providedNcaIds) {
        if (isSetEmptyOrNull(existingNcaIds)) {
            return Collections.emptySet();
        }
        var copySet = new HashSet<>(existingNcaIds);
        copySet.removeAll(providedNcaIds);

        return copySet;
    }

    private void generalNcaIdsAndGpusValidation(
            ClusterUpdateRequest clusterUpdateRequest,
            ClusterEntity existingClusterEntity, String ncaId) {

        // Validate ncaId of clusterID
        validateNcaId(ncaId, existingClusterEntity.getNcaId());

        // Validate nca id is not part of authorized nca ids
        validateNcaIdDoNotBelongsToAuthorizedNcaIds(clusterUpdateRequest.getAuthorizedNCAIds(),
                                                    ncaId);

        // Validate authorized nca id for * and empty strings
        validateAuthorizedNcaIdsForNewCluster(clusterUpdateRequest.getAuthorizedNCAIds());
        if (nvcaConfigurationProperties.isAuthorizedNcaIdRegexValidationEnabled()) {
            validateAuthorizedNcaIdsFormat(clusterUpdateRequest.getAuthorizedNCAIds());
        }

        // Validate authorized nca id for external clusters
        validateAuthorizedNcaIdsForExternalCluster(clusterUpdateRequest.getAuthorizedNCAIds(),
                                                   existingClusterEntity.getAuthClientId());

        // Validate gpus based on capability and also for single default instance types

        clusterCreationService.validateGpusForNewCluster(
                NvcaRequestSchemaToUdtConverter.toGpuV5Udts(clusterUpdateRequest.getGpus()),
                clusterUpdateRequest.getCapabilities());
    }

    private void setDefaultValues(ClusterUpdateRequest clusterUpdateRequest) {
        if (clusterUpdateRequest.getAuthorizedNCAIds() == null) {
            clusterUpdateRequest.setAuthorizedNCAIds(new HashSet<>());
        }
        if (clusterUpdateRequest.getGpus() == null) {
            clusterUpdateRequest.setGpus(new HashSet<>());
        }
        if (clusterUpdateRequest.getCapabilities() == null) {
            clusterUpdateRequest.setCapabilities(new HashSet<>());
        }
        if (clusterUpdateRequest.getAttributes() == null) {
            clusterUpdateRequest.setAttributes(new HashSet<>());
        }

    }

    private void validateClusterForNvca2Flow(ClusterEntity clusterEntity) {

        if (clusterEntity.getNvcaVersion() == null) {
            String errorMsg = String.format(
                    "This cluster %s can not be reconfigured since it was registered with NVCA 1.0 flow",
                    clusterEntity.getClusterId());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
    }

    private void updateClusterGroup(
            ClusterUpdateRequest clusterUpdateRequest, String ncaId, String clusterId,
            ClusterEntity existingClusterEntity, Map<String, Object> auditProps,
            List<String> activeInstances) {

        // Check if new cluster group exists for the ncaId
        Optional<ClusterGroupsByAccountEntity> optionalClusterGroupForNewGroupName =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        ncaId,
                        clusterUpdateRequest.getClusterGroupName());

        // Entity before
        ClusterEntity entityBefore = CopyUtil.deepCopy(existingClusterEntity);
        ClusterEntity newClusterEntity;
        // Case 1:
        // Cluster group already exists and need to move cluster to this group
        // Validate if cluster can be moved to new cluster group and then move the cluster
        if (optionalClusterGroupForNewGroupName.isPresent()) {

            var clusterGroupForNewGroupName = optionalClusterGroupForNewGroupName.get();

            // Validate if cluster can be added to the cluster group
            validateClusterForClusterGroup(clusterUpdateRequest, clusterGroupForNewGroupName,
                                           ncaId);

            // Once everything is validated then terminate cluster from old cluster group
            // Use original termination flow to terminate the cluster which will even delete queues
            clusterTerminateService.deleteClusterForReconfiguration(existingClusterEntity,
                                                                    clusterId,
                                                                    activeInstances, auditProps);

            // Perform cluster creation request from generate operations
            // New cluster will be in same state as existing cluster
            newClusterEntity =
                    performClusterCreationFromUpdateRequest(clusterUpdateRequest,
                                                            existingClusterEntity,
                                                            clusterId,
                                                            clusterGroupForNewGroupName.getClusterGroupId(),
                                                            auditProps);
        } else {
            // Case 2:
            // Cluster is being moved to new cluster group
            // Do the validation for naming
            validateClusterGroupNaming(clusterUpdateRequest);

            // Terminate cluster from old cluster group
            // Use original termination flow to terminate the cluster which will even delete queues
            clusterTerminateService.deleteClusterForReconfiguration(existingClusterEntity,
                                                                    clusterId,
                                                                    activeInstances, auditProps);

            // Perform cluster creation request from generate operations
            // New cluster will be in NOT READY state as cluster is moved to different cluster group
            String clusterGroupId = generateRandomUUID();
            newClusterEntity =
                    performClusterCreationFromUpdateRequest(clusterUpdateRequest,
                                                            existingClusterEntity,
                                                            clusterId, clusterGroupId, auditProps);
        }

        // Send audit and telemetry events
        sendClusterReconfigurationAuditAndTelemetryEvent(auditProps, clusterId,
                                                         entityBefore, newClusterEntity);
    }

    private void validateClusterGroupNaming(ClusterUpdateRequest clusterUpdateRequest) {
        if (clusterUpdateRequest.getClusterGroupName().length() > 32) {
            String errorMsg = String.format(
                    "The cluster group name %s exceeds the limit of 32 chars",
                    clusterUpdateRequest.getClusterGroupName());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
        if (clusterUpdateRequest.getClusterDescription() != null &&
                clusterUpdateRequest.getClusterDescription().length() > 32) {
            String errorMsg = String.format(
                    "The cluster description %s exceeds the limit of 32 chars",
                    clusterUpdateRequest.getClusterDescription());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
    }

    private ClusterEntity performClusterCreationFromUpdateRequest(
            ClusterUpdateRequest clusterUpdateRequest,
            ClusterEntity existingClusterEntity,
            String clusterId, String clusterGroupId,
            Map<String, Object> auditProps) {
        // Since cluster is being moved to different cluster group we are adding
        // gpus which are provided in PUT request
        ClusterCreationRequest clusterCreationRequest =
                ClusterCreationRequest.builder()
                        .clusterName(existingClusterEntity.getClusterName())
                        .clusterGroupName(clusterUpdateRequest.getClusterGroupName())
                        .clusterDescription(clusterUpdateRequest.getClusterDescription())
                        .ncaId(existingClusterEntity.getNcaId())
                        .authorizedNCAIds(clusterUpdateRequest.getAuthorizedNCAIds())
                        .cloudProvider(clusterUpdateRequest.getCloudProvider())
                        .capabilities(clusterUpdateRequest.getCapabilities())
                        .attributes(clusterUpdateRequest.getAttributes())
                        .customAttributes(clusterUpdateRequest.getCustomAttributes())
                        .gpus(clusterUpdateRequest.getGpus())
                        .nvcaVersion(clusterUpdateRequest.getNvcaVersion())
                        .ssaClientId(existingClusterEntity.getAuthClientId())
                        .oAuthClientId(existingClusterEntity.getAuthClientId())
                        .region(clusterUpdateRequest.getRegion())
                        .clusterKeyId(clusterUpdateRequest.getClusterKeyId())
                        .clusterSource(clusterUpdateRequest.getClusterSource())
                        .clusterConfigurations(clusterUpdateRequest.getClusterConfigurations())
                        .clusterConfigurationFiles(clusterUpdateRequest.getClusterConfigurationFiles())
                        .build();

        // Do the validation for naming
        clusterCreationService.validateClusterNamingForLength(clusterCreationRequest);

        // Save, new cluster in cluster group
        return clusterCreationService.clusterCreationUpdateInDb(clusterCreationRequest, clusterId,
                                                                clusterGroupId, auditProps,
                                                                existingClusterEntity);
    }

    private void sendClusterReconfigurationAuditAndTelemetryEvent(
            Map<String, Object> auditProps,
            String clusterId,
            ClusterEntity entityBefore,
            ClusterEntity newClusterEntity) {
        // Audit log changes in DB
        populateAuditValuesForReconfigurationOfCluster(auditProps, clusterId);
        auditService.sendAuditEventForClusterEntity(auditProps, entityBefore, newClusterEntity);

        // Send telemetry
        sendTelemetryEvent(newClusterEntity);
    }

    private void sendTelemetryEvent(ClusterEntity clusterEntity) {
        Map<String, Object> metaData = getMetadataForCluster(clusterEntity,
                                                             "nvcaClusterReconfigured");

        try {
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withMetadata(metaData)
                                                               .withClusterId(clusterEntity.getClusterId())
                                                               .withClusterName(clusterEntity.getClusterName())
                                                               .withEventName(
                                                                       Events.NVCA_CLUSTER_RECONFIGURED.toString())));
        } catch (Exception e) {
            // Do not throw exceptions for telemetry failures
            log.warn(
                    "Error sending telemetry for the creation of cluster {}, with cluster group {}",
                    clusterEntity.getClusterName(), clusterEntity.getClusterGroupName());
        }
    }

    private void validateClusterForClusterGroup(
            ClusterUpdateRequest clusterUpdateRequest,
            ClusterGroupsByAccountEntity clusterGroupForNewGroupName,
            String ncaId) {
        // validate cluster group for new flow
        clusterCreationService.validateClusterGroupForNvca2Flow(clusterGroupForNewGroupName);

        // Validate NcaId of cluster to be added in cluster group
        // Nca ID of cluster should be same as ncaId of cluster group
        validateNcaId(clusterGroupForNewGroupName.getKey().getNcaId(), ncaId);

        // validate cloud provider is same as cluster provider of cluster group
        clusterCreationService.validateClusterProvider(
                clusterGroupForNewGroupName.getClusterGroupId(),
                clusterUpdateRequest.getCloudProvider());
    }

    private boolean isClusterGroupNameChanged(String existingGroup, String newGroup) {
        return !existingGroup.equals(newGroup);
    }

    private boolean isCloudProviderChanged(String existingCloudProvider, String newCloudProvider) {
        return !existingCloudProvider.equals(newCloudProvider);
    }

    private String setToJson(Set<?> gpuV4Set) {
        if (!isSetEmptyOrNull(gpuV4Set)) {
            return GsonCompatMapper.toJson(gpuV4Set);
        }
        return "";
    }

    private ClusterEntity getClusterIfExists(String clusterId) {
        var optionalClusterInfo =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        if (optionalClusterInfo.isEmpty()) {
            String errMsg = String.format("Cluster with clusterId %s does not exist", clusterId);
            log.error(errMsg);
            throw new IcmsNotFoundException(errMsg);
        }
        return optionalClusterInfo.get();
    }

    // This function will return list of instanceIds
    private List<String> toInstanceIdList(List<InstanceByZoneEntity> entities) {
        return entities.stream()
                .map(InstanceByZoneEntity::getInstanceId)
                .toList();
    }
}
