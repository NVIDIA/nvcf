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
package com.nvidia.icms.inbound.rest.controllers.nvca;

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.getDummyClusterCreationRequest;
import static com.nvidia.icms.util.TestUtil.getDummyClusterCreationResponse;
import static com.nvidia.icms.util.TestUtil.getDummyGetClusterResponse;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationResponse;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterSource;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.service.ClusterManagementService;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import java.util.List;
import org.assertj.core.api.Assertions;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.platform.commons.util.StringUtils;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;

@ExtendWith(MockitoExtension.class)
class NvcaAccountControllerTest extends IntegrationTest {

    private final String ClUSTER_CREATION_URL = "/v1/accounts/{ncaId}/clusters";

    private final String GET_CLUSTERS_URL = "/v1/accounts/{ncaId}/clusters";

    private final String GET_ClUSTER_URL = "/v1/accounts/{ncaId}/clusters/{clusterId}";

    private final String NVCA_CLUSTER_VERSION_LISTING = "/v1/accounts/{ncaId}/clusterVersions";

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ObjectMapper objectMapper;

    @Autowired
    private NvcaConfigurationProperties nvcaConfigurationProperties;

    @MockitoBean
    private ClusterManagementService clusterManagementService;

    @Test
    void createCluster_success()
            throws Exception {
        // Prepare
        var request = getDummyClusterCreationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        when(clusterManagementService.clusterCreation(any(), eq(DUMMY_BYOC_NCA_ID),
                                                      any())).thenReturn(
                getDummyClusterCreationResponse());

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.post(ClUSTER_CREATION_URL, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String response = mvcResult.getResponse().getContentAsString();
        ClusterCreationResponse clusterCreationResponse =
                objectMapper.readValue(response, ClusterCreationResponse.class);
        assertEquals("dummy_cluster_Id", clusterCreationResponse.getClusterId());
        assertEquals("dummy_cluster_group_id", clusterCreationResponse.getClusterGroupId());
        verify(clusterManagementService).clusterCreation(eq(request), eq(DUMMY_BYOC_NCA_ID),
                                                         Mockito.any());
    }

    // Error: 500
    @Test
    void createCluster_registrationFailed_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyClusterCreationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        when(clusterManagementService.clusterCreation(eq(request), eq(DUMMY_BYOC_NCA_ID),
                                                      any())).thenThrow(
                new IcmsInternalServerException("dummy_exception_message"));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.post(ClUSTER_CREATION_URL, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isInternalServerError())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Internal Server Error\"}");
        verify(clusterManagementService).clusterCreation(eq(request), eq(DUMMY_BYOC_NCA_ID),
                                                         Mockito.any());
    }

    // Error: 401
    @Test
    void createCluster_invalidAuthToken_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyClusterCreationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.post(ClUSTER_CREATION_URL, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                "dummy_token"))
                        .andExpect(MockMvcResultMatchers.status().isUnauthorized()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Authentication failure - Full authentication is required to access this resource\"}");
    }

    // Error: 403
    @Test
    void createCluster_invalidAccess_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyClusterCreationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.post(ClUSTER_CREATION_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.SPOT_REQUEST_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    @Test
    void createCluster_blankClusterName_throwsBadRequest()
            throws Exception {
        // Prepare
        var request = getDummyClusterCreationRequest();
        request.setClusterName("");
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.post(ClUSTER_CREATION_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isBadRequest()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"clusterName must be provided\"}");
    }

    @Test
    void getCluster_success()
            throws Exception {
        // Prepare
        when(clusterManagementService.getCluster(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID)).thenReturn(
                getDummyGetClusterResponse(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_ClUSTER_URL, DUMMY_BYOC_NCA_ID,
                                                           DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String response = mvcResult.getResponse().getContentAsString();
        GetClusterResponse getClusterResponse =
                objectMapper.readValue(response, GetClusterResponse.class);
        assertNotNull(getClusterResponse);
        assertEquals(DUMMY_CLUSTER_ID, getClusterResponse.getClusterId());
        assertEquals("dummy_cluster_group_id", getClusterResponse.getClusterGroupId());
        assertEquals(ClusterSource.NGC_MANAGED.toString(), getClusterResponse.getClusterSource());
        verify(clusterManagementService).getCluster(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID);
    }

    // Error: 500
    @Test
    void getCluster_throwsInternalServerException()
            throws Exception {
        // Prepare
        when(clusterManagementService.getCluster(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID)).thenThrow(
                new IcmsInternalServerException("dummy_exception_message"));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_ClUSTER_URL, DUMMY_BYOC_NCA_ID,
                                                           DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isInternalServerError())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Internal Server Error\"}");
        verify(clusterManagementService).getCluster(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID);
    }

    // Error: 404
    @Test
    void getCluster_clusterNotFound_throwsException()
            throws Exception {
        // Prepare
        when(clusterManagementService.getCluster(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID)).thenThrow(
                new IcmsNotFoundException("dummy_cluster_not_found_exception"));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_ClUSTER_URL, DUMMY_BYOC_NCA_ID,
                                                           DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isNotFound())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"dummy_cluster_not_found_exception\"}");
        verify(clusterManagementService).getCluster(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID);
    }

    // Error: 401
    @Test
    void getCluster_invalidAuthToken_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyClusterCreationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_ClUSTER_URL, DUMMY_BYOC_NCA_ID,
                                                           DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                "dummy_token"))
                        .andExpect(MockMvcResultMatchers.status().isUnauthorized()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Authentication failure - Full authentication is required to access this resource\"}");
    }

    // Error: 403
    @Test
    void getCluster_invalidAccess_throwsException()
            throws Exception {

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_ClUSTER_URL, DUMMY_BYOC_NCA_ID,
                                                           DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.SPOT_REQUEST_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    // Clusters listing based on ncaId

    @Test
    void getClusters_success()
            throws Exception {
        // Prepare
        when(clusterManagementService.getClusters(DUMMY_BYOC_NCA_ID, null,
                null)).thenReturn(
                List.of(getDummyGetClusterResponse(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID)));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_CLUSTERS_URL, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String response = mvcResult.getResponse().getContentAsString();
        List<GetClusterResponse> getClusterResponseList =
                objectMapper.readValue(response, objectMapper.getTypeFactory()
                        .constructCollectionType(List.class, GetClusterResponse.class));
        assertNotNull(getClusterResponseList);
        assertEquals(1, getClusterResponseList.size());
        assertEquals(DUMMY_CLUSTER_ID, getClusterResponseList.get(0).getClusterId());
        assertEquals("dummy_cluster_group_id", getClusterResponseList.get(0).getClusterGroupId());
        verify(clusterManagementService).getClusters(DUMMY_BYOC_NCA_ID, null,
                null);
    }

    @Test
    void getClusters_legacyIncludeGfnParam_mapsToIncludeNonByoc()
            throws Exception {
        // Prepare
        when(clusterManagementService.getClusters(DUMMY_BYOC_NCA_ID, true, true)).thenReturn(
                List.of(getDummyGetClusterResponse(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID)));

        // Act
        mockMvc.perform(MockMvcRequestBuilders.get(GET_CLUSTERS_URL, DUMMY_BYOC_NCA_ID)
                                .param("includeAuthorizedClusters", "true")
                                .param("includeGfnInAuthorizedClusters", "true")
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        // Assert: legacy param value is forwarded as includeNonByocInAuthorizedClusters
        verify(clusterManagementService).getClusters(DUMMY_BYOC_NCA_ID, true, true);
    }

    @Test
    void getClusters_currentIncludeNonByocParam_isHonoured()
            throws Exception {
        // Prepare
        when(clusterManagementService.getClusters(DUMMY_BYOC_NCA_ID, true, true)).thenReturn(
                List.of(getDummyGetClusterResponse(DUMMY_BYOC_NCA_ID, DUMMY_CLUSTER_ID)));

        // Act
        mockMvc.perform(MockMvcRequestBuilders.get(GET_CLUSTERS_URL, DUMMY_BYOC_NCA_ID)
                                .param("includeAuthorizedClusters", "true")
                                .param("includeNonByocInAuthorizedClusters", "true")
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        // Assert
        verify(clusterManagementService).getClusters(DUMMY_BYOC_NCA_ID, true, true);
    }

    // Error: 500
    @Test
    void getClusters_throwsInternalServerException()
            throws Exception {
        // Prepare
        when(clusterManagementService.getClusters(DUMMY_BYOC_NCA_ID, null,
                null)).thenThrow(
                new IcmsInternalServerException("dummy_exception_message"));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_CLUSTERS_URL, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isInternalServerError())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Internal Server Error\"}");
        verify(clusterManagementService).getClusters(DUMMY_BYOC_NCA_ID, null,
                null);
    }

    // Error: 404
    @Test
    void getClusters_clusterNotFound_throwsException()
            throws Exception {
        // Prepare
        when(clusterManagementService.getClusters(DUMMY_BYOC_NCA_ID, null,
                null)).thenThrow(
                new IcmsNotFoundException("dummy_cluster_not_found_exception"));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_CLUSTERS_URL, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isNotFound())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"dummy_cluster_not_found_exception\"}");
        verify(clusterManagementService).getClusters(DUMMY_BYOC_NCA_ID, null,
                null);
    }

    // Error: 401
    @Test
    void getClusters_invalidAuthToken_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyClusterCreationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_CLUSTERS_URL, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                "dummy_token"))
                        .andExpect(MockMvcResultMatchers.status().isUnauthorized()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Authentication failure - Full authentication is required to access this resource\"}");
    }

    // Error: 403
    @Test
    void getClusters_invalidAccess_throwsException()
            throws Exception {

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(GET_CLUSTERS_URL, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.SPOT_REQUEST_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    // cluster version listing
    @Test
    void getClusterVersion_success()
            throws Exception {

        when(clusterManagementService.getClusterVersion(DUMMY_BYOC_NCA_ID)).thenReturn(
                nvcaConfigurationProperties.getClusterVersion());

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(NVCA_CLUSTER_VERSION_LISTING, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String response = mvcResult.getResponse().getContentAsString();
        assertNotNull(response);
        assertTrue(StringUtils.isNotBlank(response));
        verify(clusterManagementService).getClusterVersion(DUMMY_BYOC_NCA_ID);
    }

    // Error: 500
    @Test
    void getClusterVersion_throwsInternalServerException()
            throws Exception {
        // Prepare
        doThrow(new IcmsInternalServerException("dummy_error")).when(clusterManagementService)
                .getClusterVersion(DUMMY_BYOC_NCA_ID);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(NVCA_CLUSTER_VERSION_LISTING, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isInternalServerError())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Internal Server Error\"}");
        verify(clusterManagementService).getClusterVersion(DUMMY_BYOC_NCA_ID);
    }


    // Error: 401
    @Test
    void getClusterVersion_invalidAuthToken_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyClusterCreationRequest();

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(NVCA_CLUSTER_VERSION_LISTING, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                "dummy_token"))
                        .andExpect(MockMvcResultMatchers.status().isUnauthorized()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Authentication failure - Full authentication is required to access this resource\"}");
    }

    // Error: 403
    @Test
    void getClusterVersion_invalidAccess_throwsException()
            throws Exception {

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(NVCA_CLUSTER_VERSION_LISTING, DUMMY_BYOC_NCA_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                          TestUtil.SPOT_REQUEST_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }
}
