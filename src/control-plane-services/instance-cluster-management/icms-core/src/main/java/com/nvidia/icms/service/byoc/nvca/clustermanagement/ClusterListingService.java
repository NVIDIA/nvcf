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

import static com.nvidia.icms.service.CloudHealthService.logIfStatusIsNull;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.node.ArrayNode;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterRegion;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.GpuResponseSchema;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.InstanceTypeResponseSchema;
import com.nvidia.icms.inbound.rest.model.nvca.ImageConfig;
import com.nvidia.icms.inbound.rest.model.nvca.ImageCredentialHelper;
import com.nvidia.icms.inbound.rest.model.nvca.SisConfig;
import com.nvidia.icms.inbound.rest.model.nvca.VaultConfig;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterConfigurationRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import jakarta.validation.constraints.NotNull;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Optional;
import java.util.Set;

import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@AllArgsConstructor
@Slf4j
public class ClusterListingService {

    public static final String CLUSTER_REGIONS_RESPONSE_FIELD_NAME = "clusterRegions";

    public static final String CLUSTER_SOURCES_RESPONSE_FIELD_NAME = "clusterSources";

    private final ClusterRepository clusterRepository;

    private final NvcaConfigurationProperties nvcaConfigurationProperties;

    private final CloudHealthService cloudHealthService;

    private final ObjectMapper objectMapper;

    private final NvcaClusterRepository nvcaClusterRepository;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    private final ComputePlatformService computePlatformService;

