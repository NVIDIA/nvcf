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
package com.nvidia.icms.inbound.rest.controllers.account;

import static com.nvidia.icms.util.TestUtil.ATTRIBUTES_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_AZURE_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_AZURE_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_AZURE_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_AUTHORIZED_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_CLUSTER_GROUP_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_OCI_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_NVCA_VERSION;
import static com.nvidia.icms.util.TestUtil.DUMMY_OCI_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_OCI_INSTANCE_TYPE_VALUE;
import static com.nvidia.icms.util.TestUtil.INSTANCE_TYPES_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.NGC_CLUSTER_NAME_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.NGC_GPU_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.NGC_REGION_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.getDummyGpuFroBart;
import static com.nvidia.icms.util.TestUtil.getDummyGpuV5;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceType;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceTypeV5;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;

import com.amazonaws.services.sqs.model.QueueAttributeName;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.account.AccountInfoService;
import com.nvidia.icms.service.byoc.ClustersService;
import com.nvidia.icms.util.JwtKeyUtils;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.jetbrains.annotations.NotNull;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mockito;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;

@Slf4j
public class AccountInfoControllerTest extends IntegrationTest {

    private static final String GPU_API_URL = "/v1/si/accounts/{ncaId}/gpus";
    private static final String REGIONS_API_URL = "/v1/si/accounts/{ncaId}/regions";
    private static final String CLUSTER_NAMES_API_URL = "/v1/si/accounts/{ncaId}/clusterNames";
    private static final String ATTRIBUTES_API_URL = "/v1/si/accounts/{ncaId}/attributes";
    private static final String INSTANCE_TYPES_API_URL = "/v1/si/accounts/{ncaId}/instanceTypes";

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private ClustersService clustersService;

    @MockitoBean
    private ClusterRepository clusterRepository;

    @MockitoBean
    private NvcaClusterRepository nvcaClusterRepository;

    @MockitoBean
    private CloudHealthService cloudHealthService;

    @Autowired
    private AccountInfoService accountInfoService;

    @MockitoBean
    private ClusterTargetingHelper clusterTargetingHelper;

