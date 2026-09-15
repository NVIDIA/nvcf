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

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterRegion;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationResponse;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterConfigurationRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterConfigurationByClusterIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.util.TestUtil;

import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository.WILDCARD;
import static com.nvidia.icms.service.platform.ComputePlatformTestFixtures.PLATFORM_CLUSTER_DESCRIPTION;
import static com.nvidia.icms.service.platform.ComputePlatformTestFixtures.PLATFORM_CLUSTER_GROUP_ID;
import static com.nvidia.icms.service.platform.ComputePlatformTestFixtures.PLATFORM_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_AUTHORIZED_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CREATION_QUEUE_URL;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_ZONE_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.buildGpuUdts;
import static com.nvidia.icms.util.TestUtil.getDummyClustersByAuthorizedAccountResp;
import static com.nvidia.icms.util.TestUtil.getDummyGpuV4;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.icms.errors.IcmsInternalServerException;
import org.mockito.ArgumentMatchers;

@ExtendWith(MockitoExtension.class)
class ClusterListingServiceTest {

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    private NvcaConfigurationProperties nvcaConfigurationProperties;

    @Mock
    private CloudHealthService cloudHealthService;

    @Mock
    private NvcaClusterRepository nvcaClusterRepository;

    @Mock
    private ObjectMapper objectMapper;

    @Mock
    NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    @Mock
    private ComputePlatformService computePlatformService;

    @InjectMocks
    private ClusterListingService clusterListingService;

    @Test
    void getClustersByNcaId_withValidInputs_returnsSuccess() {

        // Prepare
        when(clusterRepository.getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID)).thenReturn(
                List.of(TestUtil.getDummyClusterEntity()));