    @Observed
    public String getClusterVersion(String ncaId) {

        log.info("Received request for clusterVersion listing for {} ncaId", ncaId);
        String clusterVersionStr = nvcaConfigurationProperties.getClusterVersion();
        try {
            JsonNode clusterVersionNode = objectMapper.readTree(clusterVersionStr);

            // Set regions in response
            ArrayNode regionArrayNode = objectMapper.createArrayNode();
            for (ClusterRegion clusterRegion : ClusterRegion.values()) {
                regionArrayNode.add(clusterRegion.toString());
            }
            ((ObjectNode) clusterVersionNode).set(CLUSTER_REGIONS_RESPONSE_FIELD_NAME,
                                                  regionArrayNode);

            // Set clusterSource in response
            ArrayNode clusterSourceArrayNode = objectMapper.createArrayNode();
            for (ClusterSource clusterSource : ClusterSource.values()) {
                clusterSourceArrayNode.add(clusterSource.toString());
            }
            ((ObjectNode) clusterVersionNode).set(CLUSTER_SOURCES_RESPONSE_FIELD_NAME,
                                                  clusterSourceArrayNode);

            return objectMapper.writeValueAsString(clusterVersionNode);
        } catch (Exception exception) {
            String errMsg = String.format(
                    "Failed to append regions to cluster version and capabilities, error: %s",
                    exception.getMessage());
            log.error("error: {}, exception: ", errMsg, exception);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    /**
     * Lists the clusters for given ncaId
     *
     * @param ncaId                          Primary Nvidia Cloud Account Id
     * @param includeNonByocInAuthorizedClusters
     * @return All clusters available for given ncaId
     */

    @Observed
    public List<GetClusterResponse> getClustersByNcaId(
            String ncaId,
            Boolean includeAuthorizedClusters,
            Boolean includeNonByocInAuthorizedClusters) {
        try {
            List<GetClusterResponse> clusterResponseList = new ArrayList<>();

            if (Boolean.TRUE.equals(includeAuthorizedClusters)) {
                return getAllAuthorizedNvcaClustersForNcaId(ncaId, includeNonByocInAuthorizedClusters);
            }

            List<ClusterEntity> clusterEntities = clusterRepository.getAllClustersInAnAccount(
                    ncaId);

            for (ClusterEntity clusterEntity : clusterEntities) {

                // Validating cluster meant for NVCA2.0
                if (clusterEntity.getNvcaVersion() != null) {

                    GetClusterResponse clusterResponse = toGetClusterResponse(clusterEntity);
                    setClusterConfigurations(clusterResponse);
                    fetchAndUpdateClusterInfoFromCloudHealthEntity(clusterResponse,
                                                                   clusterEntity.getClusterStatus());
                    clusterResponseList.add(clusterResponse);
                }
            }

            return clusterResponseList;
        } catch (IcmsInternalServerException internalServerException) {
            log.error(
                    "Failed to fetch cluster info for ncaId {}, internalServer error: {}, exception: ",
                    ncaId, internalServerException.getBody().getDetail(), internalServerException);
            throw internalServerException;

        } catch (Exception exception) {
            String errMsg =
                    String.format("Failed to fetch cluster info for ncaId %s, error: %s", ncaId,
                                  exception.getMessage());
            log.error(errMsg, exception);

            throw new IcmsInternalServerException(errMsg);
        }
    }

    /**
     * Get Cluster for given ncaId and clusterId
     *
     * @param ncaId     Primary Nvidia Cloud Account Id
     * @param clusterId Cluster Id
     * @return Cluster created for given ncaId and clusterId
     */
    @Observed
    public GetClusterResponse getClusterByNcaIdAndClusterId(String ncaId, String clusterId) {

        // Validating clusterId
        // Setting checkForHashedClusterId: false because NGC UI will only pass valid clusterId
        // We are not doing validation with auth token
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        if (optionalClusterEntity.isEmpty()) {
            String errMsg = String.format("Can not find any cluster for %s ncaId and %s clusterId",
                                          ncaId, clusterId);
            log.error(errMsg);
            throw new IcmsNotFoundException(errMsg);
        }

        ClusterEntity clusterEntity = optionalClusterEntity.get();

        // Validating ncaId for clusterId
        if (!clusterEntity.getNcaId().equals(ncaId)) {
            String errMsg = String.format("%s ncaId doesn't exists for %s clusterId", ncaId,
                                          clusterId);
            log.error(errMsg);
            throw new IcmsConflictException(errMsg);
        }

        // Validating cluster meant for NVCA2.0
        validateClusterForNvca2Flow(clusterEntity);

        // Generating response
        GetClusterResponse clusterResponse = toGetClusterResponse(clusterEntity);
        // Populate advanced cluster configuration maps (only for GET-by-id)
        populateAdvancedClusterConfigurationMaps(clusterResponse, clusterId);
        setClusterConfigurations(clusterResponse);
        fetchAndUpdateClusterInfoFromCloudHealthEntity(clusterResponse,
                                                       clusterEntity.getClusterStatus());

        return clusterResponse;
    }

    private List<GetClusterResponse> getAllAuthorizedNvcaClustersForNcaId(
            String ncaId,
            Boolean includeNonByocInAuthorizedClusters) {
        // Get all authorized clusters for ncaId
        List<ClustersByAuthorizedAccountsEntity> allClustersInAuthorizedAccount =
                new ArrayList<>(nvcaClusterRepository.getAllClustersInAuthorizedAccount(ncaId));
        allClustersInAuthorizedAccount.addAll(
                nvcaClusterRepository.getAllClustersInAuthorizedAccount(
                        ClusterRepository.WILDCARD));

        List<GetClusterResponse> clusterResponseList = new ArrayList<>();
        for (ClustersByAuthorizedAccountsEntity authorizedClusterEntity : allClustersInAuthorizedAccount) {

            // Skip first-party platform clusters if includeNonByocInAuthorizedClusters=false
            if (computePlatformService.isPlatformCluster(authorizedClusterEntity.getClusterGroupName()) &&
                    !includeNonByocClustersInAuthorizedClusters(includeNonByocInAuthorizedClusters)) {
                continue;
            }

            // Get cluster entity from authorized ncaId
            Optional<ClusterEntity> optionalClusterEntity = clusterRepository.getClusterInfoByClusterId(
                    authorizedClusterEntity.getKey().getClusterId(), false);
            if (optionalClusterEntity.isPresent()) {
                ClusterEntity clusterEntity = optionalClusterEntity.get();

                // Validating cluster meant for NVCA2.0
                if (clusterEntity.getNvcaVersion() != null) {

                    GetClusterResponse clusterResponse = toGetClusterResponse(clusterEntity);
                    setClusterConfigurations(clusterResponse);
                    fetchAndUpdateClusterInfoFromCloudHealthEntity(clusterResponse,
                                                                   clusterEntity.getClusterStatus());
                    clusterResponseList.add(clusterResponse);
                }
            }
        }
        return clusterResponseList;
    }

    private SisConfig getSisConfig() {
        var sisConfig = nvcaConfigurationProperties.getSisConfig();
        return SisConfig.builder()
                .publicKeysetEndpoint(sisConfig.getPublicKeySetEndpoint())
                .spotServiceURL(sisConfig.getSpotServiceUrl())
                .tokenURL(sisConfig.getTokenUrl())
                .build();
    }


    private VaultConfig getVaultConfig() {
        var vaultConfig = nvcaConfigurationProperties.getVaultConfig();
        return VaultConfig.builder()
                .address(vaultConfig.getAddress())
                .build();
    }

    private ImageCredentialHelper getImageCredentialHelper() {
        var imageCredentialHelper = nvcaConfigurationProperties.getImageCredentialHelper();
        var imageConfig = imageCredentialHelper.getImageConfig();
        return ImageCredentialHelper.builder()
                .imageConfig(ImageConfig.builder()
                        .repository(imageConfig.getRepository())
                        .tag(imageConfig.getTag())
                        .build())
                .build();
    }

    private void setClusterConfigurations(GetClusterResponse clusterResponse) {
        clusterResponse.setSisConfig(getSisConfig());
        clusterResponse.setVaultConfig(getVaultConfig());
        clusterResponse.setImageCredentialHelper(getImageCredentialHelper());
    }

    private void populateAdvancedClusterConfigurationMaps(GetClusterResponse clusterResponse,
                                                          String clusterId) {
        nvcaClusterConfigurationRepository.findByClusterId(clusterId).ifPresent(configEntity -> {
            clusterResponse.setClusterConfigurations(configEntity.getClusterConfigurations());
            clusterResponse.setClusterConfigurationFiles(configEntity.getClusterConfigurationFiles());
        });
    }

    private GetClusterResponse toGetClusterResponse(ClusterEntity clusterEntity) {
        Set<String> customAttributes =
                clusterEntity.getCustomAttributes() != null ? clusterEntity.getCustomAttributes()
                        : new HashSet<>();
        return GetClusterResponse.builder()
                .clusterName(clusterEntity.getClusterName())
                .clusterGroupName(clusterEntity.getClusterGroupName())
                .clusterDescription(clusterEntity.getClusterDescription())
                .ncaId(clusterEntity.getNcaId())
                .authorizedNCAIds(clusterEntity.getAuthorizedNcaIds())
                .cloudProvider(clusterEntity.getClusterProvider())
                .region(StringUtils.toRootLowerCase(clusterEntity.getRegion()))
                .capabilities(clusterEntity.getCapabilities())
                .attributes(clusterEntity.getAttributes())
                .customAttributes(customAttributes)
                .gpus(toGpuRequestSchemas(NvcaConverter.getGpusV5(clusterEntity)))
                .nvcaVersion(clusterEntity.getNvcaVersion())
                .ssaClientId(clusterEntity.getAuthClientId())
                .oAuthClientId(clusterEntity.getAuthClientId())
                .clusterId(clusterEntity.getClusterId())
                .status(clusterEntity.getClusterStatus().toString())
                .clusterSource(clusterEntity.getClusterSource())
                .k8sVersion(clusterEntity.getK8sVersion())
                .clusterGroupId(clusterEntity.getClusterGroupId())
                .nvcaLastConnected(clusterEntity.getNvcaLastConnected())
                .clusterKeyId(clusterEntity.getClusterKeyId())
                .build();
    }

    private void validateClusterForNvca2Flow(ClusterEntity clusterEntity) {

        if (clusterEntity.getNvcaVersion() == null) {
            String errorMsg = String.format(
                    "The cluster with %s clusterId can not be listed since it was registered with NVCA 1.0 flow",
                    clusterEntity.getClusterId());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
    }

    private void fetchAndUpdateClusterInfoFromCloudHealthEntity(
            GetClusterResponse clusterResponse,
            ClusterStatusEnum clusterStatusEnum) {

        try {
            // Resolve the resource provider from configured compute platforms (config-driven);
            // BYOC clusters and any unconfigured provider map to ResourceProvider.BYOC.
            CloudProvider cloudProvider = CloudProvider.getCloudProviderFromClusterProvider(
                    clusterResponse.getCloudProvider());
            ResourceProvider resourceProvider = computePlatformService.resourceProviderFor(cloudProvider)
                    .orElse(ResourceProvider.BYOC);
            Optional<CloudHealthEntity> optionalCloudHealth =
                    cloudHealthService.getCloudHealth(resourceProvider, clusterResponse.getClusterId());

            // Setting gupUsage
            clusterResponse.setGpuUsage(
                    optionalCloudHealth.map(CloudHealthEntity::getGpuUsage).orElse(null));

            // Set cluster upgrade status
            clusterResponse.setClusterUpgradeStatus(
                    optionalCloudHealth.map(CloudHealthEntity::getClusterUpgradeStatus).orElse(null));

            // Overriding status based on heartbeat
            if (clusterStatusEnum.equals(ClusterStatusEnum.READY)) {

                optionalCloudHealth.ifPresent(entity -> logIfStatusIsNull(entity,
                                                                          "fetchAndUpdateClusterInfoFromCloudHealthEntity",
                                                                          clusterResponse.getClusterId()));

                if (optionalCloudHealth.isEmpty() ||
                        (optionalCloudHealth.get().getStatus() != null && !optionalCloudHealth.get()
                                .getStatus().equals(CloudHealthStatus.HEALTHY))) {
                    clusterResponse.setStatus(ClusterStatusEnum.UNHEALTHY.toString());
                }
            }
        } catch (Exception exception) {
            log.error(
                    "CLOUD_HEALTH_STATUS_LOGGING: function: fetchAndUpdateClusterInfoFromCloudHealthEntity, failed to fetch cloud health status, cluster-id {}, error: {} exception: ",
                    clusterResponse.getClusterId(), exception.getMessage(), exception);
            throw exception;
        }
    }

    private boolean includeNonByocClustersInAuthorizedClusters(Boolean includeNonByocInAuthorizedClusters) {
        if (includeNonByocInAuthorizedClusters == null) {
            return false;
        }
        return Boolean.TRUE.equals(includeNonByocInAuthorizedClusters);
    }

    private GpuResponseSchema toGpuRequestSchema(@NotNull GpuV5Udt gpuV5Udt) {
        return GpuResponseSchema.builder()
                .name(gpuV5Udt.getName())
                .capacity(gpuV5Udt.getCapacity())
                .instanceTypes(toInstanceTypeRequestSchemas(gpuV5Udt.getInstanceTypes()))
                .build();
    }

    private Set<GpuResponseSchema> toGpuRequestSchemas(Set<GpuV5Udt> gpuV5Udts) {
        if (gpuV5Udts == null) {
            return null;
        }

        Set<GpuResponseSchema> result = new HashSet<>();
        gpuV5Udts.forEach(r -> result.add(toGpuRequestSchema(r)));

        return result;
    }

    private InstanceTypeResponseSchema toInstanceTypeRequestSchema(
            @NotNull InstanceTypeV5Udt instanceTypeV5Udt) {
        return InstanceTypeResponseSchema.builder()
                .cpuCores(instanceTypeV5Udt.getCpuCores())
                .systemMemory(instanceTypeV5Udt.getSystemMemory())
                .gpuMemory(instanceTypeV5Udt.getGpuMemory())
                .gpuCount(instanceTypeV5Udt.getGpuCount())
                .name(instanceTypeV5Udt.getName())
                .description(instanceTypeV5Udt.getDescription())
                .isDefault(instanceTypeV5Udt.getIsDefault())
                .value(instanceTypeV5Udt.getValue())
                .cpuArch(instanceTypeV5Udt.getCpuArch())
                .os(instanceTypeV5Udt.getOs())
                .driverVersion(instanceTypeV5Udt.getDriverVersion())
                .storage(instanceTypeV5Udt.getStorage())
                .build();
    }

    private Set<InstanceTypeResponseSchema> toInstanceTypeRequestSchemas(
            Set<InstanceTypeV5Udt> instanceTypeV5Udts) {
        if (instanceTypeV5Udts == null) {
            return null;
        }

        Set<InstanceTypeResponseSchema> result = new HashSet<>();
        instanceTypeV5Udts.forEach(r -> result.add(toInstanceTypeRequestSchema(r)));

        return result;
    }
}
