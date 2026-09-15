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
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.getGpusV5;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toGpusV4;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.defaultGpuValidation;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getMetadataForCluster;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TERMINATION_QUEUE;
import static com.nvidia.icms.service.telemetry.model.Events.NVCA_CLUSTER_UPDATE;
import static com.nvidia.icms.util.AuthUtils.computeJwksFingerprint;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForUpdateCluster;

import com.amazonaws.services.sqs.model.QueueAttributeName;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.converters.NvcaRequestSchemaToUdtConverter;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationResponse;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.ByocServiceHelper;
import com.nvidia.icms.service.byoc.ClusterQueueAccessCredsService;
import com.nvidia.icms.service.byoc.ClusterRegistrationService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nimbusds.jose.jwk.JWKSet;
import java.nio.charset.StandardCharsets;
import java.text.ParseException;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.GsonCompatMapper;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.regex.Pattern;

import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class NvcaClusterRegistrationService {

    // A GPU name must only contain alphanumeric characters, hyphens,
    // or underscores (required for SQS FIFO queue name compatibility)
    private static final Pattern VALID_GPU_NAME_PATTERN = Pattern.compile("^[a-zA-Z0-9_-]+$");

    /** Maximum allowed JWKS payload size: 64 KB. */
    public static final int MAX_JWKS_SIZE_BYTES = 65_536;

    private final NvcaClusterRepository nvcaClusterRepository;

    private final NvcaConfigurationProperties nvcaConfigurationProperties;

    private final ClusterQueueAccessCredsService clusterQueueAccessCredsService;

    private final ByocConfigurationProperties byocConfigurationProperties;

    private final ClusterRepository clusterRepository;

    private final ClusterOidcIdentityService clusterOidcIdentityService;

    private final QueueManager queueManager;

    private final TelemetryEventClient telemetryEventClient;

    private final AppAuditService auditService;

    private final InstanceServiceHelper instanceServiceHelper;

    private final ByocServiceHelper byocServiceHelper;

    public static final String SSA_CLUSTER_ID_PREFIX = "nvssa";

    /**
     * Enum representing the different types of creation queues that can be created/deleted.
     */
    private enum QueueType {
        CLUSTER_GROUP,      // Shared across all clusters in the cluster group
        CLUSTER_SPECIFIC,   // Unique to specific cluster
        TASK_SPECIFIC,       // For task-based operations within cluster
        TERMINATION_QUEUE // Termination queue specific to cluster
    }

    // TODO: Create a helper class and move these function
    public static Set<String> getRemovedGpuNames(Set<GpuV5Udt> existingGpus, Set<GpuV5Udt> providedGpus) {
        if (isSetEmptyOrNull(existingGpus)) {
            return Collections.emptySet();
        }

        Set<String> existingGpuNames = new HashSet<>();
        existingGpus.forEach(gpuV5 -> existingGpuNames.add(gpuV5.getName()));

        Set<String> providedGpuNames = new HashSet<>();
        providedGpus.forEach(gpuV5 -> providedGpuNames.add(gpuV5.getName()));

        existingGpuNames.removeAll(providedGpuNames);

        return existingGpuNames;
    }

    // TODO: Create a helper class and move these function
    public static Set<String> getRemovedInstanceTypeName(
            Set<GpuV5Udt> existingGpus, Set<GpuV5Udt> providedGpus) {
        if (isSetEmptyOrNull(existingGpus)) {
            return Collections.emptySet();
        }

        Set<String> existingInstanceTypeNames = new HashSet<>();
        existingGpus.forEach(gpuV5 -> gpuV5.getInstanceTypes()
                .forEach(instanceType -> existingInstanceTypeNames.add(instanceType.getName())));

        Set<String> providedInstanceTypeNames = new HashSet<>();
        providedGpus.forEach(gpuV5 -> gpuV5.getInstanceTypes()
                .forEach(instanceType -> providedInstanceTypeNames.add(instanceType.getName())));

        existingInstanceTypeNames.removeAll(providedInstanceTypeNames);

        return existingInstanceTypeNames;
    }

    // TODO: Create a helper class and move these function
    public static boolean isClusterTargetingEnabled(Boolean isAutoTargetingEnabled) {
        return Boolean.TRUE.equals(isAutoTargetingEnabled);
    }

    /**
     * Validate GPU name for:
     * 1. Char size should be <=22 (required for SQS FIFO queue name should be less than 80 characters)
     * 2. Must only contain alphanumeric characters, hyphens, or underscores (required for SQS FIFO queue name compatibility)
     *
     * @param gpus GPUs to be validated
     */
    // TODO: Create a helper class and move these function
    public static void validateGpuName(@NonNull Set<GpuV5Udt> gpus) {
        for (GpuV5Udt gpu : gpus) {
            String gpuName = gpu.getName();
            if (gpuName.length() >= 23) {
                String error = String.format(
                        "GPU name chars must be <=22, provided %s GPU name is of %d size",
                        gpuName, gpuName.length());
                log.error("GPU name is exceeding allowed 23 char limit, error - {}", error);
                throw new IcmsBadRequestException(error);
            }
            if (!VALID_GPU_NAME_PATTERN.matcher(gpuName).matches()) {
                String error = String.format("GPU name '%s' contains invalid characters. "
                                                     + "Only alphanumeric characters, hyphens (-), and underscores (_) are allowed.",
                                             gpuName);
                log.error("GPU name contains invalid characters, error - {}", error);
                throw new IcmsBadRequestException(error);
            }
        }
    }

    /**
     * @param clusterId clusterId from authentication token
     * @return {@link NvcaAccessCreds}
     * <p>
     * If authentication token is ApiKey then clusterId should be used directly
     * If authentication token is JWT then clusterId should be hashed value of clusterId
     */
    @Observed
    public NvcaAccessCreds renewAccessCredentials(@NotNull String clusterId) {
        ClusterEntity clusterInfo = validateClusterId(clusterId);
        validateStatus(clusterInfo.getClusterStatus());
        return clusterQueueAccessCredsService.generateCredsForNvcaQueues(clusterInfo);
    }

    /**
     * @param nvcaRegistrationRequest NVCA registration request
     * @param clusterId               clusterId from authentication token
     * @param auditProps              AuditProperties
     * @return {@link NvcaRegistrationResponse}
     * <p>
     * If authentication token is ApiKey then clusterId should be used directly
     * If authentication token is JWT then clusterId should be hashed value of clusterId
     */
    @Observed
    public NvcaRegistrationResponse nvcaClusterRegistration(
            @NotNull NvcaRegistrationRequest nvcaRegistrationRequest,
            @NotNull String clusterId,
            @NotNull Map<String, Object> auditProps) {

        log.info(
                "Received cluster status update request from {} cluster: status {}, k8sVersion {}, "
                        + "nvcaVersion {}, gpuCount {}, allowClusterTargeting {}, "
                        + "allowTaskClusterCreationQueues {}, oidcIssuerPresent {}, jwksPresent {}",
                clusterId,
                nvcaRegistrationRequest.getStatus(),
                nvcaRegistrationRequest.getK8sVersion(),
                nvcaRegistrationRequest.getNvcaVersion(),
                nvcaRegistrationRequest.getGpus() == null ? 0 : nvcaRegistrationRequest.getGpus().size(),
                nvcaRegistrationRequest.getAllowClusterTargeting(),
                nvcaRegistrationRequest.getAllowTaskClusterCreationQueues(),
                StringUtils.isNotBlank(nvcaRegistrationRequest.getOidcIssuer()),
                StringUtils.isNotBlank(nvcaRegistrationRequest.getJwks()));

        // 1. Check if valid cluster-id provided
        ClusterEntity clusterInfo = validateClusterId(clusterId);
        ClusterEntity entityBefore = CopyUtil.deepCopy(clusterInfo);

        validateStatus(nvcaRegistrationRequest.getStatus());

        // If DynamicGpuDiscovery is disabled then providedGpus = existing GPUs
        Set<GpuV5Udt> providedGpus = getGpusFromRequest(nvcaRegistrationRequest, clusterInfo);
        Set<GpuV5Udt> existingGpus = getGpuFromEntity(clusterInfo);
        List<ClusterByGroupIdAndIdEntity> clusterByGroupIdList = new ArrayList<>();

        Set<GpuV5Udt> newlyAddedGpus = getNewlyAddedGpus(existingGpus, providedGpus);
        Set<String> removedGpuNames = getRemovedGpuNames(existingGpus, providedGpus);
        Set<String> removedInstanceTypes = getRemovedInstanceTypeName(existingGpus, providedGpus);

        logVerboseGpuConfigurationChange(clusterInfo, existingGpus, newlyAddedGpus, providedGpus, removedGpuNames, removedInstanceTypes);

        // Validate GPU name
        validateGpuName(!isSetEmptyOrNull(providedGpus) ? providedGpus : existingGpus);

        // Fetching allCluster info ONLY if needed
        if (!isSetEmptyOrNull(newlyAddedGpus) || !isSetEmptyOrNull(removedGpuNames)) {
            clusterByGroupIdList = clusterRepository.getClustersFromClusterGroup(
                    clusterInfo.getClusterGroupId());
        }

        // Validating newly added GPUs
        if (!isSetEmptyOrNull(newlyAddedGpus)) {
            validateNewlyAddedGpusWithClusterGroup(clusterInfo, newlyAddedGpus,
                                                   clusterByGroupIdList);

            // Validating single default GPU
            defaultGpuValidation(newlyAddedGpus);
        }

        // Validating removed instance-types
        if (!isSetEmptyOrNull(removedInstanceTypes)) {
            validateRemovedInstanceTypes(removedInstanceTypes, clusterId);
        }

        // Setting up clusterTargeting in clusterInfo
        setAllowTargetingField(clusterInfo, nvcaRegistrationRequest);

        // Setting up task creation queue flag in clusterInfo
        setAllowTaskClusterCreationQueues(clusterInfo, nvcaRegistrationRequest);

        // Generating creation queue for missing GPUs
        // Currently Air gapped == nats enabled
        // If nats is enabled then don't create creation queues
        // TODO: When we want to add NATS in AWS SIS at that time we need one more property from NVCA to know what NVCA is using
        if (!instanceServiceHelper.isNatsEnabled()) {

            // Generating creation queue for newly added GPUs
            createCreationQueues(clusterInfo, existingGpus, providedGpus);

            // Generating termination queue if not already generated
            createTerminationQueue(clusterInfo);

        }

        // Delete queues for removed GPUs
        // We should delete queue for removed GPUs after queue creation of newly added GPUs
        // this will make sure if there is any error in queue creation then we don't delete queue before returning the error
        if (!isSetEmptyOrNull(removedGpuNames)) {
            deleteQueueForRemovedGpus(removedGpuNames, clusterByGroupIdList, clusterInfo);
        }

        // Updating GPUs from NVCA
        if (!isSetEmptyOrNull(providedGpus)) {
            clusterInfo.setGpusV4(toGpusV4(providedGpus));
            clusterInfo.setGpusV5(providedGpus);
        }

        clusterInfo.setClusterStatus(nvcaRegistrationRequest.getStatus());
        clusterInfo.setK8sVersion(nvcaRegistrationRequest.getK8sVersion());
        clusterInfo.setNvcaLastConnected(instanceServiceHelper.getCurrentTimestamp());
        // If NVCA version is provided then update it in clusterInfo
        if (StringUtils.isNotBlank(nvcaRegistrationRequest.getNvcaVersion())) {
            clusterInfo.setNvcaVersion(nvcaRegistrationRequest.getNvcaVersion());
        }
        // If clusterSource is not set then set it to ngc-managed in registration
        if (StringUtils.isBlank(clusterInfo.getClusterSource())) {
            clusterInfo.setClusterSource(ClusterSource.NGC_MANAGED.toString());
        }

        applyOidcIdentityIfPresent(nvcaRegistrationRequest, clusterInfo, clusterId);

        // Generating response
        NvcaAccessCreds nvcaQueueAccessCreds =
                clusterQueueAccessCredsService.generateCredsForNvcaQueues(clusterInfo);

        updateEntityInDb(clusterInfo, entityBefore, auditProps);

        return NvcaRegistrationResponse.builder()
                .credentials(nvcaQueueAccessCreds)
                .clusterId(clusterId)
                .clusterGroupId(clusterInfo.getClusterGroupId())
                .build();
    }

    /**
     * @return true when OIDC/PSAT cluster identity is enabled in configuration.
     * Flag off means we never read or write cluster OIDC identity for
     * managed-NVCF behavior parity.
     */
    private boolean isOidcClusterIdentityEnabled() {
        return nvcaConfigurationProperties.isOidcClusterIdentityEnabled();
    }

    private void applyOidcIdentityIfPresent(
            NvcaRegistrationRequest nvcaRegistrationRequest,
            ClusterEntity clusterInfo,
            String clusterId) {
        if (!isOidcClusterIdentityEnabled()
                || StringUtils.isBlank(nvcaRegistrationRequest.getJwks())) {
            return;
        }

        String jwks = nvcaRegistrationRequest.getJwks();
        // Size is compared in UTF-8 bytes (not UTF-16 String chars) to match the
        // wire representation clients actually send and to keep the budget
        // accurate for non-ASCII payloads.
        if (jwks.getBytes(StandardCharsets.UTF_8).length > MAX_JWKS_SIZE_BYTES) {
            throw new IcmsBadRequestException(
                    String.format("JWKS payload exceeds maximum allowed size of %d bytes", MAX_JWKS_SIZE_BYTES));
        }

        try {
            JWKSet.parse(jwks);
        } catch (java.text.ParseException e) {
            throw new IcmsBadRequestException("Invalid JWKS format: " + e.getMessage());
        }

        String jwksFingerprint;
        try {
            jwksFingerprint = computeJwksFingerprint(jwks);
        } catch (ParseException e) {
            throw new IcmsBadRequestException("Invalid JWKS format: " + e.getMessage());
        }

        clusterOidcIdentityService.validateFingerprintAvailable(jwksFingerprint, clusterId);
        clusterOidcIdentityService.applyOidcIdentity(
                clusterInfo,
                jwks,
                StringUtils.isNotBlank(nvcaRegistrationRequest.getOidcIssuer())
                        ? nvcaRegistrationRequest.getOidcIssuer()
                        : null,
                jwksFingerprint);
    }

    private void setAllowTargetingField(ClusterEntity clusterInfo,
                                        NvcaRegistrationRequest nvcaRegistrationRequest) {
        clusterInfo.setAllowClusterTargeting(nvcaRegistrationRequest.getAllowClusterTargeting() == null
                                                     ? Boolean.FALSE
                                                     : nvcaRegistrationRequest.getAllowClusterTargeting());
    }

    private void setAllowTaskClusterCreationQueues(ClusterEntity clusterInfo,
                                        NvcaRegistrationRequest nvcaRegistrationRequest) {

        // If cluster doesn't have targeting enabled then no cluster specific queues will be used by NVCA
        // OR if task specific cluster creation queue is not enabled in SIS then set the flag as FALSE
        if (!nvcaConfigurationProperties.isTasksCreationQueuesEnabled() ||
                !isClusterTargetingEnabled(clusterInfo.getAllowClusterTargeting())) {
            clusterInfo.setAllowTaskClusterCreationQueues(Boolean.FALSE);
        } else {
            clusterInfo.setAllowTaskClusterCreationQueues(
                    nvcaRegistrationRequest.getAllowTaskClusterCreationQueues() == null
                            ? Boolean.FALSE
                            : nvcaRegistrationRequest.getAllowTaskClusterCreationQueues());}

    }

    /**
     * Creates or verifies the termination queue for the cluster.
     * 
     * This method implements a self-healing termination queue creation strategy that ensures
     * the required termination queue exists for the cluster. The termination queue is used
     * for handling cluster termination operations and is unique to each cluster.
     * 
     * Self-healing behavior:
     * - Only creates the queue if it doesn't already exist (based on terminationQueueUrl)
     * - Recovers from inconsistent states caused by partial failures or manual deletions
     * - Operations are idempotent - safe to run repeatedly
     * - If queue exists in AWS, creation is skipped; if missing, it's recreated
     * 
     * Queue configuration:
     * - Queue type: FIFO (First-In-First-Out) for ordered message processing
     * - Queue URL: Generated based on cluster ID for uniqueness
     * - Queue attributes: Updated during credential generation (not during creation)
     * 
     * This runs as part of the queue creation phase to ensure service availability:
     * - Termination queue is essential for proper cluster lifecycle management
     * - Missing termination queue can cause cluster termination failures
     * 
     * @param clusterEntity The cluster entity containing termination queue configuration
     */
    private void createTerminationQueue(@NotNull ClusterEntity clusterEntity) {
        String queueUrl = null;
        try {

            boolean isNew = (clusterEntity.getTerminationQueueUrl() == null || clusterEntity.getTerminationQueueUrl().isEmpty());
            queueUrl = getTerminationQueueUrl(clusterEntity.getClusterId());

            // Passing updateQueueAttributesIfQueueExists: false as we will update queue attribute for existing queue at the time of creds generation later in the API execution
            logQueueCreationDetails("createTerminationQueue", clusterEntity, queueUrl, isNew, QueueType.TERMINATION_QUEUE);
            String terminationQueueUrl =
                    clusterQueueAccessCredsService.createNvcaTerminationQueue(queueUrl, clusterEntity.getClusterId(), false);

            clusterEntity.setTerminationQueueType(String.valueOf(QueueAttributeName.FifoQueue));
            clusterEntity.setTerminationQueueUrl(terminationQueueUrl);

        } catch (Exception exception) {
            String errorContext = String.format("createTerminationQueue: Exception occurred while creating %s queue, " +
                            "clusterDetails: %s, queueUrl: %s, error: %s, exception: ",
                    TERMINATION_QUEUE,
                    logClusterDetails(clusterEntity),
                    queueUrl,
                    exception.getMessage());
            log.error(errorContext, exception);

            // rethrowing same exception
            throw exception;
        }
    }
    /**
     * Creates or verifies creation queues for all GPUs in the cluster.
     * 
     * This method implements a self-healing queue creation strategy that ensures all required
     * queues exist for the cluster's GPUs. It creates three types of queues:
     * 1. Cluster-group queues: Shared across all clusters in the same group
     * 2. Cluster-specific queues: Unique to this cluster (if targeting enabled)
     * 3. Task-specific queues: For task-based operations (if enabled)
     * 
     * Self-healing behavior:
     * - Always attempts to create queues for ALL provided GPUs (not just newly added ones)
     * - Recovers from inconsistent states caused by partial failures or manual deletions
     * - Operations are idempotent - safe to run repeatedly
     * - If queue exists in AWS, creation is skipped; if missing, it's recreated
     * 
     * This runs BEFORE deletion to ensure service availability:
     * - Extra queues are harmless (cleaned up later)
     * - Missing queues cause 500 errors and outages
     * 
     * @param clusterEntity The cluster entity containing queue configuration
     * @param existingGpus The GPUs currently configured in the database
     * @param providedGpus The GPUs provided in the registration request (may be empty)
     */
    // TODO: Discuss with team about creating a separate class for queue management needed in NVCA cluster registration
    private void createCreationQueues(
            @NotNull ClusterEntity clusterEntity,
            @NonNull Set<GpuV5Udt> existingGpus,
            @NonNull Set<GpuV5Udt> providedGpus) {

        // if provided GPUs are null then creating queue for already configured GUPs
        Set<GpuV5Udt> gpusToCreateQueue = existingGpus;
        if (!isSetEmptyOrNull(providedGpus)) {
            log.info("createCreationQueues: using provided GPUs to create queues {}", getGpuNameFromGpuV5(providedGpus));
            gpusToCreateQueue = providedGpus;
        }

        Map<String, CreationQueueUdt> creationQueueMap =
                clusterEntity.getCreationQueues() == null ? new HashMap<>() :
                        clusterEntity.getCreationQueues();

        Boolean isAutoTargetingEnabled = clusterEntity.getAllowClusterTargeting();
        Boolean isClusterCreationQueuesForTasksEnabled = clusterEntity.getAllowTaskClusterCreationQueues();

        Map<String, CreationQueueUdt> clusterCreationQueueMap = getClusterCreationQueue(clusterEntity,
                                                                                     isAutoTargetingEnabled);
        Map<String, CreationQueueUdt> clusterCreationQueueForTasksMap = getClusterCreationQueueForTasks(
                clusterEntity,
                isAutoTargetingEnabled);

        for (GpuV5Udt gpu : gpusToCreateQueue) {
            // Always attempt to create/verify cluster-group queues
            createQueueAndUpdateQueueMap(creationQueueMap, clusterEntity, gpu, QueueType.CLUSTER_GROUP);

            if (isClusterTargetingEnabled(isAutoTargetingEnabled)) {
                // Always attempt to create/verify cluster-specific queues
                createQueueAndUpdateQueueMap(clusterCreationQueueMap, clusterEntity, gpu, QueueType.CLUSTER_SPECIFIC);

                // Always attempt to create/verify task-specific queues if enabled
                if (instanceServiceHelper.isTaskClusterCreationQueuesAllowed(isClusterCreationQueuesForTasksEnabled)) {
                    createQueueAndUpdateQueueMap(clusterCreationQueueForTasksMap, clusterEntity, gpu, QueueType.TASK_SPECIFIC);
                }
            }
        }

        if (isClusterTargetingEnabled(isAutoTargetingEnabled)) {
            clusterEntity.setClusterCreationQueues(clusterCreationQueueMap);

            // Set task specific cluster creation key if targeting and tasks queues enabled
            if (instanceServiceHelper.isTaskClusterCreationQueuesAllowed(isClusterCreationQueuesForTasksEnabled)) {
                clusterEntity.setClusterCreationQueuesForTasks(clusterCreationQueueForTasksMap);
            }
        }
        clusterEntity.setCreationQueues(creationQueueMap);
    }

    /**
     * Creates or verifies creation queues for a specific GPU based on the queue type.
     * 
     * This unified method handles the creation and verification of all queue types:
     * - Cluster-group queues (shared across cluster group)
     * - Cluster-specific queues (unique to this cluster)
     * - Task-specific queues (for task-based operations)
     * 
     * Self-healing behavior:
     * - If queue exists in map but is missing from AWS, it will be recreated
     * - If queue exists in both map and AWS, creation is skipped (idempotent)
     * - If queue is missing from both, it will be created
     * 
     * @param queueMap The map containing the queues to update
     * @param clusterEntity The cluster entity containing configuration
     * @param gpu The GPU for which to create/verify the queue
     * @param queueType The type of queue being created (CLUSTER_GROUP, CLUSTER_SPECIFIC, TASK_SPECIFIC)
     */
    private void createQueueAndUpdateQueueMap(@NotNull Map<String, CreationQueueUdt> queueMap,
                                              @NotNull ClusterEntity clusterEntity,
                                              @NotNull GpuV5Udt gpu,
                                              @NotNull QueueType queueType) {
        
        String queueUrl = null;
        CreationQueueUdt creationQueue;
        String gpuName = gpu.getName();
        String clusterId = clusterEntity.getClusterId();
        String clusterGroupId = clusterEntity.getClusterGroupId();
        
        try {
            // Check if this is a new queue or existing one being verified
            boolean isNewQueue = !queueMap.containsKey(gpu.getName());

            creationQueue = switch (queueType) {
                case CLUSTER_GROUP -> {
                    queueUrl = getCreationQueueUrl(clusterGroupId, gpu.getName());
                    logQueueCreationDetails("createQueueAndUpdateQueueMap", clusterEntity, queueUrl, isNewQueue, queueType, gpuName);
                    yield buildNvcaFunctionCreationQueue(queueUrl, clusterId);
                }
                case CLUSTER_SPECIFIC -> {
                    queueUrl = getCreationQueueUrl(clusterId, gpu.getName());
                    logQueueCreationDetails("createQueueAndUpdateQueueMap", clusterEntity, queueUrl, isNewQueue, queueType, gpuName);
                    yield buildNvcaFunctionCreationQueue(queueUrl, clusterId);
                }
                case TASK_SPECIFIC -> {
                    queueUrl = getNvcaTasksCreationQueueUrl(clusterId, gpu.getName());
                    logQueueCreationDetails("createQueueAndUpdateQueueMap", clusterEntity, queueUrl, isNewQueue, queueType, gpuName);
                    yield buildNvcaTasksCreationQueue(queueUrl, clusterId);
                }

                // We have dedicated function for termination queue creation
                case TERMINATION_QUEUE -> null;
            };
            
            // Update the map with the queue info (creates or updates the entry)
            if (creationQueue != null) {
                queueMap.put(gpu.getName(), creationQueue);
            }
            
        } catch (Exception exception) {
            String errorContext = String.format("createQueueAndUpdateQueueMap: Exception occurred while creating %s queue, " +
                            "clusterDetails: %s, queueUrl: %s, error: %s, exception: ",
                    queueType.toString().toLowerCase(),
                    logClusterDetails(clusterEntity),
                    queueUrl,
                    exception.getMessage());
            log.error(errorContext, exception);
            throw exception;
        }
    }

    private Map<String, CreationQueueUdt> getClusterCreationQueue(ClusterEntity clusterEntity,
                                                               Boolean isAutoTargetingEnabled) {
        Map<String, CreationQueueUdt> clusterCreationQueueMap = new HashMap<>();
        if (isClusterTargetingEnabled(isAutoTargetingEnabled) && clusterEntity.getClusterCreationQueues() != null) {
            clusterCreationQueueMap = clusterEntity.getClusterCreationQueues();
        }
        return clusterCreationQueueMap;
    }

    private Map<String, CreationQueueUdt> getClusterCreationQueueForTasks(ClusterEntity clusterEntity,
                                                                  Boolean allowTaskClusterCreationQueues) {
        Map<String, CreationQueueUdt> clusterCreationQueueForTasksMap = new HashMap<>();
        if (instanceServiceHelper.isTaskClusterCreationQueuesAllowed(allowTaskClusterCreationQueues)
                && clusterEntity.getClusterCreationQueueForTasks() != null) {
            clusterCreationQueueForTasksMap = clusterEntity.getClusterCreationQueueForTasks();
        }
        return clusterCreationQueueForTasksMap;
    }

    // TODO: Create a helper class and move these function
    public static Set<GpuV5Udt> getNewlyAddedGpus(Set<GpuV5Udt> existingGpus, Set<GpuV5Udt> providedGpus) {
        if (isSetEmptyOrNull(providedGpus)) {
            return new HashSet<>();
        }
        HashSet<GpuV5Udt> copySet = new HashSet<>(providedGpus);
        copySet.removeAll(existingGpus);

        return copySet;
    }

    private boolean isDynamicGpuDiscoveryEnabled(ClusterEntity clusterEntity) {
        return clusterEntity.getCapabilities() != null &&
                clusterEntity.getCapabilities().contains(DYNAMIC_GPU_DISCOVERY.toString());
    }

    // authorizedNcaIds must be same if GPUs are shared between two clusters
    public void validateNewlyAddedGpusWithClusterGroup(
            ClusterEntity clusterInfo,
            Set<GpuV5Udt> newlyAddedGpus,
            List<ClusterByGroupIdAndIdEntity> clusterByGroupIdList) {
        /*
         1. Find all cluster from clusterGroup
         2. Check if any cluster has same GPUs as that of new registred cluster
         3. Validate if authorizedNcaId are same
         */
        for (ClusterByGroupIdAndIdEntity existingClusterInfo : clusterByGroupIdList) {

            // Same cluster will be ignored to compare authorized nca-id as it is being updated by NVCA
            if (!existingClusterInfo.getKey().getClusterId().equals(clusterInfo.getClusterId())) {

                Set<String> existingAuthorizedNcaId = existingClusterInfo.getAuthorizedNcaIds();
                String existingClusterName = existingClusterInfo.getClusterName();
                Set<GpuV5Udt> existingGpuV5 = getGpusV5(existingClusterInfo);
                String clusterGroupName = existingClusterInfo.getClusterGroupName();
                Set<String> registeredClusterAuthorizedNcaId = clusterInfo.getAuthorizedNcaIds();

                for (GpuV5Udt newlyAddedGpu : newlyAddedGpus) {
                    validateGpuWithExistingGpu(existingGpuV5,
                                               existingAuthorizedNcaId,
                                               registeredClusterAuthorizedNcaId, newlyAddedGpu,
                                               existingClusterName, clusterGroupName);
                }
            }
        }
    }

    /**
     * Deletes queues and removes queue references for GPUs that are being removed from a cluster.
     * 
     * This method handles the complex logic of queue deletion when GPUs are removed from a cluster,
     * ensuring that shared queues are protected while cluster-specific queues are always cleaned up.
     * 
     * Queue deletion strategy:
     * - Cluster-group queues: Only deleted if no other cluster in the group uses the GPU
     * - Cluster-specific queues: Always deleted for removed GPUs (they're unique to this cluster)
     * - Queue references: Always removed from the cluster's maps for all removed GPUs
     * 
     * Example scenario:
     * - ClusterGroup CG1 has clusters C1 and C2, both using GPU A100
     * - C1 removes A100: cluster-group queue is preserved (C2 needs it), but C1's specific queues are deleted
     * - C1's queue maps are updated to remove A100 references
     * 
     * @param removedGpuNames List of GPU names being removed from the cluster
     * @param clusterByGroupIdList All clusters in the same cluster group (used to check GPU sharing)
     * @param clusterInfo The cluster entity being updated
     */
    // TODO: Discuss with team about creating a separate class for queue management needed in NVCA cluster registration
    public void deleteQueueForRemovedGpus(
            @Nullable Set<String> removedGpuNames,
            @NotNull List<ClusterByGroupIdAndIdEntity> clusterByGroupIdList,
            @NotNull ClusterEntity clusterInfo) {

        /*
        Steps:
        1. Find active GPUs from all cluster having same cluster group
        2. Determine which shared cluster group queues can be deleted (not used by other clusters)
        3. Delete cluster-group queues only for GPUs not used by other clusters
        4. Delete function specific cluster queues for ALL removed GPUs
        5. Delete task specific cluster queues for ALL removed GPUs (they're unique to this cluster)
        6. Always remove queue references from cluster's maps
         */

        if (isSetEmptyOrNull(removedGpuNames) || instanceServiceHelper.isNatsEnabled()) {
            return;
        }

        // 1. Find active GPUs from all clusters in the same cluster group (excluding current cluster)
        Set<String> activeGpuName = new HashSet<>();
        for (ClusterByGroupIdAndIdEntity existingClusterInfo : clusterByGroupIdList) {

            // Same cluster will be ignored to find active GPUs as it is being updated by NVCA
            if (!existingClusterInfo.getKey().getClusterId().equals(clusterInfo.getClusterId())) {

                for (GpuV5Udt gpu : getGpusV5(existingClusterInfo)) {
                    activeGpuName.add(gpu.getName());
                }
            }
        }

        // 2. Determine which shared cluster group queues can be deleted (not used by other clusters)
        Set<String> gpusToDeleteSharedQueues = new HashSet<>(removedGpuNames);
        gpusToDeleteSharedQueues.removeAll(activeGpuName);

        // 3. Delete cluster-group queues only for GPUs not used by other clusters
        deleteCreationQueue(clusterInfo, gpusToDeleteSharedQueues, clusterInfo.getCreationQueues(), QueueType.CLUSTER_GROUP);

        if (isClusterTargetingEnabled(clusterInfo.getAllowClusterTargeting())) {

            // 4. Delete function specific cluster queues for ALL removed GPUs (they're unique to this cluster)
            deleteCreationQueue(clusterInfo, removedGpuNames, clusterInfo.getClusterCreationQueues(), QueueType.CLUSTER_SPECIFIC);

            // 5. Delete task specific cluster queues for ALL removed GPUs (they're unique to this cluster)
            if (instanceServiceHelper.isTaskClusterCreationQueuesAllowed(
                    clusterInfo.getAllowTaskClusterCreationQueues())) {
                deleteCreationQueue(clusterInfo, removedGpuNames, clusterInfo.getClusterCreationQueueForTasks(), QueueType.TASK_SPECIFIC);
            }
        }

        // 5. Always remove queue references from cluster's maps for ALL removed GPUs
        removeQueueReferencesFromMaps(clusterInfo, removedGpuNames);
    }

    public void validateRemovedInstanceTypes(
            Set<String> removedInstanceTypeNames,
            String clusterId) {

        if (removedInstanceTypeNames.isEmpty()) {
            return;
        }
        Set<String> activeInstanceIds = instanceServiceHelper.getActiveInstancesFromZoneForInstanceType(
                clusterId, removedInstanceTypeNames);
        if (!activeInstanceIds.isEmpty()) {
            String errMsg =
                    String.format(
                            "cluster registration failed, active instances exists for removed %s instanceTypes, active instances %s",
                            removedInstanceTypeNames, activeInstanceIds);
            log.error(
                    "Active instances exists for cluster-id {} with {} instanceTypes hence avoiding GPUs reconfiguration, active instances - {}",
                    clusterId, removedInstanceTypeNames, activeInstanceIds);
            throw new IcmsConflictException(errMsg);
        }
    }

    private void validateGpuWithExistingGpu(
            Set<GpuV5Udt> existingGpuV5,
            Set<String> existingAuthorizedNcaId,
            Set<String> registeredClusterAuthorizedNcaId,
            GpuV5Udt newlyAddedGpu,
            String existingClusterName,
            String clusterGroupName) {
        if (!isSetEmptyOrNull(existingGpuV5)) {

            for (GpuV5Udt existingGpu : existingGpuV5) {

                // AuthorizedNcaId must be same if GPUs are share between already present cluster
                if (existingGpu.getName().equals(newlyAddedGpu.getName())) {

                    if (!existingAuthorizedNcaId.containsAll(
                            registeredClusterAuthorizedNcaId)) {
                        String errMsg = String.format(
                                "%s GPU is already present in %s cluster from same %s clusterGroup but %s authorizedNcaId are not same",
                                newlyAddedGpu.getName(), existingClusterName, clusterGroupName,
                                registeredClusterAuthorizedNcaId);
                        log.error(errMsg);
                        throw new IcmsConflictException(errMsg);
                    }

                    // Validate instance types present inside the GPUs
                    ClusterRegistrationService.validateInstanceTypesV5(existingGpu.getInstanceTypes(),
                                                                       newlyAddedGpu.getInstanceTypes(),
                                                                       true, existingGpu.getName());

                }
            }
        }
    }

    private Set<GpuV5Udt> getGpuFromEntity(ClusterEntity clusterEntity) {
        if (isSetEmptyOrNull(getGpusV5(clusterEntity))) {
            return new HashSet<>();
        }
        return getGpusV5(clusterEntity);
    }

    /*
    GPUs from request will be considered if DynamicGpuDiscovery is enabled
     */
    private Set<GpuV5Udt> getGpusFromRequest(
            NvcaRegistrationRequest request,
            ClusterEntity clusterEntity) {

        if (isDynamicGpuDiscoveryEnabled(clusterEntity)) {
            if (!isSetEmptyOrNull(request.getGpus())) {
                return NvcaRequestSchemaToUdtConverter.toGpuV5Udts(request.getGpus());
            }
            String errMsg = "GPUs are not provided in the request";
            log.error("{} is enabled, error: {}", DYNAMIC_GPU_DISCOVERY, errMsg);
            throw new IcmsBadRequestException(errMsg);
        }

        // If DynamicGpuDiscovery is disabled then providedGpus = existing GPUs
        return getGpusV5(clusterEntity);
    }

    private ClusterEntity validateClusterId(@NotNull String clusterId) {

        // Passed clientId is already hashed if authentication is JWT based
        // Setting checkForHashedClusterId: false
        Optional<ClusterEntity> optionalClusterInfo =
                clusterRepository.getClusterInfoByClusterId(
                        clusterId, false);
        if (optionalClusterInfo.isEmpty()) {
            String errMsg = String.format("Cluster with clusterId %s doesn't exists", clusterId);
            log.error(errMsg);
            throw new IcmsNotFoundException(errMsg);
        }
        return optionalClusterInfo.get();
    }

    private String getTerminationQueueUrl(String clusterId) {
        String trimmedClusterId = clusterId;
        if (clusterId.startsWith(SSA_CLUSTER_ID_PREFIX)) {
            trimmedClusterId = clusterId.substring(10);
        }

        String url = String.format(nvcaConfigurationProperties.getTerminationQueueNameFormat(),
                                trimmedClusterId);
        return url;
    }

    private String getCreationQueueUrl(String clusterGroupId, String gpuName) {
        String url = String.format(nvcaConfigurationProperties.getCreationQueueNameFormat(),
                clusterGroupId, gpuName);
        return url;
    }

    private String getNvcaTasksCreationQueueUrl(String clusterId, String gpuName) {
        String truncatedClusterId = clusterId;
        // If clusterId is OAuth clientId then truncate it to match UUID length
        if (truncatedClusterId.startsWith(SSA_CLUSTER_ID_PREFIX)) {
            // Begin from where we will get 36 chars which is the UUID length
            truncatedClusterId = clusterId.substring(clusterId.length() - 36);
        }

        String url = String.format(nvcaConfigurationProperties.getTasksCreationQueueNameFormat(),
                                   truncatedClusterId, gpuName);
        return url;
    }

    private void sendTelemetryEvent(ClusterEntity clusterEntity) {
        try {
            Map<String, Object> metaData = getMetadataForCluster(clusterEntity,
                                                                 "nvcaClusterRegistered");
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withMetadata(metaData)
                                                               .withClusterId(clusterEntity.getClusterId())
                                                               .withClusterName(clusterEntity.getClusterName())
                                                               .withEventName(
                                                                       NVCA_CLUSTER_UPDATE.toString())));
        } catch (Exception e) {
            // Do not throw exceptions for telemetry failures
            log.warn(
                    "Error sending telemetry for the registration of cluster {}, with cluster group {}",
                    clusterEntity.getClusterName(), clusterEntity.getClusterGroupName());
        }
    }

    private void validateStatus(ClusterStatusEnum clusterStatus) {
        if (clusterStatus == null || !isClusterStatusAllowedToGenerateCreds(clusterStatus)) {
            String errMsg = String.format("Cluster status must be one of %s", List.of(
                    ClusterStatusEnum.READY, ClusterStatusEnum.CORDON, ClusterStatusEnum.CORDON_AND_DRAIN));

            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }
    }

    // If the cluster status is READY/CORDON/CORDON_AND_DRAIN then we will allow to generate credential for SQS queues from clusters
    private boolean isClusterStatusAllowedToGenerateCreds(@NotNull ClusterStatusEnum clusterStatus) {
        return clusterStatus.equals(ClusterStatusEnum.READY) || clusterStatus.equals(ClusterStatusEnum.CORDON)
                || clusterStatus.equals(ClusterStatusEnum.CORDON_AND_DRAIN);
    }

    private void updateEntityInDb(
            @NotNull ClusterEntity clusterInfo,
            @Nullable ClusterEntity entityBefore,
            @NotNull Map<String, Object> auditProps) {
        // Updating response in DB
        nvcaClusterRepository.updateClusterRegistration(clusterInfo);

        // Sending telemetry event
        sendTelemetryEvent(clusterInfo);

        // Sending audit event
        populateAuditValuesForUpdateCluster(auditProps, clusterInfo.getClusterId());
        auditService.sendAuditEventForClusterEntity(auditProps, entityBefore, clusterInfo);
    }

    public String setToJson(Set<?> set) {
        if (!isSetEmptyOrNull(set)) {
            return GsonCompatMapper.toJson(set);
        }
        return "[]";
    }

    /**
     * Deletes creation queues for removed GPUs based on the queue type.
     * 
     * This unified method handles the deletion of all queue types:
     * - Cluster-group queues (shared across cluster group)
     * - Cluster-specific queues (unique to this cluster)
     * - Task-specific queues (for task-based operations)
     * 
     * Note: Partial deletion failures are handled by the self-healing queue creation logic.
     * If some queues are deleted but operation fails, the next registration will recreate
     * any missing queues before attempting deletion again.
     * 
     * @param clusterInfo The cluster entity containing queue configuration
     * @param removedGpuNames The GPUs that were removed from the cluster
     * @param queueMap The map containing the queues to delete from
     * @param queueType The type of queue being deleted (CLUSTER_GROUP, CLUSTER_SPECIFIC, TASK_SPECIFIC)
     */
    private void deleteCreationQueue(
            ClusterEntity clusterInfo,
            Set<String> removedGpuNames,
            Map<String, CreationQueueUdt> queueMap,
            QueueType queueType) {

        if (queueMap == null) {
            return;
        }

        for (String gpuName : removedGpuNames) {
            if (queueMap.containsKey(gpuName)) {
                CreationQueueUdt creationQueue = queueMap.get(gpuName);
                String queueUrl = creationQueue.getUrl();

                try {
                    log.info("deleteCreationQueue: deleting {} queue for removed GPU, clusterDetails: {}, removedGpu: {}, queueUrl: {}",
                            queueType, logClusterDetails(clusterInfo), gpuName, queueUrl);

                    queueManager.deleteQueue(queueUrl);

                } catch (Exception exception) {
                    String errorContext = String.format("deleteCreationQueue: Exception occurred while deleting %s queue, " +
                                    "clusterDetails: %s, queueUrl: %s, error: %s, exception: ",
                            queueType.toString().toLowerCase(),
                            logClusterDetails(clusterInfo), 
                            queueUrl, 
                            exception.getMessage());
                    log.error(errorContext, exception);
                    throw exception;
                }
            }
        }
    }

    private CreationQueueUdt buildNvcaFunctionCreationQueue(String url, String clusterId) {
        // Passing updateQueueAttributesIfQueueExists: false as we will update queue attribute for existing queue at the time of creds generation later in the API execution
        String creationQueueUrl =
                clusterQueueAccessCredsService.createNvcaFunctionCreationQueue(url, clusterId, false);

        return CreationQueueUdt.builder()
                .queueType(String.valueOf(QueueAttributeName.FifoQueue))
                .url(creationQueueUrl)
                .build();
    }

    private CreationQueueUdt buildNvcaTasksCreationQueue(String url, String clusterId) {
        // Passing updateQueueAttributesIfQueueExists: false as we will update queue attribute for existing queue at the time of creds generation later in the API execution
        String creationQueueUrl =
                clusterQueueAccessCredsService.createNvcaTasksCreationQueue(url, clusterId, false);

        return CreationQueueUdt.builder()
                .queueType(String.valueOf(QueueAttributeName.FifoQueue))
                .url(creationQueueUrl)
                .build();
    }

    // TODO: Move this function in ClusterRegistrationService
    public CreationQueueUdt buildNonByocClusterCreationQueue(@NotNull String url, @NotNull String clusterId, boolean updateQueueAttributesIfQueueExists) {
        String creationQueueUrl =
                clusterQueueAccessCredsService.createNonByocClusterCreationQueue(url, clusterId, updateQueueAttributesIfQueueExists);

        return CreationQueueUdt.builder()
                .queueType(String.valueOf(QueueAttributeName.FifoQueue))
                .url(creationQueueUrl)
                .build();
    }

    /**
     * Removes queue references from all cluster maps for removed GPUs.
     * This ensures that queue credentials won't include GPUs that the cluster no longer has.
     *
     * @param clusterInfo    The cluster entity to update
     * @param removedGpuNames The GPUs that were removed from the cluster
     */
    private void removeQueueReferencesFromMaps(ClusterEntity clusterInfo, Set<String> removedGpuNames) {
        // Remove from cluster group creation queues map
        Map<String, CreationQueueUdt> creationQueues = clusterInfo.getCreationQueues();
        if (creationQueues != null) {
            for (String gpuName : removedGpuNames) {
                creationQueues.remove(gpuName);
            }
        }

        // Remove from cluster-specific creation queues map
        if (isClusterTargetingEnabled(clusterInfo.getAllowClusterTargeting())) {
            Map<String, CreationQueueUdt> clusterCreationQueues = clusterInfo.getClusterCreationQueues();
            if (clusterCreationQueues != null) {
                for (String gpuName : removedGpuNames) {
                    clusterCreationQueues.remove(gpuName);
                }
            }

            // Remove from task-specific cluster creation queues map
            if (instanceServiceHelper.isTaskClusterCreationQueuesAllowed(clusterInfo.getAllowTaskClusterCreationQueues())) {
                Map<String, CreationQueueUdt> taskQueues = clusterInfo.getClusterCreationQueueForTasks();
                if (taskQueues != null) {
                    for (String gpuName : removedGpuNames) {
                        taskQueues.remove(gpuName);
                    }
                }
            }
        }
    }

    private Set<String> getGpuNameFromGpuV5(Set<GpuV5Udt> gpuV5Udts) {
        Set<String> gpuNames = new HashSet<>();
        gpuV5Udts.forEach(gpuV5Udt -> gpuNames.add(gpuV5Udt.getName()));
        return gpuNames;
    }

    // Logs changed GPU configuration
    private void logVerboseGpuConfigurationChange(ClusterEntity clusterInfo,
                                                  Set<GpuV5Udt> existingGpus,
                                                  Set<GpuV5Udt> newlyAddedGpus,
                                                  Set<GpuV5Udt> providedGpus,
                                                  Set<String> removedGpuNames,
                                                  Set<String> removedInstanceTypes) {

        log.info(
                "logVerboseGpuConfigurationChange: For clusterId: {} , DynamicGPUDiscovery status: {}" +
                        " clusterName: {} from clusterGroupId: {} from clusterGroupName: {} ," +
                        " ExistingGPUs: {} , ProvidedGPUs: {} , newlyAddedGPUs: {} , RemovedGPUNames: {}, RemovedInstanceTypes : {}",
                clusterInfo.getClusterId(), isDynamicGpuDiscoveryEnabled(clusterInfo),
                clusterInfo.getClusterName(), clusterInfo.getClusterGroupId(),
                clusterInfo.getClusterGroupName(), setToJson(existingGpus),
                setToJson(providedGpus), setToJson(newlyAddedGpus),
                setToJson(removedGpuNames), setToJson(removedInstanceTypes));

        logGpuConfigurationChange(clusterInfo, existingGpus, newlyAddedGpus, providedGpus, removedGpuNames, removedInstanceTypes);
    }

    // Logs only changed GPU names not complete GPU configuration
    private void logGpuConfigurationChange(ClusterEntity clusterInfo,
                                           Set<GpuV5Udt> existingGpus,
                                           Set<GpuV5Udt> newlyAddedGpus,
                                           Set<GpuV5Udt> providedGpus,
                                           Set<String> removedGpuNames,
                                           Set<String> removedInstanceTypes) {

        log.info(
                "logGpuConfigurationChange: For clusterId: {} , DynamicGPUDiscovery status: {}" +
                        " clusterName: {} from clusterGroupId: {} from clusterGroupName: {} ," +
                        " ExistingGPUNames: {} , ProvidedGPUNames: {} , newlyAddedGPUNames: {} , RemovedGPUNames: {}, RemovedInstanceTypes : {}",
                clusterInfo.getClusterId(), isDynamicGpuDiscoveryEnabled(clusterInfo),
                clusterInfo.getClusterName(), clusterInfo.getClusterGroupId(),
                clusterInfo.getClusterGroupName(), setToJson(getGpuNameFromGpuV5(existingGpus)),
                setToJson(getGpuNameFromGpuV5(providedGpus)), setToJson(getGpuNameFromGpuV5(newlyAddedGpus)),
                setToJson(removedGpuNames), setToJson(removedInstanceTypes));
    }

    private String logClusterDetails(@NotNull ClusterEntity clusterEntity){
        return String.format("ClusterId: %s, clusterGroupId: %s", clusterEntity.getClusterId(), clusterEntity.getClusterGroupId());
    }

    private void logQueueCreationDetails(String action, ClusterEntity clusterEntity, String queueUrl, boolean isNewQueue, QueueType queueType, String gpuName) {
        log.info(
                "{}: {} {} Queue, GPU: {}, clusterDetails: {}, queueUrl: {}",
                action,
                isNewQueue ? "creating" : "verifying/re-creating",
                queueType,
                gpuName,
                logClusterDetails(clusterEntity),
                queueUrl);
    }

    private void logQueueCreationDetails(String action, ClusterEntity clusterEntity, String queueUrl, boolean isNewQueue, QueueType queueType) {
        log.info(
                "{}: {} {} Queue, clusterDetails: {}, queueUrl: {}",
                action,
                isNewQueue ? "creating" : "verifying/re-creating",
                queueType,
                logClusterDetails(clusterEntity),
                queueUrl);
    }
}