        // Mock nested configuration objects
        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);
        
        // Mock imageCredentialHelper configuration
        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("test_image_cred_repo");
        mockImageConfig.setTag("test_image_cred_tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);
        
        Map<String, GpuCapacity> gpuCapacityMap = new HashMap<>();
        GpuCapacity gpuCapacity = GpuCapacity.builder()
                .capacity(10)
                .allocated(5)
                .available(5)
                .build();
        gpuCapacityMap.put("AZURE", gpuCapacity);

        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.of(
                CloudHealthEntity.builder()
                        .key(CloudHealthKey.builder()
                                     .cloudProvider(ResourceProvider.BYOC)
                                     .zone("id")
                                     .build())
                        .status(CloudHealthStatus.HEALTHY)
                        .clusterUpgradeStatus("SUCCESS")
                        .gpuUsage(gpuCapacityMap)
                        .build()));

        // Act
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, false,
                false);

        // Assert
        Assertions.assertNotNull(response);
        Assertions.assertEquals(1, response.size());
        GetClusterResponse getClusterResponse = response.get(0);
        Assertions.assertEquals("id", getClusterResponse.getClusterId());
        Assertions.assertEquals("dummy_end_point",
                                getClusterResponse.getSisConfig().getPublicKeysetEndpoint());
        Assertions.assertNotNull(getClusterResponse.getGpuUsage());
        Assertions.assertFalse(getClusterResponse.getGpuUsage().isEmpty());
        Assertions.assertNotNull(getClusterResponse.getNvcaLastConnected());
        Assertions.assertEquals(getClusterResponse.getStatus(),
                                ClusterStatusEnum.READY.toString());
        Assertions.assertEquals("SUCCESS", getClusterResponse.getClusterUpgradeStatus());
        
        // Assert imageCredentialHelper is present and configured correctly
        Assertions.assertNotNull(getClusterResponse.getImageCredentialHelper());
        Assertions.assertNotNull(getClusterResponse.getImageCredentialHelper().getImageConfig());
        Assertions.assertEquals("test_image_cred_repo", 
                                getClusterResponse.getImageCredentialHelper().getImageConfig().getRepository());
        Assertions.assertEquals("test_image_cred_tag", 
                                getClusterResponse.getImageCredentialHelper().getImageConfig().getTag());

        verify(clusterRepository).getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID);
        verify(nvcaConfigurationProperties).getSisConfig();
        verify(nvcaConfigurationProperties).getVaultConfig();
        verify(nvcaConfigurationProperties).getImageCredentialHelper();
        verifyNoInteractions(nvcaClusterConfigurationRepository);
    }

    @Test
    void getClustersByNcaId_withAllAuthorizedClustersFilter_returnsSuccess() {

        // Prepare
        ClustersByAuthorizedAccountsEntity nonByocCluster1 =
                getDummyClustersByAuthorizedAccountResp(PLATFORM_CLUSTER_GROUP_NAME,
                                                        PLATFORM_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID,
                                                        DUMMY_BYOC_NCA_ID, DUMMY_BYOC_INSTANCE_TYPE,
                                                        DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                        DUMMY_GPU_NAME, 8,
                                                        DUMMY_BYOC_AUTHORIZED_NCA_ID);
        String clusterGroupId = UUID.randomUUID().toString();
        ClustersByAuthorizedAccountsEntity cluster2 =
                getDummyClustersByAuthorizedAccountResp(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                                                        clusterGroupId, DUMMY_CLUSTER_ID + "_1",
                                                        DUMMY_BYOC_NCA_ID, DUMMY_BYOC_INSTANCE_TYPE,
                                                        DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                        DUMMY_GPU_NAME, 8, "*");

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID)).thenReturn(
                new ArrayList<>(List.of(cluster2)));

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(WILDCARD)).thenReturn(
                new ArrayList<>(List.of(nonByocCluster1)));

        // Send clusterEntity for Non BYOC as well
        ClusterEntity nonByocClusterEntity = getDummyClusterEntityForNonByocRegistration();
        ClusterEntity clusterEntity = TestUtil.getDummyClusterEntity();
        when(clusterRepository.getClusterInfoByClusterId(cluster2.getKey().getClusterId(), false))
                .thenReturn(Optional.of(clusterEntity));
        when(clusterRepository.getClusterInfoByClusterId(nonByocCluster1.getKey().getClusterId(), false))
                .thenReturn(Optional.of(nonByocClusterEntity));

        // Mock nested configuration objects
        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);
        
        // Mock imageCredentialHelper configuration
        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("test_image_cred_repo_2");
        mockImageConfig.setTag("test_image_cred_tag_2");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);
        
        Map<String, GpuCapacity> gpuCapacityMap = new HashMap<>();
        GpuCapacity gpuCapacity = GpuCapacity.builder()
                .capacity(10)
                .allocated(5)
                .available(5)
                .build();
        gpuCapacityMap.put("AZURE", gpuCapacity);

        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.of(
                CloudHealthEntity.builder()
                        .key(CloudHealthKey.builder()
                                     .cloudProvider(ResourceProvider.BYOC)
                                     .zone("id")
                                     .build())
                        .status(CloudHealthStatus.HEALTHY)
                        .clusterUpgradeStatus("SUCCESS")
                        .gpuUsage(gpuCapacityMap)
                        .build()));

        // Act
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, true,
                true);

        // Assert
        Assertions.assertNotNull(response);
        Assertions.assertEquals(2, response.size());
        for (GetClusterResponse getClusterResponse : response) {
            if (getClusterResponse.getClusterId().equals("id")) {
                Assertions.assertEquals("id", getClusterResponse.getClusterId());
                Assertions.assertEquals("dummy_end_point",
                        getClusterResponse.getSisConfig().getPublicKeysetEndpoint());
                Assertions.assertNotNull(getClusterResponse.getGpuUsage());
                Assertions.assertFalse(getClusterResponse.getGpuUsage().isEmpty());
                Assertions.assertNotNull(getClusterResponse.getNvcaLastConnected());
                Assertions.assertEquals(getClusterResponse.getStatus(),
                        ClusterStatusEnum.READY.toString());
                Assertions.assertEquals("SUCCESS", getClusterResponse.getClusterUpgradeStatus());
                
                // Assert imageCredentialHelper for first cluster
                Assertions.assertNotNull(getClusterResponse.getImageCredentialHelper());
                Assertions.assertNotNull(getClusterResponse.getImageCredentialHelper().getImageConfig());
                Assertions.assertEquals("test_image_cred_repo_2", 
                        getClusterResponse.getImageCredentialHelper().getImageConfig().getRepository());
                Assertions.assertEquals("test_image_cred_tag_2", 
                        getClusterResponse.getImageCredentialHelper().getImageConfig().getTag());
            }

            if (getClusterResponse.getClusterId().equals(DUMMY_NON_BYOC_ZONE_NAME)) {
                Assertions.assertEquals("dummy_nonbyoc-zone", getClusterResponse.getClusterId());
                Assertions.assertEquals("dummy_end_point",
                        getClusterResponse.getSisConfig().getPublicKeysetEndpoint());
                Assertions.assertNull(getClusterResponse.getGpuUsage());
                Assertions.assertNotNull(getClusterResponse.getNvcaLastConnected());
                Assertions.assertEquals(getClusterResponse.getStatus(),
                        ClusterStatusEnum.UNHEALTHY.toString());
                Assertions.assertNull(getClusterResponse.getClusterUpgradeStatus());
                
                // Assert imageCredentialHelper for Non BYOC cluster
                Assertions.assertNotNull(getClusterResponse.getImageCredentialHelper());
                Assertions.assertNotNull(getClusterResponse.getImageCredentialHelper().getImageConfig());
                Assertions.assertEquals("test_image_cred_repo_2", 
                        getClusterResponse.getImageCredentialHelper().getImageConfig().getRepository());
                Assertions.assertEquals("test_image_cred_tag_2", 
                        getClusterResponse.getImageCredentialHelper().getImageConfig().getTag());
            }
        }

        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID);
        verify(nvcaClusterRepository).getAllClustersInAuthorizedAccount(WILDCARD);
        verify(nvcaConfigurationProperties, times(2)).getSisConfig();
        verify(nvcaConfigurationProperties, times(2)).getVaultConfig();
        verify(nvcaConfigurationProperties, times(2)).getImageCredentialHelper();
        verifyNoInteractions(nvcaClusterConfigurationRepository);
    }

    @Test
    void getClustersByNcaId_withHeartbeatMissing_returnsSuccessWithStatusUnhealthy() {

        // Prepare
        when(clusterRepository.getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID)).thenReturn(
                List.of(TestUtil.getDummyClusterEntity()));

        // Mock nested configuration objects
        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);
        
        // Mock imageCredentialHelper configuration
        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("test_image_cred_repo_3");
        mockImageConfig.setTag("test_image_cred_tag_3");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);
        
        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(
                Optional.empty());

        // Act
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, false,
                false);

        // Assert
        Assertions.assertNotNull(response);
        Assertions.assertEquals(1, response.size());
        GetClusterResponse getClusterResponse = response.get(0);
        Assertions.assertEquals("id", getClusterResponse.getClusterId());
        Assertions.assertEquals("dummy_end_point",
                                getClusterResponse.getSisConfig().getPublicKeysetEndpoint());
        Assertions.assertNull(getClusterResponse.getGpuUsage());
        Assertions.assertNotNull(getClusterResponse.getNvcaLastConnected());
        Assertions.assertEquals(getClusterResponse.getStatus(),
                                ClusterStatusEnum.UNHEALTHY.toString());
        
        // Assert imageCredentialHelper is present even when heartbeat is missing
        Assertions.assertNotNull(getClusterResponse.getImageCredentialHelper());
        Assertions.assertNotNull(getClusterResponse.getImageCredentialHelper().getImageConfig());
        Assertions.assertEquals("test_image_cred_repo_3", 
                                getClusterResponse.getImageCredentialHelper().getImageConfig().getRepository());
        Assertions.assertEquals("test_image_cred_tag_3", 
                                getClusterResponse.getImageCredentialHelper().getImageConfig().getTag());

        verify(clusterRepository).getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID);
        verify(nvcaConfigurationProperties).getSisConfig();
        verify(nvcaConfigurationProperties).getVaultConfig();
        verify(nvcaConfigurationProperties).getImageCredentialHelper();
        verifyNoInteractions(nvcaClusterConfigurationRepository);

    }

    @Test
    void getClustersByNcaId_withClustersNotPresent_returnsEmptyResponse() {

        // Prepare
        when(clusterRepository.getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID)).thenReturn(List.of());

        // Act
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, false,
                false);

        // Assert
        Assertions.assertNotNull(response);
        Assertions.assertEquals(0, response.size());
        verify(clusterRepository).getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID);
        verifyNoInteractions(nvcaClusterConfigurationRepository);
    }

    @Test
    void getClusterByNcaIdAndClusterId_populatesConfiguration_whenPresent() {
        // Prepare
        ClusterEntity entity = TestUtil.getDummyClusterEntity();
        when(clusterRepository.getClusterInfoByClusterId(entity.getClusterId(), false))
                .thenReturn(Optional.of(entity));

        // Mock nested configuration objects
        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        // Mock imageCredentialHelper configuration
        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("test_image_cred_repo");
        mockImageConfig.setTag("test_image_cred_tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        var cfgEntity = new ClusterConfigurationByClusterIdEntity(
                entity.getClusterId(),
                Map.of("a", "b"),
                Map.of("f", "x")
        );
        when(nvcaClusterConfigurationRepository.findByClusterId(entity.getClusterId()))
                .thenReturn(Optional.of(cfgEntity));

        // Act
        var resp = clusterListingService.getClusterByNcaIdAndClusterId(entity.getNcaId(), entity.getClusterId());

        // Assert
        Assertions.assertNotNull(resp.getClusterConfigurations());
        Assertions.assertEquals("b", resp.getClusterConfigurations().get("a"));
        Assertions.assertNotNull(resp.getClusterConfigurationFiles());
        Assertions.assertEquals("x", resp.getClusterConfigurationFiles().get("f"));

        // Verify
        verify(nvcaClusterConfigurationRepository).findByClusterId(entity.getClusterId());
    }

    @Test
    void getClusterByNcaIdAndClusterId_configurationMissing_isOmitted() {
        // Prepare
        ClusterEntity entity = TestUtil.getDummyClusterEntity();
        when(clusterRepository.getClusterInfoByClusterId(entity.getClusterId(), false))
                .thenReturn(Optional.of(entity));

        // Mock nested configuration objects
        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        // Mock imageCredentialHelper configuration
        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("test_image_cred_repo");
        mockImageConfig.setTag("test_image_cred_tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        when(nvcaClusterConfigurationRepository.findByClusterId(entity.getClusterId()))
                .thenReturn(Optional.empty());

        // Act
        var resp = clusterListingService.getClusterByNcaIdAndClusterId(entity.getNcaId(), entity.getClusterId());

        // Assert
        Assertions.assertNull(resp.getClusterConfigurations());
        Assertions.assertNull(resp.getClusterConfigurationFiles());

        // Verify
        verify(nvcaClusterConfigurationRepository).findByClusterId(entity.getClusterId());
    }

    @Test
    void getClusterByNcaIdAndClusterId_withValidInputs_returnsSuccess() {

        // Prepare
        when(clusterRepository.getClusterInfoByClusterId("id", false)).thenReturn(
                Optional.of(TestUtil.getDummyClusterEntity()));

        // Mock nested configuration objects
        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);
        
        // Mock imageCredentialHelper configuration
        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("test_single_cluster_repo");
        mockImageConfig.setTag("test_single_cluster_tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        Map<String, GpuCapacity> gpuCapacityMap = new HashMap<>();
        GpuCapacity gpuCapacity = GpuCapacity.builder()
                .capacity(10)
                .allocated(5)
                .available(5)
                .build();
        gpuCapacityMap.put("AZURE", gpuCapacity);

        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.of(
                CloudHealthEntity.builder()
                        .key(CloudHealthKey.builder()
                                     .cloudProvider(ResourceProvider.BYOC)
                                     .zone("id")
                                     .build())
                        .status(CloudHealthStatus.HEALTHY)
                        .clusterUpgradeStatus("SUCCESS")
                        .gpuUsage(gpuCapacityMap)
                        .build()));

        // Act
        var response = clusterListingService.getClusterByNcaIdAndClusterId("ncaId", "id");

        // Assert
        Assertions.assertNotNull(response);
        Assertions.assertEquals("id", response.getClusterId());
        Assertions.assertEquals("ncaId", response.getNcaId());
        Assertions.assertEquals("dummy_end_point",
                                response.getSisConfig().getPublicKeysetEndpoint());
        Assertions.assertNotNull(response.getGpuUsage());
        Assertions.assertFalse(response.getGpuUsage().isEmpty());
        Assertions.assertNotNull(response.getNvcaLastConnected());
        Assertions.assertEquals("SUCCESS", response.getClusterUpgradeStatus());
        
        // Assert imageCredentialHelper for single cluster response
        Assertions.assertNotNull(response.getImageCredentialHelper());
        Assertions.assertNotNull(response.getImageCredentialHelper().getImageConfig());
        Assertions.assertEquals("test_single_cluster_repo", 
                                response.getImageCredentialHelper().getImageConfig().getRepository());
        Assertions.assertEquals("test_single_cluster_tag", 
                                response.getImageCredentialHelper().getImageConfig().getTag());

        verify(clusterRepository).getClusterInfoByClusterId("id", false);
        verify(nvcaConfigurationProperties).getSisConfig();
        verify(nvcaConfigurationProperties).getVaultConfig();
        verify(nvcaConfigurationProperties).getImageCredentialHelper();
        verify(nvcaClusterConfigurationRepository).findByClusterId("id");
    }

    @Test
    void getClusterByNcaIdAndClusterId_withClusterFoundButNcaIdNotMatching_throwsException() {

        // Prepare
        when(clusterRepository.getClusterInfoByClusterId("id", false)).thenReturn(
                Optional.of(TestUtil.getDummyClusterEntity()));

        // Act
        IcmsConflictException icmsConflictException =
                Assertions.assertThrows(IcmsConflictException.class,
                                        () -> clusterListingService.getClusterByNcaIdAndClusterId(
                                                "ncaId1", "id"));

        // Assert
        Assertions.assertEquals("ncaId1 ncaId doesn't exists for id clusterId",
                                icmsConflictException.getBody().getDetail());

        verify(clusterRepository).getClusterInfoByClusterId("id", false);
    }

    @Test
    void getClusterByNcaIdAndClusterId_withClusterInfoNotFound_throwsException() {

        // Prepare
        when(clusterRepository.getClusterInfoByClusterId("id", false)).thenReturn(
                Optional.empty());

        // Act
        IcmsNotFoundException icmsNotFoundException =
                Assertions.assertThrows(IcmsNotFoundException.class,
                                        () -> clusterListingService.getClusterByNcaIdAndClusterId(
                                                "ncaId", "id"));

        // Assert
        Assertions.assertEquals("Can not find any cluster for ncaId ncaId and id clusterId",
                                icmsNotFoundException.getBody().getDetail());

        verify(clusterRepository).getClusterInfoByClusterId("id", false);
    }

    @Test
    void getClusterByNcaIdAndClusterId_withClusterForBartFlow_throwsException() {

        // Prepare
        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setNvcaVersion(null);

        when(clusterRepository.getClusterInfoByClusterId("id", false)).thenReturn(
                Optional.of(clusterEntity));

        // Act
        PreConditionFailedException exception =
                Assertions.assertThrows(PreConditionFailedException.class,
                                        () -> clusterListingService.getClusterByNcaIdAndClusterId(
                                                "ncaId", "id"));

        // Assert
        Assertions.assertEquals(
                "The cluster with id clusterId can not be listed since it was registered with NVCA 1.0 flow",
                exception.getBody().getDetail());

        verify(clusterRepository).getClusterInfoByClusterId("id", false);
    }

    @Test
    void getClusterVersion_returnsClusterSourcesAndRegions() throws Exception {
        // Prepare
        String expectedClusterVersion = nvcaConfigurationProperties.getClusterVersion();
        when(nvcaConfigurationProperties.getClusterVersion()).thenReturn(expectedClusterVersion);

        // Mock ObjectMapper behavior
        ObjectNode rootNode = new JsonMapper().createObjectNode();
        rootNode.put("version", "1.0");
        when(objectMapper.readTree(expectedClusterVersion)).thenReturn(rootNode);
        when(objectMapper.createArrayNode()).thenAnswer(invocation -> new JsonMapper().createArrayNode());
        when(objectMapper.writeValueAsString(ArgumentMatchers.any())).thenAnswer(invocation -> {
            ObjectNode node = invocation.getArgument(0);
            return node.toString();
        });

        // Act
        String response = clusterListingService.getClusterVersion(DUMMY_BYOC_NCA_ID);

        // Assert
        Assertions.assertNotNull(response);
        Assertions.assertTrue(response.contains("\"clusterRegions\""));
        Assertions.assertTrue(response.contains("\"clusterSources\""));
        
        // Verify all regions are present
        for (ClusterRegion region : ClusterRegion.values()) {
            Assertions.assertTrue(response.contains(region.toString()));
        }
        
        // Verify all sources are present
        for (ClusterSource source : ClusterSource.values()) {
            Assertions.assertTrue(response.contains(source.toString()));
        }

        verify(objectMapper).readTree(expectedClusterVersion);
        verify(objectMapper, times(2)).createArrayNode();
        verify(objectMapper).writeValueAsString(ArgumentMatchers.any());
    }

    @Test
    void getClusterVersion_whenJsonProcessingFails_throwsException() throws Exception {
        // Prepare
        String clusterVersionStr = "{\"version\": \"1.0\"}";
        when(nvcaConfigurationProperties.getClusterVersion()).thenReturn(clusterVersionStr);
        when(objectMapper.readTree(clusterVersionStr)).thenThrow(new JacksonException("Parse error") {});

        // Act & Assert
        IcmsInternalServerException exception = Assertions.assertThrows(
                IcmsInternalServerException.class,
                () -> clusterListingService.getClusterVersion(DUMMY_BYOC_NCA_ID));

        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("Failed to append regions to cluster version"));
    }

    // ==================== Additional Tests for getClustersByNcaId ====================

    @Test
    void getClustersByNcaId_withNvca1Cluster_skipsCluster() {
        // Prepare - cluster with nvcaVersion = null (NVCA 1.0 flow)
        ClusterEntity nvca1Cluster = TestUtil.getDummyClusterEntity();
        nvca1Cluster.setNvcaVersion(null);

        when(clusterRepository.getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(List.of(nvca1Cluster));

        // Act
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, false, false);

        // Assert - NVCA 1.0 clusters should be skipped
        Assertions.assertNotNull(response);
        Assertions.assertEquals(0, response.size());
    }

    @Test
    void getClustersByNcaId_withAuthorizedNonByocClusterAndIncludeFalse_skipsNonByocCluster() {
        // Prepare
        ClustersByAuthorizedAccountsEntity nonByocCluster =
                getDummyClustersByAuthorizedAccountResp(PLATFORM_CLUSTER_GROUP_NAME,
                        PLATFORM_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID,
                        DUMMY_BYOC_NCA_ID, DUMMY_BYOC_INSTANCE_TYPE,
                        DUMMY_BYOC_INSTANCE_TYPE_VALUE, DUMMY_GPU_NAME, 8,
                        DUMMY_BYOC_AUTHORIZED_NCA_ID);

        when(computePlatformService.isPlatformCluster(PLATFORM_CLUSTER_GROUP_NAME)).thenReturn(true);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(new ArrayList<>(List.of(nonByocCluster)));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(WILDCARD))
                .thenReturn(new ArrayList<>());

        // Act - includeNonByocInAuthorizedClusters = false
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, true, false);

        // Assert - Non BYOC clusters should be skipped
        Assertions.assertNotNull(response);
        Assertions.assertEquals(0, response.size());
    }

    @Test
    void getClustersByNcaId_withAuthorizedNonByocClusterAndIncludeNull_skipsNonByocCluster() {
        // Prepare
        ClustersByAuthorizedAccountsEntity nonByocCluster =
                getDummyClustersByAuthorizedAccountResp(PLATFORM_CLUSTER_GROUP_NAME,
                        PLATFORM_CLUSTER_GROUP_ID, DUMMY_CLUSTER_ID,
                        DUMMY_BYOC_NCA_ID, DUMMY_BYOC_INSTANCE_TYPE,
                        DUMMY_BYOC_INSTANCE_TYPE_VALUE, DUMMY_GPU_NAME, 8,
                        DUMMY_BYOC_AUTHORIZED_NCA_ID);

        when(computePlatformService.isPlatformCluster(PLATFORM_CLUSTER_GROUP_NAME)).thenReturn(true);
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(new ArrayList<>(List.of(nonByocCluster)));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(WILDCARD))
                .thenReturn(new ArrayList<>());

        // Act - includeNonByocInAuthorizedClusters = null (should default to false)
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, true, null);

        // Assert - Non BYOC clusters should be skipped when includeNonByocInAuthorizedClusters is null
        Assertions.assertNotNull(response);
        Assertions.assertEquals(0, response.size());
    }

    @Test
    void getClustersByNcaId_withCloudHealthStatusNotHealthy_returnsUnhealthyStatus() {
        // Prepare
        ClusterEntity clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);

        when(clusterRepository.getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(List.of(clusterEntity));

        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("repo");
        mockImageConfig.setTag("tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        // Cloud health returns UNHEALTHY status
        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.of(
                CloudHealthEntity.builder()
                        .key(CloudHealthKey.builder()
                                .cloudProvider(ResourceProvider.BYOC)
                                .zone("id")
                                .build())
                        .status(CloudHealthStatus.UNHEALTHY)
                        .build()));

        // Act
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, false, false);

        // Assert - Status should be overridden to UNHEALTHY
        Assertions.assertNotNull(response);
        Assertions.assertEquals(1, response.size());
        Assertions.assertEquals(ClusterStatusEnum.UNHEALTHY.toString(), response.get(0).getStatus());
    }

    @Test
    void getClustersByNcaId_withClusterStatusNotReady_keepsOriginalStatus() {
        // Prepare
        ClusterEntity clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterStatus(ClusterStatusEnum.NOT_READY);

        when(clusterRepository.getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(List.of(clusterEntity));

        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("repo");
        mockImageConfig.setTag("tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        // Cloud health is missing
        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.empty());

        // Act
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, false, false);

        // Assert - Status should remain NOT_READY (not overridden because status != READY)
        Assertions.assertNotNull(response);
        Assertions.assertEquals(1, response.size());
        Assertions.assertEquals(ClusterStatusEnum.NOT_READY.toString(), response.get(0).getStatus());
    }

    @Test
    void getClustersByNcaId_withCloudHealthException_throwsException() {
        // Prepare
        ClusterEntity clusterEntity = TestUtil.getDummyClusterEntity();

        when(clusterRepository.getAllClustersInAnAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(List.of(clusterEntity));

        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("repo");
        mockImageConfig.setTag("tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id"))
                .thenThrow(new RuntimeException("Cloud health service error"));

        // Act & Assert
        IcmsInternalServerException exception = Assertions.assertThrows(
                IcmsInternalServerException.class,
                () -> clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, false, false));

        Assertions.assertTrue(exception.getBody().getDetail()
                .contains("Failed to fetch cluster info for ncaId"));
    }

    @Test
    void getClustersByNcaId_withAuthorizedNvca1Cluster_skipsCluster() {
        // Prepare - authorized cluster that is NVCA 1.0 (nvcaVersion = null)
        String clusterGroupId = UUID.randomUUID().toString();
        ClustersByAuthorizedAccountsEntity authorizedCluster =
                getDummyClustersByAuthorizedAccountResp(DUMMY_BYOC_CLUSTER_GROUP_NAME,
                        clusterGroupId, DUMMY_CLUSTER_ID,
                        DUMMY_BYOC_NCA_ID, DUMMY_BYOC_INSTANCE_TYPE,
                        DUMMY_BYOC_INSTANCE_TYPE_VALUE, DUMMY_GPU_NAME, 8,
                        DUMMY_BYOC_AUTHORIZED_NCA_ID);

        ClusterEntity nvca1Cluster = TestUtil.getDummyClusterEntity();
        nvca1Cluster.setNvcaVersion(null);

        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(DUMMY_BYOC_NCA_ID))
                .thenReturn(new ArrayList<>(List.of(authorizedCluster)));
        when(nvcaClusterRepository.getAllClustersInAuthorizedAccount(WILDCARD))
                .thenReturn(new ArrayList<>());
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, false))
                .thenReturn(Optional.of(nvca1Cluster));

        // Act
        var response = clusterListingService.getClustersByNcaId(DUMMY_BYOC_NCA_ID, true, false);

        // Assert - NVCA 1.0 authorized clusters should be skipped
        Assertions.assertNotNull(response);
        Assertions.assertEquals(0, response.size());
    }

    @Test
    void getClusterByNcaIdAndClusterId_withClusterStatusNotReady_keepsOriginalStatus() {
        // Prepare
        ClusterEntity clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterStatus(ClusterStatusEnum.NOT_READY);

        when(clusterRepository.getClusterInfoByClusterId("id", false))
                .thenReturn(Optional.of(clusterEntity));

        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("repo");
        mockImageConfig.setTag("tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        when(nvcaClusterConfigurationRepository.findByClusterId("id"))
                .thenReturn(Optional.empty());

        // Cloud health is missing
        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.empty());

        // Act
        var response = clusterListingService.getClusterByNcaIdAndClusterId("ncaId", "id");

        // Assert - Status should remain NOT_READY
        Assertions.assertNotNull(response);
        Assertions.assertEquals(ClusterStatusEnum.NOT_READY.toString(), response.getStatus());
    }

    @Test
    void getClusterByNcaIdAndClusterId_withCloudHealthStatusNotHealthy_returnsUnhealthyStatus() {
        // Prepare
        ClusterEntity clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterStatus(ClusterStatusEnum.READY);

        when(clusterRepository.getClusterInfoByClusterId("id", false))
                .thenReturn(Optional.of(clusterEntity));

        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("repo");
        mockImageConfig.setTag("tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        when(nvcaClusterConfigurationRepository.findByClusterId("id"))
                .thenReturn(Optional.empty());

        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.of(
                CloudHealthEntity.builder()
                        .key(CloudHealthKey.builder()
                                .cloudProvider(ResourceProvider.BYOC)
                                .zone("id")
                                .build())
                        .status(CloudHealthStatus.UNHEALTHY)
                        .build()));

        // Act
        var response = clusterListingService.getClusterByNcaIdAndClusterId("ncaId", "id");

        // Assert - Status should be overridden to UNHEALTHY
        Assertions.assertNotNull(response);
        Assertions.assertEquals(ClusterStatusEnum.UNHEALTHY.toString(), response.getStatus());
    }

    @Test
    void getClusterByNcaIdAndClusterId_withNullCustomAttributes_returnsEmptySet() {
        // Prepare
        ClusterEntity clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setCustomAttributes(null);

        when(clusterRepository.getClusterInfoByClusterId("id", false))
                .thenReturn(Optional.of(clusterEntity));

        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("repo");
        mockImageConfig.setTag("tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        when(nvcaClusterConfigurationRepository.findByClusterId("id"))
                .thenReturn(Optional.empty());

        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.empty());

        // Act
        var response = clusterListingService.getClusterByNcaIdAndClusterId("ncaId", "id");

        // Assert - Custom attributes should be empty set, not null
        Assertions.assertNotNull(response);
        Assertions.assertNotNull(response.getCustomAttributes());
        Assertions.assertTrue(response.getCustomAttributes().isEmpty());
    }

    @Test
    void getClusterByNcaIdAndClusterId_withRegionUpperCase_returnsLowerCaseRegion() {
        // Prepare
        ClusterEntity clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setRegion("US-EAST-1");

        when(clusterRepository.getClusterInfoByClusterId("id", false))
                .thenReturn(Optional.of(clusterEntity));

        var mockSisConfig = new NvcaConfigurationProperties.SisConfig();
        mockSisConfig.setPublicKeySetEndpoint("dummy_end_point");
        mockSisConfig.setTokenUrl("dummy_token_url");
        mockSisConfig.setSpotServiceUrl("dummy_service_url");
        when(nvcaConfigurationProperties.getSisConfig()).thenReturn(mockSisConfig);

        var mockVaultConfig = new NvcaConfigurationProperties.VaultConfig();
        mockVaultConfig.setAddress("dummy_address");
        when(nvcaConfigurationProperties.getVaultConfig()).thenReturn(mockVaultConfig);

        var mockImageConfig = new NvcaConfigurationProperties.ImageCredentialHelper.ImageConfig();
        mockImageConfig.setRepository("repo");
        mockImageConfig.setTag("tag");
        var mockImageCredentialHelper = new NvcaConfigurationProperties.ImageCredentialHelper();
        mockImageCredentialHelper.setImageConfig(mockImageConfig);
        when(nvcaConfigurationProperties.getImageCredentialHelper()).thenReturn(mockImageCredentialHelper);

        when(nvcaClusterConfigurationRepository.findByClusterId("id"))
                .thenReturn(Optional.empty());

        when(cloudHealthService.getCloudHealth(ResourceProvider.BYOC, "id")).thenReturn(Optional.empty());

        // Act
        var response = clusterListingService.getClusterByNcaIdAndClusterId("ncaId", "id");

        // Assert - Region should be converted to lowercase
        Assertions.assertNotNull(response);
        Assertions.assertEquals("us-east-1", response.getRegion());
    }

    // ==================== Private helper methods ====================

    private static ClusterEntity getDummyClusterEntityForNonByocRegistration() {
        return ClusterEntity.builder()
                .clusterName(DUMMY_NON_BYOC_ZONE_NAME)
                .clusterId(DUMMY_NON_BYOC_ZONE_NAME)
                .ncaId("nonbyoc_nca_id")
                .terminationQueueUrl("dummy_nonbyoc_termination_url")
                .terminationQueueType("queue_type")
                .clusterDescription(PLATFORM_CLUSTER_DESCRIPTION)
                .clusterProvider(ClusterProviderEnum.OCI)
                .clusterStatus(ClusterStatusEnum.READY)
                .clusterGroupName(PLATFORM_CLUSTER_GROUP_NAME)
                .clusterGroupId(PLATFORM_CLUSTER_GROUP_ID)
                .creationQueueUrl(DUMMY_CREATION_QUEUE_URL)
                .creationQueueType("queue_type")
                .k8sVersion("k8sVersion")
                .registrationTime(Instant.now())
                .gpus(buildGpuUdts())
                .authorizedNcaIds(Set.of("ncaId1", "ncaId2"))
                .requestDump("request")
                .region(ClusterRegion.US_EAST_1.toString())
                .capabilities(Set.of("DynamicGPUDiscovery"))
                .attributes(Set.of("KataRuntimeIsolation"))
                .nvcaVersion("1.0.0")
                .authClientId("dummy_auth_client_id")
                .nvcaLastConnected(Instant.now())
                .gpusV4(Set.of(
                        getDummyGpuV4(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_INSTANCE_TYPE_VALUE, 1,
                                      "AZURE")))
                .gpusV5(NvcaConverter.getGpusV5(Collections.emptySet(), Set.of(
                        getDummyGpuV4(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_INSTANCE_TYPE_VALUE, 1,
                                      "AZURE"))))
                .allowClusterTargeting(Boolean.TRUE)
                .build();
    }
}