    @Test
    void testGetGpusForBart() throws Exception {
        //prepare
        String azureClusterGroupId = UUID.randomUUID().toString();
        String ociClusterGroupId = UUID.randomUUID().toString();
        var ncaId = "bart_" + DUMMY_BYOC_NCA_ID;

        // BART clusters
        List<ClusterGroupsByAuthorizedAccountsEntity> clusterGroupsByAuthorizedAccountsEntities =
                getDummyClusterGroups(azureClusterGroupId, ociClusterGroupId);

        Mockito.when(clusterRepository.getAllClusterGroupsInAuthorizedAccount(ncaId))
                .thenReturn(clusterGroupsByAuthorizedAccountsEntities);

        List<ClusterByGroupIdAndIdEntity> clustersFromDb =
                getClusterByGroupIdAndIdEntitiesForBart(azureClusterGroupId, ociClusterGroupId);

        Mockito.when(clusterRepository.getClustersFromClusterGroup(azureClusterGroupId))
                .thenReturn(clustersFromDb);
        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(GPU_API_URL, ncaId)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        NGC_GPU_LISTING_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);
        assertEquals("{\"gpus\":[]}", response);
    }

    @ParameterizedTest
    @MethodSource("getRegionFilters")
    void testGetRegions(String clusters, String attributes, String gpus, String backends, String expected)
            throws Exception {
        //prepare
        String azureClusterGroupId = UUID.randomUUID().toString();
        String azureClusterId = UUID.randomUUID().toString();
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityAzure = getClusterByGroupIdAndIdEntityForAzure(
                azureClusterGroupId, azureClusterId);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityAzure);
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(azureClusterId, getDummyCloudHealthEntity(azureClusterId, DUMMY_AZURE_GPU_NAME),
                       ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(REGIONS_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        NGC_REGION_LISTING_SCOPE))
                                .param("clusters", clusters)
                                .param("attributes", attributes)
                                .param("gpus", gpus)
                                .param("backends", backends)
                                .param("instanceTypeUsage", "DEFAULT"))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);
        assertEquals(expected, response);
    }

    @ParameterizedTest
    @MethodSource("getClusterFilters")
    void testGetClusterNames(String attributes, String gpus, String backends, String expected) throws Exception {
        //prepare
        String azureClusterGroupId = UUID.randomUUID().toString();
        String azureClusterId = UUID.randomUUID().toString();
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();
        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityAzure = getClusterByGroupIdAndIdEntityForAzure(
                azureClusterGroupId, azureClusterId);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityAzure);
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(azureClusterId, getDummyCloudHealthEntity(azureClusterId, DUMMY_AZURE_GPU_NAME),
                       ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(CLUSTER_NAMES_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        NGC_CLUSTER_NAME_LISTING_SCOPE))
                                .param("attributes", attributes)
                                .param("gpus", gpus).param("backends", backends))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);
        assertEquals(expected, response);
    }

    @ParameterizedTest
    @MethodSource("getAttributeFilters")
    void testGetAttributes(String regions, String clusters, String gpus, String backends, String expectedAttributes)
            throws Exception {
        //prepare
        String azureClusterGroupId = UUID.randomUUID().toString();
        String azureClusterId = UUID.randomUUID().toString();
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityAzure = getClusterByGroupIdAndIdEntityForAzure(
                azureClusterGroupId, azureClusterId);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityAzure);
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(azureClusterId, getDummyCloudHealthEntity(azureClusterId, DUMMY_AZURE_GPU_NAME),
                       ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(ATTRIBUTES_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        ATTRIBUTES_LISTING_SCOPE))
                                .param("regions", regions)
                                .param("clusters", clusters)
                                .param("gpus", gpus)
                                .param("backends", backends))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);
        assertThat(response).contains(expectedAttributes);
    }

    @Test
    void testGetInstanceTypesWithoutFilters_returnsAllInstances() throws Exception {
        //prepare
        String azureClusterGroupId = UUID.randomUUID().toString();
        String azureClusterId = UUID.randomUUID().toString();
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityAzure = getClusterByGroupIdAndIdEntityForAzure(
                azureClusterGroupId, azureClusterId);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityAzure);
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(azureClusterId, getDummyCloudHealthEntity(azureClusterId, DUMMY_AZURE_GPU_NAME),
                       ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCE_TYPES_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        INSTANCE_TYPES_LISTING_SCOPE))
                                .param("regions", "").param("clusters", "")
                                .param("attributes", "").param("gpus", "")
                                .param("backends","AZURE,OCI"))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);

        String expectedInstanceTypeDetails = "{\"AZURE_GPU\":[{\"name\":\"AZURE.GPU.AZURE_GPU_1x\",\"value\":\"AZURE.GPU.AZURE_GPU\",\"description\":\"GPU\",\"cpuCores\":4,\"systemMemory\":\"10Gi\",\"gpuMemory\":\"20Gi\",\"gpuCount\":8,\"availableCapacity\":1,\"clusters\":[\"azure_cluster_name\"],\"regions\":[\"dummy_region_1\"],\"attributes\":[\"cattr1\",\"cattr2\",\"attr2\",\"attr1\"],\"gpuName\":\"AZURE_GPU\",\"defaultable\":true,\"cpuArch\":null,\"os\":null,\"driverVersion\":null,\"storage\":null,\"nodeType\":\"SINGLE\"}],\"OCI_GPU\":[{\"name\":\"OCI.GPU.OCI_GPU_1x\",\"value\":\"OCI.GPU.OCI_GPU\",\"description\":\"GPU\",\"cpuCores\":4,\"systemMemory\":\"10Gi\",\"gpuMemory\":\"20Gi\",\"gpuCount\":8,\"availableCapacity\":1,\"clusters\":[\"oci_cluster_name\"],\"regions\":[\"dummy_region_2\"],\"attributes\":[\"cattr1\",\"cattr2\",\"attr2\",\"attr3\"],\"gpuName\":\"OCI_GPU\",\"defaultable\":true,\"cpuArch\":null,\"os\":null,\"driverVersion\":null,\"storage\":null,\"nodeType\":\"SINGLE\"}]}";
        assertThat(response).isEqualTo(expectedInstanceTypeDetails);
    }

    @Test
    void testGetInstanceTypesWitInstanceTypeFilter_returnsSpecificInstances() throws Exception {
        //prepare
        String azureClusterGroupId = UUID.randomUUID().toString();
        String azureClusterId = UUID.randomUUID().toString();
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityAzure = getClusterByGroupIdAndIdEntityForAzure(
                azureClusterGroupId, azureClusterId);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityAzure);
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(azureClusterId, getDummyCloudHealthEntity(azureClusterId, DUMMY_AZURE_GPU_NAME),
                       ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCE_TYPES_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        INSTANCE_TYPES_LISTING_SCOPE))
                                .param("regions", "")
                                .param("clusters", "")
                                .param("attributes", "")
                                .param("gpus", "AZURE_GPU")
                                .param("instanceType", "AZURE.GPU.AZURE_GPU_1x")
                                .param("instanceTypeUsage", ""))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);

        String expectedInstanceTypeDetails = "{\"AZURE_GPU\":[{\"name\":\"AZURE.GPU.AZURE_GPU_1x\",\"value\":\"AZURE.GPU.AZURE_GPU\",\"description\":\"GPU\",\"cpuCores\":4,\"systemMemory\":\"10Gi\",\"gpuMemory\":\"20Gi\",\"gpuCount\":8,\"availableCapacity\":1,\"clusters\":[\"azure_cluster_name\"],\"regions\":[\"dummy_region_1\"],\"attributes\":[\"cattr1\",\"cattr2\",\"attr2\",\"attr1\"],\"gpuName\":\"AZURE_GPU\",\"defaultable\":true,\"cpuArch\":null,\"os\":null,\"driverVersion\":null,\"storage\":null,\"nodeType\":\"SINGLE\"}]}";
        assertThat(response).isEqualTo(expectedInstanceTypeDetails);
    }

    @Test
    void testGetInstanceTypesWithFilters_returnEmpty() throws Exception {
        //prepare
        String azureClusterGroupId = UUID.randomUUID().toString();
        String azureClusterId = UUID.randomUUID().toString();
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();
        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityAzure = getClusterByGroupIdAndIdEntityForAzure(
                azureClusterGroupId, azureClusterId);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityAzure);
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(azureClusterId, getDummyCloudHealthEntity(azureClusterId, DUMMY_AZURE_GPU_NAME),
                       ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCE_TYPES_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        INSTANCE_TYPES_LISTING_SCOPE))
                                .param("regions", "dummy_region_1,dummy_region_2")
                                .param("clusters", "azure_cluster_name,oci_cluster_name")
                                .param("attributes", "attr4")
                                .param("gpus", "AZURE_GPU,OCI_GPU")
                                .param("backends","dummy_group_name"))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);

        String expectedInstanceTypeDetails = "{}";
        assertThat(response).isEqualTo(expectedInstanceTypeDetails);
    }

    @Test
    void testGetInstanceTypesWitInstanceTypeUsageFilter_returnsSpecificInstances() throws Exception {
        //prepare
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

        var instanceType1 = getDummyInstanceTypeV5("instance_type_1", "instance_type_value_1", 1, NodeTypeEnum.SINGLE);
        var instanceType2 = getDummyInstanceTypeV5("instance_type_2", "instance_type_value_2", 1, NodeTypeEnum.MULTI);
        var dummyGpuV5 = getDummyGpuV5(Set.of(instanceType1, instanceType2), DUMMY_OCI_GPU_NAME);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        // Setting nodeType as MULTI
        clusterByGroupIdAndIdEntityOci.setGpusV5(Set.of(dummyGpuV5));

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                       Map.of(ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCE_TYPES_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        INSTANCE_TYPES_LISTING_SCOPE))
                                .param("regions", "")
                                .param("clusters", "")
                                .param("attributes", "")
                                .param("gpus", "")
                                .param("instanceType", "")
                                .param("instanceTypeUsage", InstanceTypeUsageEnum.CONTAINER.toString()))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);

        // instance_type_1 is SINGLE nodeType, due that including it in response
        String expectedInstanceTypeDetails = "{\"OCI_GPU\":[{\"name\":\"instance_type_1\",\"value\":\"instance_type_value_1\",\"description\":\"GPU\",\"cpuCores\":4,\"systemMemory\":\"10Gi\",\"gpuMemory\":\"20Gi\",\"gpuCount\":1,\"availableCapacity\":10,\"clusters\":[\"oci_cluster_name\"],\"regions\":[\"dummy_region_2\"],\"attributes\":[\"cattr1\",\"cattr2\",\"attr2\",\"attr3\"],\"gpuName\":\"OCI_GPU\",\"defaultable\":true,\"cpuArch\":null,\"os\":null,\"driverVersion\":null,\"storage\":null,\"nodeType\":\"SINGLE\"}]}";
        assertThat(response).isEqualTo(expectedInstanceTypeDetails);
    }

    @Test
    void testGetRegionsWitInstanceTypeUsageFilter_returnsSpecificRegions() throws Exception {
        //prepare
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

        var instanceType1 = getDummyInstanceTypeV5("instance_type_1", "instance_type_value_1", 1, NodeTypeEnum.SINGLE);
        var instanceType2 = getDummyInstanceTypeV5("instance_type_2", "instance_type_value_2", 1, NodeTypeEnum.MULTI);
        var dummyGpuV5 = getDummyGpuV5(Set.of(instanceType1, instanceType2), DUMMY_OCI_GPU_NAME);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        // Setting nodeType as MULTI
        clusterByGroupIdAndIdEntityOci.setGpusV5(Set.of(dummyGpuV5));

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(REGIONS_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        NGC_REGION_LISTING_SCOPE))
                                .param("clusters", "")
                                .param("attributes", "")
                                .param("gpus", "")
                                .param("backends", "")
                                .param("instanceTypeUsage", "CONTAINER"))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        // Verify
        String response = mvcResult.getResponse().getContentAsString();

        // instance_type_1 is SINGLE nodeType, due that including region in response
        String expectedRegions = "{\"regions\":[\"dummy_region_2\"]}";
        assertThat(response).isEqualTo(expectedRegions);
    }

    @Test
    void testGetClusterNamesWitInstanceTypeUsageFilter_returnsSpecificClusters() throws Exception {
        //prepare
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

       var instanceType1 = getDummyInstanceTypeV5("instance_type_1", "instance_type_value_1", 1, NodeTypeEnum.SINGLE);
        var instanceType2 = getDummyInstanceTypeV5("instance_type_2", "instance_type_value_2", 1, NodeTypeEnum.MULTI);
        var dummyGpuV5 = getDummyGpuV5(Set.of(instanceType1, instanceType2), DUMMY_OCI_GPU_NAME);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        // Setting nodeType as MULTI
        clusterByGroupIdAndIdEntityOci.setGpusV5(Set.of(dummyGpuV5));

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(CLUSTER_NAMES_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        NGC_CLUSTER_NAME_LISTING_SCOPE))
                                .param("attributes", "")
                                .param("gpus", "")
                                .param("backends", "")
                                .param("instanceTypeUsage", "CONTAINER"))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);

        // instance_type_1 is SINGLE nodeType, due that including cluster name in response
        assertEquals("{\"clusterNames\":[\"oci_cluster_name\"]}", response);
    }

    @ParameterizedTest
    @MethodSource("capacityValidationParams")
    void testInstanceTypesCapacityValidation(String capacityValidationParam, String expectedResponse) throws Exception {
        arrangeZeroCapacityAzureClusterSetup();

        var request = MockMvcRequestBuilders.get(INSTANCE_TYPES_API_URL, DUMMY_BYOC_NCA_ID)
                .contentType(MediaType.APPLICATION_JSON)
                .header(HttpHeaders.AUTHORIZATION, JwtKeyUtils.getAuthHeader(
                        DUMMY_CUSTOMER_1, INSTANCE_TYPES_LISTING_SCOPE));
        if (capacityValidationParam != null) {
            request.param("capacityValidation", capacityValidationParam);
        }

        MvcResult mvcResult = mockMvc.perform(request)
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);
        assertThat(response).isEqualTo(expectedResponse);
    }

    @Test
    void testGetAttributesWitInstanceTypeUsageFilter_returnsSpecificAttributes() throws Exception {
        //prepare
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

        var instanceType1 = getDummyInstanceTypeV5("instance_type_1", "instance_type_value_1", 1, NodeTypeEnum.SINGLE);
        var instanceType2 = getDummyInstanceTypeV5("instance_type_2", "instance_type_value_2", 1, NodeTypeEnum.MULTI);
        var dummyGpuV5 = getDummyGpuV5(Set.of(instanceType1, instanceType2), DUMMY_OCI_GPU_NAME);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        // Setting nodeType as MULTI
        clusterByGroupIdAndIdEntityOci.setGpusV5(Set.of(dummyGpuV5));

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(ATTRIBUTES_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        ATTRIBUTES_LISTING_SCOPE))
                                .param("regions", "")
                                .param("clusters", "")
                                .param("gpus", "")
                                .param("instanceTypeUsage", "CONTAINER"))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);

        // instance_type_1 is SINGLE nodeType, due that including attributes in response
        assertEquals(response, "{\"attributes\":[\"attr2\",\"attr3\",\"cattr1\",\"cattr2\"]}");
    }

    @Test
    void testGetGpusWitInstanceTypeUsageFilter_returnsSpecificAttributes() throws Exception {
        //prepare
        String ociClusterGroupId = UUID.randomUUID().toString();
        String ociClusterId = UUID.randomUUID().toString();

        var instanceType1 = getDummyInstanceTypeV5("instance_type_1", "instance_type_value_1", 1, NodeTypeEnum.SINGLE);
        var instanceType2 = getDummyInstanceTypeV5("instance_type_2", "instance_type_value_2", 1, NodeTypeEnum.MULTI);
        var dummyGpuV5 = getDummyGpuV5(Set.of(instanceType1, instanceType2), DUMMY_OCI_GPU_NAME);

        ClusterByGroupIdAndIdEntity clusterByGroupIdAndIdEntityOci = getClusterByGroupIdAndIdEntityForOci(
                ociClusterGroupId, ociClusterId);

        // Setting nodeType as MULTI
        clusterByGroupIdAndIdEntityOci.setGpusV5(Set.of(dummyGpuV5));

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterByGroupIdAndIdEntityOci);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);

        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
                .thenReturn(Set.of());

        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(
                Map.of(ociClusterId, getDummyCloudHealthEntity(ociClusterId, DUMMY_OCI_GPU_NAME)));

        // Act
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(GPU_API_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON).header(HttpHeaders.AUTHORIZATION,
                                                                                JwtKeyUtils.getAuthHeader(
                                                                                        DUMMY_CUSTOMER_1,
                                                                                        NGC_GPU_LISTING_SCOPE))
                                .param("backend", "")
                                .param("instanceTypeUsage", "CONTAINER"))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Verify
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);

        // instance_type_1 is SINGLE nodeType, due that including gpu name in response
        assertEquals(response, "{\"gpus\":[\"OCI_GPU\"]}");
    }

    @NotNull
    private static List<ClusterGroupsByAuthorizedAccountsEntity> getDummyClusterGroups(
            String azureClusterGroupId, String ociClusterGroupId) {
        return List.of(getDummyClusterGroup("AWS", DUMMY_BYOC_NCA_ID,
                                            Set.of("dummy_common_nca_id", "dummy_aws_2"), "AWS",
                                            azureClusterGroupId),
                       getDummyClusterGroup("OCI", DUMMY_BYOC_NCA_ID,
                                            Set.of("AWS", "dummy_common_nca_id"), "OCI",
                                            ociClusterGroupId));
    }

    @NotNull
    private static ClusterByGroupIdAndIdEntity getClusterByGroupIdAndIdEntityForAzure(
            String clusterGroupId, String clusterId) {
        return getDummyClusterGroup("AZURE", clusterGroupId, clusterId,
                                    "azure_cluster_name",
                                    Set.of(getDummyGpuV5(DUMMY_AZURE_INSTANCE_TYPE,
                                                         DUMMY_AZURE_INSTANCE_TYPE_VALUE, 8,
                                                         DUMMY_AZURE_GPU_NAME)),
                                    Set.of("attr1", "attr2"), Set.of("cattr1", "cattr2"),
                                    "dummy_region_1", ClusterProviderEnum.AZURE);
    }

    @NotNull
    private static ClusterByGroupIdAndIdEntity getClusterByGroupIdAndIdEntityForOci(
            String clusterGroupId, String clusterId) {
        return getDummyClusterGroup("OCI", clusterGroupId, clusterId,
                                    "oci_cluster_name",
                                    Set.of(getDummyGpuV5(DUMMY_OCI_INSTANCE_TYPE,
                                                         DUMMY_OCI_INSTANCE_TYPE_VALUE, 8,
                                                         DUMMY_OCI_GPU_NAME)),
                                    Set.of("attr2", "attr3"), Set.of("cattr1", "cattr2"),
                                    "dummy_region_2", ClusterProviderEnum.OCI);
    }

    @NotNull
    private static List<ClusterByGroupIdAndIdEntity> getClusterByGroupIdAndIdEntitiesForBart(
            String azureClusterGroupId, String ociClusterGroupId) {
        return List.of(getDummyClusterByGroupIdAndIdEntityForBart("AZURE", azureClusterGroupId,
                                                                  "azure_cluster_name",
                                                                  Set.of(getDummyGpuFroBart(
                                                                          DUMMY_BYOC_INSTANCE_TYPE,
                                                                          DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                          8, DUMMY_OCI_GPU_NAME)),
                                                                  Set.of("attr1", "attr2"),
                                                                  Set.of("cattr1", "cattr2"),
                                                                  "dummy_region_1"),
                       getDummyClusterByGroupIdAndIdEntityForBart("OCI", ociClusterGroupId,
                                                                  "oci_cluster_name",
                                                                  Set.of(getDummyGpuFroBart(
                                                                          DUMMY_BYOC_INSTANCE_TYPE,
                                                                          DUMMY_BYOC_INSTANCE_TYPE_VALUE,
                                                                          8, "OCI_GPU")),
                                                                  Set.of("attr2", "attr3"),
                                                                  Set.of("cattr1", "cattr2"),
                                                                  "dummy_region_2"));
    }

    private static ClustersByAuthorizedAccountsEntity getDummyClustersByAuthorizedAccountRespForAzure(
            String clusterGroupId, String clusterId) {
        return getDummyClustersByAuthorizedAccountResp(clusterGroupId, clusterId,
                                                       DUMMY_AZURE_INSTANCE_TYPE,
                                                       DUMMY_AZURE_INSTANCE_TYPE_VALUE,
                                                       DUMMY_AZURE_GPU_NAME);
    }

    private static ClustersByAuthorizedAccountsEntity getDummyClustersByAuthorizedAccountRespForOci(
            String clusterGroupId, String clusterId) {
        return getDummyClustersByAuthorizedAccountResp(clusterGroupId, clusterId,
                                                       DUMMY_OCI_INSTANCE_TYPE,
                                                       DUMMY_OCI_INSTANCE_TYPE_VALUE,
                                                       DUMMY_OCI_GPU_NAME);
    }

    private static ClustersByAuthorizedAccountsEntity getDummyClustersByAuthorizedAccountResp(
            String clusterGroupId, String clusterId, String instanceType, String instanceTypeValue, String gpuName) {

        Map<String, CreationQueueUdt> creationQueueMap = new HashMap<>();
        creationQueueMap.put(DUMMY_OCI_GPU_NAME, CreationQueueUdt.builder()
                .url("creation_queue_url")
                .queueType(QueueAttributeName.FifoQueue.toString())
                .build());
        GpuV5Udt gpuV5 = getDummyGpuV5(instanceType, instanceTypeValue, 8, gpuName);
        return ClustersByAuthorizedAccountsEntity.builder()
                .key(ClustersByAuthorizedAccountsKey.builder()
                             .ncaIdKey(DUMMY_BYOC_AUTHORIZED_NCA_ID)
                             .clusterId(clusterId)
                             .build())
                .ncaId(DUMMY_BYOC_NCA_ID)
                .clusterGroupName(DUMMY_BYOC_CLUSTER_GROUP_NAME)
                .clusterGroupId(clusterGroupId)
                .authorizedNcaIds(Set.of(DUMMY_BYOC_AUTHORIZED_NCA_ID))
                .creationQueues(creationQueueMap)
                .gpusV5(Set.of(gpuV5))
                .build();
    }

    private static ClusterByGroupIdAndIdEntity getDummyClusterGroup(String clusterGroupName,
                                                                    String clusterGroupId,
                                                                    String clusterId,
                                                                    String clusterName,
                                                                    Set<GpuV5Udt> gpuV5Udts,
                                                                    Set<String> attributes,
                                                                    Set<String> customAttributes,
                                                                    String region,
                                                                    ClusterProviderEnum clusterProviderEnum) {

        return ClusterByGroupIdAndIdEntity.builder()
                .key(ClusterByGroupIdAndIdKey.builder()
                             .clusterGroupId(clusterGroupId)
                             .clusterId(clusterId)
                             .build())
                .clusterGroupName(clusterGroupName)
                .clusterDescription("dummy_cluster_description").k8sVersion("v1.25.9")
                .clusterStatus(ClusterStatusEnum.READY).clusterName(clusterName)
                .ncaId(DUMMY_BYOC_NCA_ID).gpusV5(gpuV5Udts).attributes(attributes).region(region)
                .allowClusterTargeting(true).customAttributes(customAttributes)
                .nvcaVersion(DUMMY_NVCA_VERSION)
                .clusterProvider(clusterProviderEnum)
                .build();
    }

    private static CloudHealthEntity getDummyCloudHealthEntity(String clusterId, String gpuName) {
        GpuCapacity gpuCapacity = GpuCapacity.builder()
                .capacity(20)
                .allocated(10)
                .available(10)
                .build();
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(gpuName, gpuCapacity);

        return CloudHealthEntity.builder()
                .key(CloudHealthKey.builder()
                             .cloudProvider(ResourceProvider.BYOC)
                             .zone(clusterId)
                             .build())
                .gpuUsage(gpuUsage)
                .status(CloudHealthStatus.HEALTHY)
                .build();
    }

    private static ClusterByGroupIdAndIdEntity getDummyClusterByGroupIdAndIdEntityForBart(
            String clusterGroupName, String clusterGroupId, String clusterName, Set<GpuUdt> gpus,
            Set<String> attributes, Set<String> customAttributes, String region) {

        return ClusterByGroupIdAndIdEntity.builder()
                .key(ClusterByGroupIdAndIdKey.builder()
                             .clusterGroupId(clusterGroupId)
                             .clusterId(UUID.randomUUID().toString())
                             .build())
                .clusterGroupName(clusterGroupName)
                .clusterDescription("dummy_cluster_description").k8sVersion("v1.25.9")
                .clusterStatus(ClusterStatusEnum.READY).clusterName(clusterName)
                .ncaId(DUMMY_BYOC_NCA_ID).gpus(gpus).attributes(attributes)
                .customAttributes(customAttributes).region(region).build();
    }

    public static ClusterGroupsByAuthorizedAccountsEntity getDummyClusterGroup(String ncaId,
                                                                               String ncaIdKey,
                                                                               Set<String> ncaIds,
                                                                               String clusterGroupName,
                                                                               String clusterGroupId) {

        return ClusterGroupsByAuthorizedAccountsEntity.builder()
                .key(ClusterGroupsByAuthorizedAccountsKey.builder()
                             .ncaIdKey(ncaIdKey)
                             .clusterGroupId(clusterGroupId)
                             .clusterGroupName(clusterGroupName)
                             .build())
                .ncaId(ncaId).authorizedNcaIds(ncaIds)
                .gpus(Set.of(getDummyGpu())).build();
    }

    public static GpuUdt getDummyGpu() {
        return GpuUdt.builder().name("dummy_gpu_name").instanceTypes(Set.of(getDummyInstanceType()))
                .build();
    }


    private static Stream<Arguments> getAttributeFilters() {
        return Stream.of(Arguments.of(
                                 /*regions*/"dummy_region_1,dummy_region_2",
                                 /*clusters*/"azure_cluster_name,oci_cluster_name",
                                 /*gpus*/"AZURE_GPU,OCI_GPU",
                                 /*backends*/"AZURE,OCI",
                                 /*expectedAttributes*/
                                            "\"attributes\":[\"attr1\",\"attr2\",\"attr3\",\"cattr1\",\"cattr2\"]"),
                         Arguments.of(
                                 /*regions*/"dummy_region_1",
                                 /*clusters*/"azure_cluster_name,oci_cluster_name",
                                 /*gpus*/"AZURE_GPU,OCI_GPU",
                                 /*backends*/"AZURE,OCI",
                                 /*expectedAttributes*/
                                            ""), Arguments.of(
                        /*regions*/"dummy_region_1,dummy_region_2",
                        /*clusters*/"oci_cluster_name",
                        /*gpus*/"AZURE_GPU,OCI_GPU",
                        /*backends*/"AZURE,OCI",
                        /*expectedAttributes*/
                                   "\"attributes\":[\"attr2\",\"attr3\",\"cattr1\",\"cattr2\"]"),
                         Arguments.of(
                                 /*regions*/"",
                                 /*clusters*/"azure_cluster_name,oci_cluster_name",
                                 /*gpus*/"AZURE_GPU",
                                 /*backends*/"AZURE,OCI",
                                 /*expectedAttributes*/
                                            "\"attributes\":[\"attr1\",\"attr2\",\"cattr1\",\"cattr2\"]"),
                         Arguments.of(
                                 /*regions*/"",
                                 /*clusters*/"",
                                 /*gpus*/"OCI_GPU",
                                 /*backends*/"",
                                 /*expectedAttributes*/
                                            "\"attributes\":[\"attr2\",\"attr3\",\"cattr1\",\"cattr2\"]"),
                         Arguments.of(
                                 /*regions*/"",
                                 /*clusters*/"",
                                 /*gpus*/"AZURE_GPU",
                                 /*backends*/"AZURE",
                                 /*expectedAttributes*/
                                            "\"attributes\":[\"attr1\",\"attr2\",\"cattr1\",\"cattr2\"]"),
                         Arguments.of(
                                 /*regions*/"dummy_region_3",
                                 /*clusters*/"azure_cluster_name,oci_cluster_name",
                                 /*gpus*/"AZURE_GPU,OCI_GPU",
                                 /*backends*/"",
                                 /*expectedAttributes*/"\"attributes\":[]"));
    }

    private static Stream<Arguments> getRegionFilters() {
        return Stream.of(Arguments.of(
                /*clusters*/"oci_cluster_name",
                /*attributes*/"attr2",
                /*gpus*/"AZURE_GPU,OCI_GPU",
                /*backends*/"AZURE,OCI",
                /*expected*/"{\"regions\":[\"dummy_region_2\"]}"), Arguments.of(
                /*clusters*/"oci_cluster_name",
                /*attributes*/"attr2",
                /*gpus*/"AZURE_GPU,OCI_GPU",
                /*backends*/"AZURE,OCI",
                /*expected*/"{\"regions\":[\"dummy_region_2\"]}"), Arguments.of(
                /*clusters*/"azure_cluster_name,oci_cluster_name",
                /*attributes*/"attr2,attr3",
                /*gpus*/"AZURE_GPU,OCI_GPU",
                /*backends*/"",
                /*expected*/"{\"regions\":[\"dummy_region_2\"]}"), Arguments.of(
                /*clusters*/"azure_cluster_name,oci_cluster_name",
                /*attributes*/"attr3",
                /*gpus*/"",
                /*backends*/"",
                /*expected*/"{\"regions\":[\"dummy_region_2\"]}"), Arguments.of(
                /*clusters*/"",
                /*attributes*/"",
                /*gpus*/"",
                /*backends*/"AZURE,OCI",
                /*expected*/"{\"regions\":[\"dummy_region_1\",\"dummy_region_2\"]}"));
    }


    private static Stream<Arguments> getClusterFilters() {
        return Stream.of(Arguments.of(
                /*attributes*/"attr1",
                /*gpus*/"AZURE_GPU,OCI_GPU",
                /*backends*/"AZURE,OCI",
                /*expected*/
                              "{\"clusterNames\":[\"azure_cluster_name\"]}"), Arguments.of(
                /*attributes*/"attr2,attr3",
                /*gpus*/"AZURE_GPU,OCI_GPU",
                /*backends*/"AZURE,OCI",
                /*expected*/"{\"clusterNames\":[\"oci_cluster_name\"]}"), Arguments.of(
                /*attributes*/"attr2",
                /*gpus*/"AZURE_GPU",
                /*backends*/"AZURE,OCI",
                /*expected*/"{\"clusterNames\":[\"azure_cluster_name\"]}"), Arguments.of(
                /*attributes*/"",
                /*gpus*/"OCI_GPU",
                /*backends*/"",
                /*expected*/
                              "{\"clusterNames\":[\"oci_cluster_name\"]}"), Arguments.of(
                /*attributes*/"",
                /*gpus*/"",
                /*backends*/"",
                /*expected*/
                              "{\"clusterNames\":[\"azure_cluster_name\",\"oci_cluster_name\"]}"));
    }


    private void arrangeZeroCapacityAzureClusterSetup() {
        String azureClusterGroupId = UUID.randomUUID().toString();
        String azureClusterId = UUID.randomUUID().toString();

        ClusterByGroupIdAndIdEntity clusterAzure = getClusterByGroupIdAndIdEntityForAzure(
                azureClusterGroupId, azureClusterId);

        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        readyClusterEntities.add(clusterAzure);
        Mockito.when(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(DUMMY_BYOC_NCA_ID))
                .thenReturn(readyClusterEntities);
        Mockito.when(clusterTargetingHelper.getWildCardAllowedClusterCachedInfo()).thenReturn(Set.of());

        // Cloud health with 0 available GPUs (healthy zone)
        GpuCapacity zeroCapacity = GpuCapacity.builder().capacity(20).allocated(20).available(0).build();
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(DUMMY_AZURE_GPU_NAME, zeroCapacity);
        CloudHealthEntity zeroHealth = CloudHealthEntity.builder()
                .key(CloudHealthKey.builder().cloudProvider(ResourceProvider.BYOC).zone(azureClusterId).build())
                .gpuUsage(gpuUsage)
                .status(CloudHealthStatus.HEALTHY)
                .build();
        Mockito.when(clusterTargetingHelper.getAllClusterHealthInMap())
                .thenReturn(Map.of(azureClusterId, zeroHealth));
    }

    private static Stream<Arguments> capacityValidationParams() {
        String expectedWhenNoValidation = "{\"AZURE_GPU\":[{\"name\":\"AZURE.GPU.AZURE_GPU_1x\",\"value\":\"AZURE.GPU.AZURE_GPU\",\"description\":\"GPU\",\"cpuCores\":4,\"systemMemory\":\"10Gi\","
                + "\"gpuMemory\":\"20Gi\",\"gpuCount\":8,\"availableCapacity\":0,\"clusters\":[\"azure_cluster_name\"],\"regions\":[\"dummy_region_1\"],\"attributes\":[\"cattr1\",\"cattr2\",\"attr2\",\"attr1\"],"
                + "\"gpuName\":\"AZURE_GPU\",\"defaultable\":true,\"cpuArch\":null,\"os\":null,\"driverVersion\":null,\"storage\":null,\"nodeType\":\"SINGLE\"}]}";
        return Stream.of(
                Arguments.of(null, "{}"),          // omitted -> validates capacity
                Arguments.of("true", "{}"),        // explicit true -> validates capacity
                Arguments.of("false", expectedWhenNoValidation) // false -> include zero-capacity instance
        );
    }
}
