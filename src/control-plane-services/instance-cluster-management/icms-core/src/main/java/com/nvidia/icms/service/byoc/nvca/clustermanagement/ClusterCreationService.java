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

import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.getGpusV5;
import static com.nvidia.icms.service.byoc.ClusterRegistrationService.validateAuthorizedNcaIdsForNewCluster;
import static com.nvidia.icms.service.byoc.ClusterRegistrationService.validateNcaId;
import static com.nvidia.icms.service.byoc.ClusterRegistrationService.validateNcaIdDoNotBelongsToAuthorizedNcaIds;
import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.MAX_JWKS_SIZE_BYTES;
import static com.nvidia.icms.util.AuthUtils.computeJwksFingerprint;
import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.validateGpuName;
import com.nimbusds.jose.jwk.JWKSet;
import java.nio.charset.StandardCharsets;
import java.text.ParseException;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.ALLOW_TASK_CLUSTER_CREATION_QUEUES;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.ATTRIBUTES;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CAPABILITIES;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_CREATION_QUEUES;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_GROUP_ID;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_GROUP_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_PROVIDER;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_REGISTRATION_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CREATION_QUEUE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.GPU;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.NVCA_LAST_CONNECTED;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.NVCA_VERSION;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REGION;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.AUTH_CLIENT_ID;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TASKS_CLUSTER_CREATION_QUEUE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.TERMINATION_QUEUE;
import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForRegisteringNewCluster;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.converters.NvcaRequestSchemaToUdtConverter;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterCapabilitiesEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationResponse;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterRegion;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterConfigurationRepository;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.byoc.ClusterRegistrationService;
import com.nvidia.icms.service.byoc.nvca.ClusterOidcIdentityService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.regex.Pattern;
import lombok.AllArgsConstructor;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ClusterCreationService {

    /*
    This regex is reffered from NVCFSPOT-1200
    One of the valid example of authorized NCA ID: JYdkpexyfPh6kAxtPc1Fa2dPr4jfFdlXLTzm1De0LFk
     */
    public static final String AUTHORIZED_NCA_ID_REGEX = "^[a-zA-Z0-9_-]{43}$";
    private static final Pattern AUTHORIZED_NCA_ID_PATTERN = Pattern.compile(AUTHORIZED_NCA_ID_REGEX);

    /*
     This pattern check following validations
     1. Only allows lowercase letters, digits, hyphens, and dots.
     2. Starts with a lowercase letter (a-z) or a digit (0-9).
     3. Allows hyphens (-) within segments, but not at the start or end of a segment.
     */
    private static final String RFC1123_SUBDOMAIN_REGEX = "[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*";

    private static final Pattern RFC1123_PATTERN = Pattern.compile(RFC1123_SUBDOMAIN_REGEX);

    private final ObjectMapper objectMapper;

    private final NvcaClusterRepository nvcaClusterRepository;

    private final ClusterRepository clusterRepository;

    private final TelemetryEventClient telemetryEventClient;

    private final AppAuditService auditService;
    
    private final NvcaConfigurationProperties nvcaConfigurationProperties;

    private final NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    private final ClusterOidcIdentityService clusterOidcIdentityService;

    /**
     * Validates authorized NCA IDs format.
     * Wildcard ("*") is treated as a special value and is not validated against the regex.
     */
    public static void validateAuthorizedNcaIdsFormat(@Nullable Set<String> authorizedNcaIds) {
        if (authorizedNcaIds == null) {
            return;
        }
        for (String authorizedNcaId : authorizedNcaIds) {
            if ("*".equals(authorizedNcaId)) {
                continue;
            }
            if (authorizedNcaId == null || !AUTHORIZED_NCA_ID_PATTERN.matcher(authorizedNcaId).matches()) {
                String errMsg = String.format("Invalid authorizedNCAIds '%s'. It must match regex %s",
                        authorizedNcaId,
                        AUTHORIZED_NCA_ID_REGEX);
                log.error(errMsg);
                throw new IcmsBadRequestException(errMsg);
            }
        }
    }

    public static String getClusterIdFromAuthClientId(@NotNull String authClientId) {
        return UUID.nameUUIDFromBytes(authClientId.getBytes()).toString();
    }

    public static void defaultGpuValidation(@NonNull Set<GpuV5Udt> gpus) {

        // Validating GPU name
        validateGpuName(gpus);

        for (GpuV5Udt gpu : gpus) {
            int defaultInstanceTypes = 0;
            for (InstanceTypeV5Udt instanceType : gpu.getInstanceTypes()) {
                if (Boolean.TRUE.equals(instanceType.getIsDefault())) {
                    defaultInstanceTypes++;
                }
            }
            if (defaultInstanceTypes != 1) {
                String errorMsg = String.format(
                        "There should be exactly one default instance type for each gpu, "
                                + "however gpu %s is having %d default instance types",
                        gpu.getName(), defaultInstanceTypes);
                log.error(errorMsg);
                throw new IcmsConflictException(errorMsg);
            }
        }
    }

    public static void validateRegion(String region) {
        for (ClusterRegion clusterRegion : ClusterRegion.values()) {
            if (clusterRegion.toString().equalsIgnoreCase(region)) {
                return;
            }
        }
        String errorMsg = String.format(
                "The provided %s region is not one of the supported regions", region);
        log.error(errorMsg);
        throw new IcmsBadRequestException(errorMsg);
    }

    public static String validateAndGetClusterSource(String clusterSource) {
        // If clusterSource is not provided then set it to "ngc-managed"
        if (StringUtils.isEmpty(clusterSource)) {
            return ClusterSource.NGC_MANAGED.toString();
        }

        // Validate if provided clusterSource is one of the known types
        for (ClusterSource sourceType : ClusterSource.values()) {
            if (sourceType.toString().equalsIgnoreCase(clusterSource)) {
                // If validated then return lower case clusterSource
                return clusterSource.toLowerCase();
            }
        }
        String errorMsg = String.format(
                "The provided %s cluster source is not one of the supported sources",
                clusterSource);
        log.error(errorMsg);
        throw new IcmsBadRequestException(errorMsg);
    }

    public static void validateAuthorizedNcaIdsForExternalCluster(@Nullable Set<String> actual,
                                                                  @Nullable String ssaClientId) {
        // External clusters can not be publicly accessible and authorized ncaId can not have * in it
        if (StringUtils.isEmpty(ssaClientId)) {
            if (actual != null && actual.contains("*")) {
                String errorMsg = "External clusters can not be publicly accessible";
                log.error(errorMsg);
                throw new IcmsConflictException(errorMsg);
            }
        }
    }

    public static void validateAuthorizedNcaIds(Set<String> expected, Set<String> actual) {
        String errorMsg;
        if (expected.size() == actual.size()) {
            Set<String> expectedNcaIds = new HashSet<>(actual);
            expectedNcaIds.removeAll(expected);
            if (expectedNcaIds.isEmpty()) {
                return;
            }
        }
        // If size is not same then nca-ids are different for clusters with shared gpus
        errorMsg =
                "Authorized nca ids are not matching with the authorized "
                        + "nca ids of the cluster having shared gpu(s)";
        log.error(errorMsg);
        throw new IcmsConflictException(errorMsg);
    }

    public static boolean isGpuSharedBetweenClusters(
            @NonNull Set<GpuV5Udt> providedGpus, @NonNull Set<GpuV5Udt> existingGpus) {
        for (GpuV5Udt gpu : providedGpus) {
            for (GpuV5Udt existingGpu : existingGpus) {
                if (gpu.getName().equals(existingGpu.getName())) {

                    // Validate is GPU is shared then instance types also needs to be same
                    ClusterRegistrationService.validateInstanceTypesV5(existingGpu.getInstanceTypes(), gpu.getInstanceTypes(),
                                                                       true, existingGpu.getName());
                    return true;
                }
            }
        }
        return false;
    }

    public static Map<String, Object> getMetadataForCluster(
            ClusterEntity clusterEntity, String clusterRegistrationStatus) {
        Map<String, Object> metaData = new HashMap<>();
        metaData.put(CLUSTER_GROUP_ID.getName(), clusterEntity.getClusterGroupId());
        metaData.put(CLUSTER_GROUP_NAME.getName(), clusterEntity.getClusterGroupName());
        metaData.put(CLUSTER_PROVIDER.getName(), clusterEntity.getClusterProvider());
        metaData.put(CLUSTER_STATUS.getName(), clusterEntity.getClusterStatus().toString());
        metaData.put(CLUSTER_REGISTRATION_STATUS.getName(), clusterRegistrationStatus);
        metaData.put(NVCA_VERSION.getName(), clusterEntity.getNvcaVersion());
        metaData.put(REGION.getName(), clusterEntity.getRegion());
        metaData.put(AUTH_CLIENT_ID.getName(), clusterEntity.getAuthClientId());
        metaData.put(TERMINATION_QUEUE.getName(), clusterEntity.getTerminationQueueUrl());
        metaData.put(TelemetryEventClient.EventMetaData.CLUSTER_REGISTERED_NCA_ID.getName(),
                     clusterEntity.getNcaId());
        metaData.put(ALLOW_TASK_CLUSTER_CREATION_QUEUES.getName(),
                     String.valueOf(clusterEntity.getAllowTaskClusterCreationQueues()));

        if (clusterEntity.getNvcaLastConnected() != null) {
            metaData.put(NVCA_LAST_CONNECTED.getName(), clusterEntity.getNvcaLastConnected().toString());
        }

        if (!isSetEmptyOrNull(clusterEntity.getAttributes())) {
            metaData.put(ATTRIBUTES.getName(), GsonCompatMapper.toJson(clusterEntity.getAttributes()));
        }

        if (!isSetEmptyOrNull(clusterEntity.getCapabilities())) {
            metaData.put(CAPABILITIES.getName(), GsonCompatMapper.toJson(clusterEntity.getCapabilities()));

        }
        if (!isSetEmptyOrNull(clusterEntity.getAuthorizedNcaIds())) {
            metaData.put(TelemetryEventClient.EventMetaData.CLUSTER_AUTHORIZED_NCA_ID.getName(),
                         clusterEntity.getAuthorizedNcaIds());
        }

        Set<GpuV5Udt> gpusV5 = getGpusV5(clusterEntity);
        if (!isSetEmptyOrNull(gpusV5)) {
            metaData.put(GPU.getName(), GsonCompatMapper.toJson(clusterEntity.getGpusV5()));
        }

        Map<String, CreationQueueUdt> gpuToCreationQueueMap =
                clusterEntity.getCreationQueues() == null ? new HashMap<>() :
                        clusterEntity.getCreationQueues();
        if (!gpuToCreationQueueMap.isEmpty()) {
            metaData.put(CREATION_QUEUE.getName(), GsonCompatMapper.toJson(gpuToCreationQueueMap));
        }

        Map<String, CreationQueueUdt> gpuToClusterCreationQueueMap =
                clusterEntity.getClusterCreationQueues() == null ? new HashMap<>() :
                        clusterEntity.getClusterCreationQueues();
        if (!gpuToClusterCreationQueueMap.isEmpty()) {
            metaData.put(CLUSTER_CREATION_QUEUES.getName(),
                         GsonCompatMapper.toJson(gpuToClusterCreationQueueMap));
        }

        Map<String, CreationQueueUdt> gpuToTasksClusterCreationQueueMap =
                clusterEntity.getClusterCreationQueuesForTasks() == null ? new HashMap<>() :
                        clusterEntity.getClusterCreationQueuesForTasks();
        if (!gpuToTasksClusterCreationQueueMap.isEmpty()) {
            metaData.put(TASKS_CLUSTER_CREATION_QUEUE.getName(),
                         GsonCompatMapper.toJson(gpuToTasksClusterCreationQueueMap));
        }

        return metaData;
    }

    @Observed
    public ClusterCreationResponse clusterCreation(
            ClusterCreationRequest clusterCreationRequest, String ncaId,
            Map<String, Object> auditProps) {

        logCreationRequestBody(clusterCreationRequest);

        // Validate clusterName syntax
        validateClusterNameSyntax(clusterCreationRequest.getClusterName());

        // Validate and error if cluster already exists for ncaId
        validateClusterNameForNcaId(clusterCreationRequest.getClusterName(), ncaId);

        // Validate nca-id in path is same as nca-id in body
        validateNcaIdInRequest(clusterCreationRequest.getNcaId(), ncaId);

        // Set default empty values
        if (clusterCreationRequest.getAuthorizedNCAIds() == null) {
            clusterCreationRequest.setAuthorizedNCAIds(new HashSet<>());
        }
        if (clusterCreationRequest.getGpus() == null) {
            clusterCreationRequest.setGpus(new HashSet<>());
        }
        if (clusterCreationRequest.getCapabilities() == null) {
            clusterCreationRequest.setCapabilities(new HashSet<>());
        }
        if (clusterCreationRequest.getAttributes() == null) {
            clusterCreationRequest.setAttributes(new HashSet<>());
        }
        if (clusterCreationRequest.getCustomAttributes() == null) {
            clusterCreationRequest.setCustomAttributes(new HashSet<>());
        }

        // Validate known attributes
        validateAttributes(clusterCreationRequest.getAttributes());

        // Validate region
        validateRegion(clusterCreationRequest.getRegion().toLowerCase());
        clusterCreationRequest.setRegion(clusterCreationRequest.getRegion().toLowerCase());

        // Validate and set clusterSource
        clusterCreationRequest.setClusterSource(
                validateAndGetClusterSource(clusterCreationRequest.getClusterSource()));

        // Check if provided cluster group is present for nca ID
        // If cluster group exists then create cluster inside cluster group
        Optional<ClusterGroupsByAccountEntity> optionalClusterGroupsByAccountEntity =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        ncaId,
                        clusterCreationRequest.getClusterGroupName());
        if (optionalClusterGroupsByAccountEntity.isPresent()) {
            log.info(
                    "Specified cluster group {} for cluster {} already exists, adding cluster to the same group",
                    clusterCreationRequest.getClusterGroupName(),
                    clusterCreationRequest.getClusterName());
            return createClusterInGroup(optionalClusterGroupsByAccountEntity.get(),
                                        clusterCreationRequest, ncaId, auditProps);
        }

        // If cluster group doesn't exist then create new cluster inside new cluster group
        return createNewClusterAndClusterGroup(clusterCreationRequest, auditProps);
    }

    public void validateAttributes(Set<String> attributes) {
        List<String> knownAttributes = nvcaConfigurationProperties.getClusterAttributes();
        for (String attribute: attributes) {
            boolean isValid = knownAttributes.contains(attribute);
            if (!isValid) {
                String errorMsg = String.format(
                        "The provided %s attribute is not a known attribute. Known attributes: %s", 
                        attribute, knownAttributes);
                log.error(errorMsg);
                throw new IcmsBadRequestException(errorMsg);
            }
        }
    }

    public ClusterEntity clusterCreationUpdateInDb(
            ClusterCreationRequest clusterCreationRequest,
            String clusterId, String clusterGroupId,
            Map<String, Object> auditProps,
            ClusterEntity existingClusterEntity) {
        ClusterEntity clusterEntity =
                toClusterEntity(clusterCreationRequest, clusterId, clusterGroupId);
        if (existingClusterEntity != null) {
            clusterEntity.setClusterStatus(existingClusterEntity.getClusterStatus());
            clusterEntity.setK8sVersion(existingClusterEntity.getK8sVersion());
            clusterEntity.setNvcaLastConnected(existingClusterEntity.getNvcaLastConnected());
        }
        populateOidcIfPresent(clusterCreationRequest, clusterEntity);

        // Save advanced BYOC cluster configuration maps if provided
        saveClusterConfigurationMapsIfPresent(clusterId, clusterCreationRequest);

        nvcaClusterRepository.saveClusterInfo(clusterEntity);

        // Audit log changes in DB
        populateAuditValuesForRegisteringNewCluster(auditProps, clusterId);
        auditService.sendAuditEventForClusterEntity(auditProps, new ClusterEntity(), clusterEntity);

        // Send telemetry
        sendTelemetryEvent(clusterEntity);

        return clusterEntity;
    }

    public void validateClusterNamingForLength(ClusterCreationRequest clusterCreationRequest) {
        if (clusterCreationRequest.getClusterName().length() > 32) {
            String errorMsg = String.format(
                    "The cluster name %s exceeds the limit of 32 chars",
                    clusterCreationRequest.getClusterName());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
        if (clusterCreationRequest.getClusterGroupName().length() > 32) {
            String errorMsg = String.format(
                    "The cluster group name %s exceeds the limit of 32 chars",
                    clusterCreationRequest.getClusterGroupName());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
        if (clusterCreationRequest.getClusterDescription() != null &&
                clusterCreationRequest.getClusterDescription().length() > 32) {
            String errorMsg = String.format(
                    "The cluster description %s exceeds the limit of 32 chars",
                    clusterCreationRequest.getClusterDescription());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
    }

    public void validateClusterGroupForNvca2Flow(ClusterGroupsByAccountEntity clusterGroupEntity) {

        if (clusterGroupEntity.getGpus() != null && !clusterGroupEntity.getGpus().isEmpty()) {
            String errorMsg = String.format(
                    "This cluster group %s can not be used since it was already registered with old flow",
                    clusterGroupEntity.getKey().getClusterGroupName());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
    }

    // With this we should validate that we have all the required values and for each gpu there is
    // exactly one default instance type
    public void validateGpusForNewCluster(@NonNull Set<GpuV5Udt> gpus, Set<String> capabilities) {
        String errorMsg;
        // If dynamic gpu discovery is present and gpus are not empty then throw error
        if (!gpus.isEmpty() &&
                isDynamicGpuDiscoveryCapabilityPresent(capabilities)) {
            errorMsg = String.format(
                    "Cluster capabilities contains %s so gpus must not be provided",
                    ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY);
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
        // If dynamic gpu discovery is not present and gpus are empty then throw error
        if (gpus.isEmpty() &&
                !isDynamicGpuDiscoveryCapabilityPresent(capabilities)) {
            errorMsg = String.format(
                    "Cluster capabilities does not contain %s so gpus must be provided",
                    ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY);
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }

        defaultGpuValidation(gpus);
    }

    public void validateClusterProvider(
            String clusterGroupId,
            ClusterProviderEnum providedValue) {
        Set<ClusterEntity> clusterEntitySet =
                clusterRepository.getAllClustersInAGroup(clusterGroupId);
        for (ClusterEntity clusterEntity : clusterEntitySet) {
            if (!clusterEntity.getClusterProvider().equals(providedValue)) {
                log.error(
                        "Specified cloudProvider {} is different than cloudProvider of clusterGroup {}",
                        providedValue, clusterEntity.getClusterProvider());
                throw new IcmsConflictException(String.format(
                        "Specified cloudProvider %s is different than cloudProvider of clusterGroup",
                        providedValue));
            }
        }
    }

    private void logCreationRequestBody(ClusterCreationRequest clusterCreationRequest) {
        log.info(
                "Cluster creation request: clusterName {}, clusterGroupName {}, ncaId {}, "
                        + "cloudProvider {}, region {}, nvcaVersion {}, capabilityCount {}, "
                        + "attributeCount {}, customAttributeCount {}, gpuCount {}, "
                        + "clusterConfigurationsPresent {}, clusterConfigurationFilesPresent {}, "
                        + "oidcIssuerPresent {}, jwksPresent {}",
                clusterCreationRequest.getClusterName(),
                clusterCreationRequest.getClusterGroupName(),
                clusterCreationRequest.getNcaId(),
                clusterCreationRequest.getCloudProvider(),
                clusterCreationRequest.getRegion(),
                clusterCreationRequest.getNvcaVersion(),
                clusterCreationRequest.getCapabilities() == null ? 0 : clusterCreationRequest.getCapabilities().size(),
                clusterCreationRequest.getAttributes() == null ? 0 : clusterCreationRequest.getAttributes().size(),
                clusterCreationRequest.getCustomAttributes() == null ? 0
                        : clusterCreationRequest.getCustomAttributes().size(),
                clusterCreationRequest.getGpus() == null ? 0 : clusterCreationRequest.getGpus().size(),
                clusterCreationRequest.getClusterConfigurations() != null,
                clusterCreationRequest.getClusterConfigurationFiles() != null,
                StringUtils.isNotBlank(clusterCreationRequest.getOidcIssuer()),
                StringUtils.isNotBlank(clusterCreationRequest.getJwks()));
    }

    private ClusterCreationResponse createNewClusterAndClusterGroup(
            ClusterCreationRequest clusterCreationRequest, Map<String, Object> auditProps) {

        // Validate for 32 char length for cluster attributes
        validateClusterNamingForLength(clusterCreationRequest);

        // For JWT, we will get client ID in request which needs to be used as clusterId
        // If JWT is not provided then generate UUID as clusterId for supporting ApiKey
        String clusterId = validateForClusterId(clusterCreationRequest);

        // Validate nca id is not part of authorized nca ids
        validateNcaIdDoNotBelongsToAuthorizedNcaIds(clusterCreationRequest.getAuthorizedNCAIds(),
                                                    clusterCreationRequest.getNcaId());

        // Validate authorized nca id for * and empty strings
        validateAuthorizedNcaIdsForNewCluster(clusterCreationRequest.getAuthorizedNCAIds());
        if (nvcaConfigurationProperties.isAuthorizedNcaIdRegexValidationEnabled()) {
            validateAuthorizedNcaIdsFormat(clusterCreationRequest.getAuthorizedNCAIds());
        }

        // Validate authorized nca id for external clusters
        validateAuthorizedNcaIdsForExternalCluster(
                clusterCreationRequest.getAuthorizedNCAIds(),
                clusterCreationRequest.getEffectiveOAuthClientId());

        // Validate gpus based on capability and also for single default instance types
        Set<GpuV5Udt> requestedGpus = NvcaRequestSchemaToUdtConverter.toGpuV5Udts(clusterCreationRequest.getGpus());
        validateGpusForNewCluster(requestedGpus,
                                  clusterCreationRequest.getCapabilities());

        String clusterGroupId = generateRandomUUID();

        // Perform cluster creation operations
        clusterCreationUpdateInDb(clusterCreationRequest, clusterId, clusterGroupId, auditProps,
                                  null);

        return ClusterCreationResponse.builder()
                .clusterId(clusterId)
                .clusterGroupId(clusterGroupId)
                .build();
    }

    private void validateNcaIdInRequest(String expectedNcaId, String actualNcaId) {
        if (!expectedNcaId.equals(actualNcaId)) {
            String errorMsg = String.format(
                    "The provided %s nca-id in request does not match %s nca-id provided in path",
                    actualNcaId, expectedNcaId);
            log.error(errorMsg);
            throw new IcmsBadRequestException(errorMsg);
        }
    }

    private ClusterCreationResponse createClusterInGroup(
            ClusterGroupsByAccountEntity clusterGroupEntity,
            ClusterCreationRequest clusterCreationRequest,
            String ncaId, Map<String, Object> auditProps) {
        // New cluster can not be added to cluster group registered in old flow
        validateClusterGroupForNvca2Flow(clusterGroupEntity);

        // Validate NcaId of cluster to be added in cluster group
        // Nca ID of cluster should be same as ncaId of cluster group
        validateNcaId(clusterGroupEntity.getKey().getNcaId(), ncaId);

        // For JWT we will get client ID in request which needs to be used as clusterId
        // If JWT is not provided then generate UUID as clusterId for supporting ApiKey
        String clusterId = validateForClusterId(clusterCreationRequest);

        // validate cloud provider is same as cluster provider of cluster group
        validateClusterProvider(clusterGroupEntity.getClusterGroupId(),
                                clusterCreationRequest.getCloudProvider());

        // Validate nca id is not part of authorized nca ids
        validateNcaIdDoNotBelongsToAuthorizedNcaIds(clusterCreationRequest.getAuthorizedNCAIds(),
                                                    ncaId);

        // Validate authorized nca id for * and empty strings
        validateAuthorizedNcaIdsForNewCluster(clusterCreationRequest.getAuthorizedNCAIds());
        if (nvcaConfigurationProperties.isAuthorizedNcaIdRegexValidationEnabled()) {
            validateAuthorizedNcaIdsFormat(clusterCreationRequest.getAuthorizedNCAIds());
        }

        // Validate gpus based on capability and also for single default instance types
        Set<GpuV5Udt> requestedGpus = NvcaRequestSchemaToUdtConverter.toGpuV5Udts(clusterCreationRequest.getGpus());
        validateGpusForNewCluster(requestedGpus,
                                  clusterCreationRequest.getCapabilities());

        // Validate if gpus are shared between clusters then their nca-ids should be same
        validateAuthorizedNcaIdsAgainstExistingClusters(clusterGroupEntity, clusterCreationRequest);

        ClusterEntity clusterEntity =
                toClusterEntity(clusterCreationRequest, clusterId,
                                clusterGroupEntity.getClusterGroupId());
        populateOidcIfPresent(clusterCreationRequest, clusterEntity);

        // Save advanced BYOC cluster configuration maps if provided
        saveClusterConfigurationMapsIfPresent(clusterId, clusterCreationRequest);

        nvcaClusterRepository.saveClusterInfo(clusterEntity);

        // Audit log changes in DB
        populateAuditValuesForRegisteringNewCluster(auditProps, clusterId);
        auditService.sendAuditEventForClusterEntity(auditProps, new ClusterEntity(), clusterEntity);

        // Send telemetry
        sendTelemetryEvent(clusterEntity);

        return ClusterCreationResponse.builder()
                .clusterId(clusterId)
                .clusterGroupId(clusterGroupEntity.getClusterGroupId())
                .build();
    }

    private void saveClusterConfigurationMapsIfPresent(
            @NotNull String clusterId,
            @NotNull ClusterCreationRequest clusterCreationRequest) {
        Map<String, String> config = clusterCreationRequest.getClusterConfigurations();
        Map<String, String> files = clusterCreationRequest.getClusterConfigurationFiles();

        boolean hasConfig = config != null && !config.isEmpty();
        boolean hasFiles = files != null && !files.isEmpty();
        if (!hasConfig && !hasFiles) {
            return;
        }

        nvcaClusterConfigurationRepository.saveOrUpdateConfiguration(
                clusterId,
                hasConfig ? config : null,
                hasFiles ? files : null
        );
    }

    private void validateAuthorizedNcaIdsAgainstExistingClusters(
            ClusterGroupsByAccountEntity clusterGroupEntity,
            ClusterCreationRequest clusterCreationRequest) {
        // Fetch clusters from cluster group and
        // validate if gpus are shared, then authorized nca ids should be same
        Set<ClusterEntity> clusterEntities =
                clusterRepository.getAllClustersInAGroup(clusterGroupEntity.getClusterGroupId());
        Set<GpuV5Udt> providedGpus = NvcaRequestSchemaToUdtConverter.toGpuV5Udts(clusterCreationRequest.getGpus());
        for (ClusterEntity clusterEntity : clusterEntities) {
            Set<GpuV5Udt> existingGpus = getGpusV5(clusterEntity);
            boolean isGpuShared = isGpuSharedBetweenClusters(providedGpus, existingGpus);
            if (isGpuShared) {
                //Generate authorized nca ID key set with ncaId
                Set<String> existingAuthNcaIds = new HashSet<>(clusterEntity.getAuthorizedNcaIds());
                existingAuthNcaIds.add(clusterEntity.getNcaId());

                Set<String> providedAuthNcaIds;
                if (clusterCreationRequest.getAuthorizedNCAIds() != null) {
                    providedAuthNcaIds = new HashSet<>(clusterCreationRequest.getAuthorizedNCAIds());
                }
                else {
                    providedAuthNcaIds = new HashSet<>();
                }
                providedAuthNcaIds.add(clusterCreationRequest.getNcaId());

                // Validate authorized nca_ids (including primary nca_id) are same
                validateAuthorizedNcaIds(existingAuthNcaIds,
                                         providedAuthNcaIds);
            }
        }
    }

    /**
     * @return true when OIDC/PSAT cluster identity is enabled in configuration.
     * When false, we do not persist cluster OIDC identity even if the request
     * carried jwks/oidcIssuer, matching legacy managed-NVCF behavior.
     */
    private boolean isOidcClusterIdentityEnabled() {
        return nvcaConfigurationProperties.isOidcClusterIdentityEnabled();
    }

    private ClusterEntity toClusterEntity(
            ClusterCreationRequest clusterCreationRequest,
            String clusterId, String clusterGroupId) {
        try {
            return ClusterEntity.builder()
                    .clusterName(clusterCreationRequest.getClusterName())
                    .clusterId(clusterId)
                    .ncaId(clusterCreationRequest.getNcaId())
                    .clusterDescription(clusterCreationRequest.getClusterDescription())
                    .clusterGroupName(clusterCreationRequest.getClusterGroupName())
                    .clusterGroupId(clusterGroupId)
                    .clusterProvider(clusterCreationRequest.getCloudProvider())
                    .clusterStatus(ClusterStatusEnum.NOT_READY)
                    .gpusV5(NvcaRequestSchemaToUdtConverter.toGpuV5Udts(clusterCreationRequest.getGpus()))
                    .gpusV4(NvcaRequestSchemaToUdtConverter.toGpuV4Udts(clusterCreationRequest.getGpus()))
                    .authorizedNcaIds(clusterCreationRequest.getAuthorizedNCAIds())
                    .capabilities(clusterCreationRequest.getCapabilities())
                    .attributes(clusterCreationRequest.getAttributes())
                    .authClientId(clusterCreationRequest.getEffectiveOAuthClientId())
                    .region(clusterCreationRequest.getRegion())
                    .nvcaVersion(clusterCreationRequest.getNvcaVersion())
                    .clusterSource(clusterCreationRequest.getClusterSource())
                    .creationQueues(new HashMap<>())
                    .clusterCreationQueues(new HashMap<>())
                    .clusterCreationQueuesForTasks(new HashMap<>())
                    .customAttributes(clusterCreationRequest.getCustomAttributes())
                    .allowClusterTargeting(Boolean.FALSE)
                    .allowTaskClusterCreationQueues(Boolean.FALSE)
                    .requestDump(objectMapper.writeValueAsString(clusterCreationRequest))
                    .registrationTime(Instant.now())
                    .clusterKeyId(clusterCreationRequest.getClusterKeyId())
                    .build();
        } catch (JacksonException jsonProcessingException) {
            String errorMsg = String.format(
                    "Json Processing exception when forming cluster entity from registration request for cluster: %s, error: %s.",
                    clusterCreationRequest.getClusterName(), jsonProcessingException.getMessage());
            log.error(errorMsg, jsonProcessingException);
            throw new IcmsInternalServerException(errorMsg);
        }
    }

    /**
     * Flag-gated: when OIDC/PSAT cluster identity is enabled and the request
     * carries a JWKS, validate it and attach the OIDC identity to the
     * cluster_by_cluster_id row before the cluster is saved.
     */
    private void populateOidcIfPresent(ClusterCreationRequest request, ClusterEntity clusterEntity) {
        if (!isOidcClusterIdentityEnabled()) {
            return;
        }
        String jwks = request.getJwks();
        if (StringUtils.isBlank(jwks)) {
            return;
        }
        // Enforce size budget in UTF-8 bytes (not UTF-16 String chars) so the
        // check matches the wire payload and is accurate for non-ASCII input.
        if (jwks.getBytes(StandardCharsets.UTF_8).length > MAX_JWKS_SIZE_BYTES) {
            throw new IcmsBadRequestException(
                    String.format("JWKS payload exceeds maximum allowed size of %d bytes",
                            MAX_JWKS_SIZE_BYTES));
        }
        // Validate JWKS parses as a well-formed JWK Set before we persist.
        // computeJwksFingerprint below also parses, but doing the parse here
        // makes the intent explicit and keeps the error message tight.
        try {
            JWKSet.parse(jwks);
        } catch (ParseException e) {
            throw new IcmsBadRequestException("Invalid JWKS format: " + e.getMessage());
        }
        String fingerprint;
        try {
            fingerprint = computeJwksFingerprint(jwks);
        } catch (ParseException e) {
            throw new IcmsBadRequestException("Invalid JWKS format: " + e.getMessage());
        }
        clusterOidcIdentityService.validateFingerprintAvailable(fingerprint, clusterEntity.getClusterId());
        clusterOidcIdentityService.applyOidcIdentity(
                clusterEntity,
                jwks,
                request.getOidcIssuer(),
                fingerprint);
    }

    private boolean isDynamicGpuDiscoveryCapabilityPresent(Set<String> capabilities) {
        if (capabilities == null) {
            return false;
        }
        return capabilities.contains(ClusterCapabilitiesEnum.DYNAMIC_GPU_DISCOVERY.toString());
    }

    private String validateForClusterId(ClusterCreationRequest clusterCreationRequest) {
        String effectiveClientId = clusterCreationRequest.getEffectiveOAuthClientId();
        // If client ID is not given then generate UUID and send it back
        if (StringUtils.isEmpty(effectiveClientId)) {
            var generatedUUID = UUID.randomUUID().toString();
            log.info("Generated {} clusterId for ApiKey flow for {} clusterName in {} clusterGroup",
                     generatedUUID, clusterCreationRequest.getClusterName(),
                     clusterCreationRequest.getClusterGroupName());
            return generatedUUID;
        }

        // Check if provided client ID is already registered with another cluster or not
        var optionalClusterInfo =
                clusterRepository.getClusterInfoByClusterId(effectiveClientId, true);
        if (optionalClusterInfo.isPresent()) {

            String errMsg = String.format("Provided %s client ID is already registered with "
                                                  + "another cluster",
                                          effectiveClientId);
            throw new IcmsConflictException(errMsg);
        }

        // Return cluster ID derived from the registered OAuth client ID
        var clusterIdFromAuthClientId = getClusterIdFromAuthClientId(effectiveClientId);
        log.info(
                "Generated {} clusterId for OAuth flow for {} authClientId, {} clusterName in {} clusterGroup",
                clusterIdFromAuthClientId, effectiveClientId,
                clusterCreationRequest.getClusterName(),
                clusterCreationRequest.getClusterGroupName());

        return clusterIdFromAuthClientId;
    }

    private void validateClusterNameForNcaId(String clusterName, String ncaId) {
        var optionalClusterInfo =
                clusterRepository.getClusterByAccountAndName(ncaId, clusterName);
        if (optionalClusterInfo.isPresent()) {
            String errMsg = String.format("Cluster with clusterName %s already exists",
                                          clusterName);
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }
    }

    private void sendTelemetryEvent(ClusterEntity clusterEntity) {
        try {
            Map<String, Object> metaData = getMetadataForCluster(clusterEntity,
                                                                 "nvcaClusterCreated");
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withMetadata(metaData)
                                                               .withClusterId(clusterEntity.getClusterId())
                                                               .withClusterName(clusterEntity.getClusterName())
                                                               .withEventName(
                                                                       Events.NVCA_CLUSTER_CREATED.toString())));
        } catch (Exception e) {
            // Do not throw exceptions for telemetry failures
            log.warn(
                    "Error sending telemetry for the creation of cluster {}, with cluster group {}",
                    clusterEntity.getClusterName(), clusterEntity.getClusterGroupName());
        }
    }

    private void validateClusterNameSyntax(String clusterName) {
        if (!RFC1123_PATTERN.matcher(clusterName).matches()) {
            String errMsg =
                    String.format("Provided clusterName %s is not with RFC1123 subdomain format %s", clusterName,
                            RFC1123_SUBDOMAIN_REGEX);
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }
    }
}
