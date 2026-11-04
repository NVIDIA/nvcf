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

import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.getDummyAwsQueueAccessInfo;
import static com.nvidia.icms.util.TestUtil.getDummyNvcaClusterHeartbeatRequest;
import static com.nvidia.icms.util.TestUtil.getDummyNvcaClusterRegistrationResponse;
import static com.nvidia.icms.util.TestUtil.getDummyNvcaRegistrationRequest;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.byoc.AwsQueueAccessInfo;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaAccessCreds;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatResponse;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaHeartbeatActionResponse;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.nvca.UpdateJwksRequest;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.NvcaService;
import com.nvidia.icms.service.byoc.nvca.ClusterOidcIdentityService;
import com.nvidia.icms.service.heartbeats.NvcaHeartbeatService;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import java.util.Map;
import java.util.Optional;
import org.assertj.core.api.Assertions;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;

@ExtendWith(MockitoExtension.class)
class NvcaControllerTest extends IntegrationTest {

    private final String NVCA_REGISTRATION_URL = "/v1/nvca/clusters/{clusterId}/register";

    private final String NVCA_CREDS_RENEWAL_URL = "/v1/nvca/clusters/{clusterId}/credentials";
    private final String NVCA_RECORD_HEARTBEAT_URL = "/v1/nvca/clusters/{clusterId}/heartbeat";

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ObjectMapper objectMapper;

    @MockitoBean
    private NvcaService nvcaService;

    @MockitoBean
    private NvcaHeartbeatService nvcaHeartbeatService;

    @MockitoBean
    private ClusterRepository clusterRepository;

    @MockitoBean
    private ClusterOidcIdentityService clusterOidcIdentityService;

    @Test
    void registerCluster_success()
            throws Exception {
        // Prepare
        var request = getDummyNvcaRegistrationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        when(nvcaService.nvcaClusterRegistration(eq(request), eq(DUMMY_CLUSTER_ID),
                                                 Mockito.any())).thenReturn(
                getDummyNvcaClusterRegistrationResponse());

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.put(NVCA_REGISTRATION_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String response = mvcResult.getResponse().getContentAsString();
        NvcaRegistrationResponse nvcaRegistrationResponse =
                objectMapper.readValue(response, NvcaRegistrationResponse.class);
        assertEquals("dummy_cluster_Id", nvcaRegistrationResponse.getClusterId());
        assertEquals("dummy_cluster_group_id", nvcaRegistrationResponse.getClusterGroupId());
        assertNotNull(nvcaRegistrationResponse.getCredentials());
        assertNotNull(nvcaRegistrationResponse.getCredentials().getCreationQueue());
        assertNotNull(nvcaRegistrationResponse.getCredentials().getTerminationQueue());
        verify(nvcaService).nvcaClusterRegistration(eq(request), eq(DUMMY_CLUSTER_ID),
                                                    Mockito.any());
    }

    @Test
    void registerCluster_withK8sVersionNotProvided_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaRegistrationRequest();
        request.setK8sVersion("");
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.put(NVCA_REGISTRATION_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isBadRequest())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"k8sVersion must be provided\"}");
    }


    // Error: 500
    @Test
    void registerCluster_registrationFailed_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaRegistrationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        when(nvcaService.nvcaClusterRegistration(eq(request), eq(DUMMY_CLUSTER_ID),
                                                 Mockito.any())).thenThrow(
                new IcmsInternalServerException("dummy_exception_message"));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.put(NVCA_REGISTRATION_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isInternalServerError())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Internal Server Error\"}");
        verify(nvcaService).nvcaClusterRegistration(eq(request), eq(DUMMY_CLUSTER_ID),
                                                    Mockito.any());
    }

    // Error: 401
    @Test
    void registerCluster_invalidAuthToken_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaRegistrationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.put(NVCA_REGISTRATION_URL, DUMMY_CLUSTER_ID)
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
    void registerCluster_invalidAccess_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaRegistrationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.put(NVCA_REGISTRATION_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.SPOT_REQUEST_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    @Test
    void registerCluster_withClientIdNotMatchingWithSubFromAuthToken_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaRegistrationRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.put(NVCA_REGISTRATION_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(
                                                        DUMMY_CLUSTER_ID + "_1",
                                                        TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isConflict())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"cluster_id provided clusterId is not matching with cluster_id_1 clientId from auth token\"}");
    }

    @Test
    void renewAccessCredentials_success()
            throws Exception {
        // Prepare
        AwsQueueAccessInfo awsQueueAccessInfo = getDummyAwsQueueAccessInfo();
        Map<String, AwsQueueAccessInfo> creationQueue =
                Map.of("A100", awsQueueAccessInfo, "H100", awsQueueAccessInfo);

        NvcaAccessCreds nvcaAccessCreds = NvcaAccessCreds.builder()
                .creationQueue(creationQueue)
                .terminationQueue(awsQueueAccessInfo)
                .build();

        when(nvcaService.renewAccessCredentials(DUMMY_CLUSTER_ID)).thenReturn(nvcaAccessCreds);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(NVCA_CREDS_RENEWAL_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String response = mvcResult.getResponse().getContentAsString();
        NvcaAccessCreds credsResponse = objectMapper.readValue(response, NvcaAccessCreds.class);
        assertNotNull(credsResponse);
        assertNotNull(credsResponse.getTerminationQueue());
        assertNotNull(credsResponse.getCreationQueue());
        verify(nvcaService).renewAccessCredentials(DUMMY_CLUSTER_ID);
    }

    // Error: 500
    @Test
    void renewAccessCredentials_credRenewalFailed_throwsException()
            throws Exception {
        // Prepare
        when(nvcaService.renewAccessCredentials(DUMMY_CLUSTER_ID)).thenThrow(
                new IcmsInternalServerException("dummy_exception_message"));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(NVCA_CREDS_RENEWAL_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isInternalServerError())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Internal Server Error\"}");
        verify(nvcaService).renewAccessCredentials(DUMMY_CLUSTER_ID);
    }

    // Error: 401
    @Test
    void renewAccessCredentials_invalidAuthToken_throwsException()
            throws Exception {
        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(NVCA_CREDS_RENEWAL_URL, DUMMY_CLUSTER_ID)
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
    void renewAccessCredentials_invalidAccess_throwsException()
            throws Exception {
        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(NVCA_CREDS_RENEWAL_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.SPOT_REQUEST_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    @Test
    void renewAccessCredentials_clientIdNotMatchingWithSubFromAuthToken_throwsException()
            throws Exception {

        // Act
        MvcResult mvcResult =
                mockMvc.perform(MockMvcRequestBuilders.get(NVCA_CREDS_RENEWAL_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(
                                                        DUMMY_CLUSTER_ID + "_1",
                                                        TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isConflict())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"cluster_id provided clusterId is not matching with cluster_id_1 clientId from auth token\"}");
    }


    @Test
    void recordNvcaClusterHeartbeat_success()
            throws Exception {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        
        NvcaClusterHeartbeatResponse response = new NvcaClusterHeartbeatResponse(NvcaHeartbeatActionResponse.ACCEPTED);
        when(nvcaHeartbeatService.recordClusterHeartbeat(DUMMY_CLUSTER_ID, request)).thenReturn(response);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.post(NVCA_RECORD_HEARTBEAT_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        assertEquals(200, mvcResult.getResponse().getStatus());
        verify(nvcaHeartbeatService).recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);
        
        // Verify response content
        String responseContent = mvcResult.getResponse().getContentAsString();
        NvcaClusterHeartbeatResponse actualResponse = objectMapper.readValue(responseContent, NvcaClusterHeartbeatResponse.class);
        assertEquals(NvcaHeartbeatActionResponse.ACCEPTED, actualResponse.getAction());
    }

    // Error: 500
    @Test
    void recordNvcaClusterHeartbeat_credRenewalFailed_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        doThrow(new IcmsInternalServerException("dummy_exception_message")).when(nvcaHeartbeatService)
                .recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.post(NVCA_RECORD_HEARTBEAT_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isInternalServerError())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Internal Server Error\"}");
        verify(nvcaHeartbeatService).recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);
    }

    // Error: 401
    @Test
    void recordNvcaClusterHeartbeat_invalidAuthToken_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.post(NVCA_RECORD_HEARTBEAT_URL, DUMMY_CLUSTER_ID)
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
    void recordNvcaClusterHeartbeat_invalidAccess_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.post(NVCA_RECORD_HEARTBEAT_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.SPOT_REQUEST_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    @Test
    void recordNvcaClusterHeartbeat_clientIdNotMatchingWithSubFromAuthToken_throwsException()
            throws Exception {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.post(NVCA_RECORD_HEARTBEAT_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .content(requestBodyJsonString)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID + "_1",
                                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isConflict())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"cluster_id provided clusterId is not matching with cluster_id_1 clientId from auth token\"}");
    }

    @Test
    void recordNvcaClusterHeartbeat_psatWrongServiceAccount_returns409()
            throws Exception {
        var request = getDummyNvcaClusterHeartbeatRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        when(clusterOidcIdentityService.findByClusterId(DUMMY_CLUSTER_ID))
                .thenReturn(Optional.of(ClusterEntity.builder()
                        .clusterId(DUMMY_CLUSTER_ID)
                        .oidcIssuer("http://localhost:8082")
                        .build()));
        when(nvcaHeartbeatService.recordClusterHeartbeat(eq(DUMMY_CLUSTER_ID), eq(request)))
                .thenReturn(new NvcaClusterHeartbeatResponse(NvcaHeartbeatActionResponse.ACCEPTED));

        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.post(NVCA_RECORD_HEARTBEAT_URL, DUMMY_CLUSTER_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeaderWithAudience(
                                                "system:serviceaccount:default:malicious-sa",
                                                java.util.List.of("nvcf-icms:" + DUMMY_CLUSTER_ID),
                                                TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isConflict())
                .andReturn();

        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"PSAT token subject does not match expected service account pattern\"}");
        verify(nvcaHeartbeatService, Mockito.never())
                .recordClusterHeartbeat(Mockito.anyString(), Mockito.any());
    }

    @Test
    void recordNvcaClusterHeartbeat_psatAudienceDoesNotMatchPath_returns409()
            throws Exception {
        var request = getDummyNvcaClusterHeartbeatRequest();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        when(clusterOidcIdentityService.findByClusterId(DUMMY_CLUSTER_ID))
                .thenReturn(Optional.of(ClusterEntity.builder()
                        .clusterId(DUMMY_CLUSTER_ID)
                        .oidcIssuer("http://localhost:8082")
                        .build()));
        when(nvcaHeartbeatService.recordClusterHeartbeat(eq(DUMMY_CLUSTER_ID), eq(request)))
                .thenReturn(new NvcaClusterHeartbeatResponse(NvcaHeartbeatActionResponse.ACCEPTED));

        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.post(NVCA_RECORD_HEARTBEAT_URL, DUMMY_CLUSTER_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeaderWithAudience(
                                                "system:serviceaccount:nvca-system:nvca",
                                                java.util.List.of("nvcf-icms:other-cluster"),
                                                TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isConflict())
                .andReturn();

        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"PSAT token audience does not match path clusterId\"}");
        verify(nvcaHeartbeatService, Mockito.never())
                .recordClusterHeartbeat(Mockito.anyString(), Mockito.any());
    }


    private final String NVCA_JWKS_URL = "/v1/nvca/clusters/{clusterId}/jwks";


    @Test
    void updateClusterJwks_payloadTooLarge_returns413() throws Exception {
        String oversizedJwks = "x".repeat(65_537);
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(oversizedJwks);
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        mockMvc.perform(MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isPayloadTooLarge());
    }

    @Test
    void updateClusterJwks_payloadUnderLimit_doesNotReturn413() throws Exception {
        // Valid JWKS under size limit
        String validJwks = "{\"keys\":[]}";
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(validJwks);
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        // Cluster exists
        ClusterEntity cluster = ClusterEntity.builder()
                .clusterId(DUMMY_CLUSTER_ID)
                .build();
        when(clusterOidcIdentityService.findByClusterId(DUMMY_CLUSTER_ID))
                .thenReturn(Optional.of(cluster));

        // Should not return 413 (may fail for other reasons like JWKS parse, but not size)
        var result = mockMvc.perform(MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                          TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                .andReturn();

        // Status should NOT be 413
        org.junit.jupiter.api.Assertions.assertNotEquals(413, result.getResponse().getStatus());
    }
    @Test
    void updateClusterJwks_clusterIdMismatchWithAuthToken_returns409() throws Exception {
        // Cluster A's JWT should not be able to update Cluster B's JWKS.
        // Authenticate as DUMMY_CLUSTER_ID + "_1" but target DUMMY_CLUSTER_ID in the path.
        String validJwks = "{\"keys\":[]}";
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(validJwks);
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBody)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(
                                                DUMMY_CLUSTER_ID + "_1",
                                                TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isConflict())
                .andReturn();

        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"cluster_id provided clusterId is not matching with cluster_id_1 clientId from auth token\"}");
    }

    // --- updateClusterJwks auth surface coverage ---

    @Test
    void updateClusterJwks_noAuthHeader_returns401() throws Exception {
        // Endpoint was removed from SecurityConfiguration.web.ignoring(), so unauthenticated
        // callers must be rejected by the JWT auth filter rather than reach the handler.
        String validJwks = "{\"keys\":[]}";
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(validJwks);
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        mockMvc.perform(MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized());
    }

    @Test
    void updateClusterJwks_clusterManagementAuthority_bypassesClusterBinding() throws Exception {
        // Admin break-glass: a caller with 'cluster-management' authority (e.g. nvcf-cli
        // cluster rotate --force) must succeed even when the JWT subject is not bound to
        // the path clusterId. Subject is "admin-user" but path is DUMMY_CLUSTER_ID.
        // Also covers the rotate "issuer-in-body" path: CLI sends the freshly-probed
        // issuer alongside JWKS so SIS reflects what the client just observed rather
        // than retaining a stale value from initial registration.
        String validJwks = "{\"keys\":[]}";
        String rotatedIssuer = "https://kubernetes.default.svc.cluster.local";
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(validJwks);
        jwksRequest.setOidcIssuer(rotatedIssuer);
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        ClusterEntity cluster = ClusterEntity.builder()
                .clusterId(DUMMY_CLUSTER_ID)
                .oidcIssuer("https://stale-from-registration.example.com")
                .build();
        when(clusterOidcIdentityService.findByClusterId(DUMMY_CLUSTER_ID))
                .thenReturn(Optional.of(cluster));

        mockMvc.perform(MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(
                                        "admin-user",
                                        TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        // Rotation persists the issuer from the request body, replacing the stale
        // value previously stored on the cluster row.
        verify(clusterOidcIdentityService).updateOidcIdentity(
                eq(DUMMY_CLUSTER_ID), eq(validJwks), eq(rotatedIssuer),
                Mockito.anyString());
        verify(clusterRepository, Mockito.never())
                .updateClusterInfo(Mockito.any(ClusterEntity.class), Mockito.any(), eq(false));
    }

    @Test
    void updateClusterJwks_noIssuerInBody_keepsExistingPersistedIssuer() throws Exception {
        // Backward-compat path: older callers (or any client that only wants to rotate
        // JWKS without touching the issuer) send no oidcIssuer in the body. The
        // controller must fall back to the issuer already persisted on the cluster row
        // rather than overwriting it with null.
        String validJwks = "{\"keys\":[]}";
        String existingIssuer = "https://kubernetes.default.svc.cluster.local";
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(validJwks);
        // oidcIssuer intentionally left null
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        ClusterEntity cluster = ClusterEntity.builder()
                .clusterId(DUMMY_CLUSTER_ID)
                .oidcIssuer(existingIssuer)
                .build();
        when(clusterOidcIdentityService.findByClusterId(DUMMY_CLUSTER_ID))
                .thenReturn(Optional.of(cluster));

        mockMvc.perform(MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(
                                        "admin-user",
                                        TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        verify(clusterOidcIdentityService).updateOidcIdentity(
                eq(DUMMY_CLUSTER_ID), eq(validJwks), eq(existingIssuer),
                Mockito.anyString());
    }

    @Test
    void updateClusterJwks_blankIssuerInBody_keepsExistingPersistedIssuer() throws Exception {
        // Blank string is treated the same as omitted: no overwrite. Guards against
        // accidental JSON serialization that emits "" instead of dropping the field.
        String validJwks = "{\"keys\":[]}";
        String existingIssuer = "https://kubernetes.default.svc.cluster.local";
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(validJwks);
        jwksRequest.setOidcIssuer("   ");
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        ClusterEntity cluster = ClusterEntity.builder()
                .clusterId(DUMMY_CLUSTER_ID)
                .oidcIssuer(existingIssuer)
                .build();
        when(clusterOidcIdentityService.findByClusterId(DUMMY_CLUSTER_ID))
                .thenReturn(Optional.of(cluster));

        mockMvc.perform(MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(
                                        "admin-user",
                                        TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        verify(clusterOidcIdentityService).updateOidcIdentity(
                eq(DUMMY_CLUSTER_ID), eq(validJwks), eq(existingIssuer),
                Mockito.anyString());
    }

    @Test
    void updateClusterJwks_paddedIssuerInBody_persistsTrimmedValue() throws Exception {
        // Whitespace-padded issuer must be trimmed before persistence.
        // `validateClusterIdWithOidcWorkloadToken` compares JWT `iss` to the
        // stored issuer with exact-string equality, so a misbehaving client
        // sending `"https://issuer.example.com "` (trailing space) would break
        // auth after rotate. Verify the controller persists the trimmed value.
        String validJwks = "{\"keys\":[]}";
        String paddedIssuer = "  https://kubernetes.default.svc.cluster.local  ";
        String trimmedIssuer = "https://kubernetes.default.svc.cluster.local";
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(validJwks);
        jwksRequest.setOidcIssuer(paddedIssuer);
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        ClusterEntity cluster = ClusterEntity.builder()
                .clusterId(DUMMY_CLUSTER_ID)
                .oidcIssuer("https://stale-from-registration.example.com")
                .build();
        when(clusterOidcIdentityService.findByClusterId(DUMMY_CLUSTER_ID))
                .thenReturn(Optional.of(cluster));

        mockMvc.perform(MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(
                                        "admin-user",
                                        TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        verify(clusterOidcIdentityService).updateOidcIdentity(
                eq(DUMMY_CLUSTER_ID), eq(validJwks), eq(trimmedIssuer),
                Mockito.anyString());
    }

    @Test
    void updateClusterJwks_invalidJwksFormat_returns400() throws Exception {
        // Parse-level JWKS validation is shared with the register path (covered by
        // NvcaClusterRegistrationServiceTest#nvcaClusterRegistration_jwksUnderLimit_getsFormatError)
        // but the update path threads through a separate entry point. Lock the
        // controller-visible behavior here so a refactor that drops the JWKSet.parse
        // call before persistence surfaces as a red test, not as silently-accepted
        // malformed JWKS that later locks the cluster out of authentication.
        // Use the cluster-management admin path so we bypass the subject
        // check and reach the JWKS parser directly — the parse failure is what we
        // want to observe, not upstream auth.
        String malformedJwks = "not-valid-json-jwks";
        UpdateJwksRequest jwksRequest = new UpdateJwksRequest();
        jwksRequest.setJwks(malformedJwks);
        String requestBody = objectMapper.writeValueAsString(jwksRequest);

        ClusterEntity cluster = ClusterEntity.builder()
                .clusterId(DUMMY_CLUSTER_ID)
                .build();
        when(clusterOidcIdentityService.findByClusterId(DUMMY_CLUSTER_ID))
                .thenReturn(Optional.of(cluster));

        MvcResult result = mockMvc.perform(MockMvcRequestBuilders.put(NVCA_JWKS_URL, DUMMY_CLUSTER_ID)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(
                                        "admin-user",
                                        TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isBadRequest())
                .andReturn();

        Assertions.assertThat(result.getResponse().getContentAsString())
                .contains("Invalid JWKS format");

        // Regression guard: malformed JWKS must not be persisted anywhere.
        verify(clusterRepository, Mockito.never())
                .updateClusterInfo(Mockito.any(ClusterEntity.class), Mockito.any(), eq(false));
        verify(clusterOidcIdentityService, Mockito.never()).updateOidcIdentity(
                Mockito.anyString(), Mockito.anyString(), Mockito.anyString(),
                Mockito.anyString());
    }

    // --- Token introspection endpoint (/v1/nvca/tokens/introspect) ---

    private final String NVCA_INTROSPECT_URL = "/v1/nvca/tokens/introspect";
    private final String SI_OIDC_INTROSPECT_URL = "/v1/si/oidc/tokens/introspect";
    private final String SI_OIDC_TOKEN_INTROSPECT_URL = "/v1/si/oidc/token/introspect";

    @Test
    void introspectToken_noAuthRequired_publicEndpoint() throws Exception {
        // The introspect endpoint is registered in SecurityConfiguration.web.ignoring() and
        // must be reachable without any Authorization header. Expect a 200 response with
        // inactive=true because the bogus token has no valid audience.
        String bogusToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig";
        String requestBody = "{\"token\":\"" + bogusToken + "\"}";

        MvcResult result = mockMvc.perform(MockMvcRequestBuilders.post(NVCA_INTROSPECT_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        // Response contains "active":false since aud doesn't match nvcf-icms:<clusterId>.
        Assertions.assertThat(result.getResponse().getContentAsString())
                .contains("\"active\":false");
    }

    @Test
    void introspectToken_siOidcAlias_noAuthRequired_publicEndpoint() throws Exception {
        String bogusToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig";
        String requestBody = "{\"token\":\"" + bogusToken + "\"}";

        MvcResult result = mockMvc.perform(MockMvcRequestBuilders.post(SI_OIDC_INTROSPECT_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        Assertions.assertThat(result.getResponse().getContentAsString())
                .contains("\"active\":false");
    }

    @Test
    void introspectToken_siOidcSingularAlias_noAuthRequired_publicEndpoint() throws Exception {
        String bogusToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig";
        String requestBody = "{\"token\":\"" + bogusToken + "\"}";

        MvcResult result = mockMvc.perform(MockMvcRequestBuilders.post(SI_OIDC_TOKEN_INTROSPECT_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        Assertions.assertThat(result.getResponse().getContentAsString())
                .contains("\"active\":false");
    }

    @Test
    void introspectToken_missingAudience_returnsInactive() throws Exception {
        // Token with iss but no aud — should parse, extract no cluster ID, and return
        // inactive rather than 500.
        String claims = "{\"iss\":\"https://k8s.example.com\","
                + "\"sub\":\"system:serviceaccount:nvca-system:nvca\"}";
        String payload = java.util.Base64.getUrlEncoder().withoutPadding()
                .encodeToString(claims.getBytes(java.nio.charset.StandardCharsets.UTF_8));
        String token = "eyJhbGciOiJSUzI1NiJ9." + payload + ".signature";
        String requestBody = "{\"token\":\"" + token + "\"}";

        MvcResult result = mockMvc.perform(MockMvcRequestBuilders.post(NVCA_INTROSPECT_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        Assertions.assertThat(result.getResponse().getContentAsString())
                .contains("\"active\":false")
                .contains("Audience must contain cluster ID");
    }

    @Test
    void introspectToken_unknownCluster_returnsInactive() throws Exception {
        // Token with nvcf-icms:<clusterId> audience but no OIDC record => inactive.
        String claims = "{\"iss\":\"https://k8s.example.com\","
                + "\"sub\":\"system:serviceaccount:x:y\","
                + "\"aud\":\"nvcf-icms:unknown-cluster\"}";
        String payload = java.util.Base64.getUrlEncoder().withoutPadding()
                .encodeToString(claims.getBytes(java.nio.charset.StandardCharsets.UTF_8));
        String token = "eyJhbGciOiJSUzI1NiJ9." + payload + ".signature";
        String requestBody = "{\"token\":\"" + token + "\"}";

        when(clusterOidcIdentityService.findByClusterId("unknown-cluster"))
                .thenReturn(Optional.empty());

        MvcResult result = mockMvc.perform(MockMvcRequestBuilders.post(NVCA_INTROSPECT_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        Assertions.assertThat(result.getResponse().getContentAsString())
                .contains("\"active\":false")
                .contains("No cluster found with valid JWKS");
    }

    @Test
    void introspectToken_malformedStoredJwks_returnsInactiveWithGenericError() throws Exception {
        String clusterId = "11111111-1111-1111-1111-111111111111";
        String claims = "{\"iss\":\"https://k8s.example.com\","
                + "\"sub\":\"system:serviceaccount:nvca-system:nvca\","
                + "\"aud\":\"nvcf-icms:" + clusterId + "\"}";
        String payload = java.util.Base64.getUrlEncoder().withoutPadding()
                .encodeToString(claims.getBytes(java.nio.charset.StandardCharsets.UTF_8));
        String token = "eyJhbGciOiJSUzI1NiJ9." + payload + ".signature";
        String requestBody = "{\"token\":\"" + token + "\"}";

        when(clusterOidcIdentityService.findByClusterId(clusterId))
                .thenReturn(Optional.of(ClusterEntity.builder()
                        .clusterId(clusterId)
                        .jwks("not-valid-jwks-json")
                        .build()));

        MvcResult result = mockMvc.perform(MockMvcRequestBuilders.post(NVCA_INTROSPECT_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andReturn();

        Assertions.assertThat(result.getResponse().getContentAsString())
                .contains("\"active\":false")
                .contains("JWT verification failed")
                .doesNotContain("not-valid-jwks-json");
    }

    @Test
    void introspectToken_oversizedToken_returns431() throws Exception {
        // Tokens larger than 2048 bytes must be rejected with 431 per the size guard.
        String oversized = "x".repeat(2100);
        String requestBody = "{\"token\":\"" + oversized + "\"}";

        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_INTROSPECT_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().is(431));
    }

    // --- NATS auth-callout webhook endpoint (/v1/nvca/nats-authorize) ---

    private final String NVCA_NATS_AUTHORIZE_URL = "/v1/nvca/nats-authorize";
    private final String SI_OIDC_NATS_AUTHORIZE_URL = "/v1/si/oidc/nats-authorize";
    private final String SI_OIDC_NATS_AUTHORIZE_CAMEL_URL = "/v1/si/oidc/natsAuthorize";

    @Test
    void natsAuthorize_noAuthRequired_publicEndpoint() throws Exception {
        // Registered in SecurityConfiguration.web.ignoring() — the auth-callout plugin
        // deliberately doesn't send an Authorization header; auth comes from the
        // signed JWT in the payload field. This test asserts that with no Authorization
        // header the request reaches the controller handler (rather than getting
        // short-circuited by Spring Security's 401 entry point). Response is 400
        // because the bogus payload has no audience claim — what matters here is
        // that the controller got a chance to inspect the body at all.
        String bogusToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig";
        String requestBody = "{\"account\":\"APP\",\"pluginName\":\"oidc\","
                + "\"payload\":\"" + bogusToken + "\"}";

        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_NATS_AUTHORIZE_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isBadRequest());
    }

    @Test
    void natsAuthorize_siOidcAlias_noAuthRequired_publicEndpoint() throws Exception {
        String bogusToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig";
        String requestBody = "{\"account\":\"APP\",\"pluginName\":\"oidc\","
                + "\"payload\":\"" + bogusToken + "\"}";

        mockMvc.perform(MockMvcRequestBuilders.post(SI_OIDC_NATS_AUTHORIZE_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isBadRequest());
    }

    @Test
    void natsAuthorize_siOidcCamelAlias_noAuthRequired_publicEndpoint() throws Exception {
        String bogusToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig";
        String requestBody = "{\"account\":\"APP\",\"pluginName\":\"oidc\","
                + "\"payload\":\"" + bogusToken + "\"}";

        mockMvc.perform(MockMvcRequestBuilders.post(SI_OIDC_NATS_AUTHORIZE_CAMEL_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isBadRequest());
    }

    @Test
    void natsAuthorize_missingAudience_returns400() throws Exception {
        // Verified token contract: audience must be nvcf-icms:{clusterId}. A token
        // without it is a malformed request — INVALID_AUDIENCE maps to 400.
        String claims = "{\"iss\":\"https://k8s.example.com\","
                + "\"sub\":\"system:serviceaccount:nvca-system:nvca\"}";
        String payload = java.util.Base64.getUrlEncoder().withoutPadding()
                .encodeToString(claims.getBytes(java.nio.charset.StandardCharsets.UTF_8));
        String token = "eyJhbGciOiJSUzI1NiJ9." + payload + ".signature";
        String requestBody = "{\"account\":\"APP\",\"pluginName\":\"oidc\","
                + "\"payload\":\"" + token + "\"}";

        // INVALID_AUDIENCE is mapped to 400 (missing/malformed), not 401.
        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_NATS_AUTHORIZE_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isBadRequest());
    }

    @Test
    void natsAuthorize_unknownCluster_returns401() throws Exception {
        // Audience claims a clusterId that has no OIDC row ⇒ UNKNOWN_CLUSTER ⇒ 401.
        String claims = "{\"iss\":\"https://k8s.example.com\","
                + "\"sub\":\"system:serviceaccount:nvca-system:nvca\","
                + "\"aud\":\"nvcf-icms:11111111-1111-1111-1111-111111111111\"}";
        String payload = java.util.Base64.getUrlEncoder().withoutPadding()
                .encodeToString(claims.getBytes(java.nio.charset.StandardCharsets.UTF_8));
        String token = "eyJhbGciOiJSUzI1NiJ9." + payload + ".signature";
        String requestBody = "{\"account\":\"APP\",\"pluginName\":\"oidc\","
                + "\"payload\":\"" + token + "\"}";

        when(clusterOidcIdentityService.findByClusterId("11111111-1111-1111-1111-111111111111"))
                .thenReturn(Optional.empty());

        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_NATS_AUTHORIZE_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized());
    }

    @Test
    void natsAuthorize_oversizedToken_returns431() throws Exception {
        String oversized = "x".repeat(2100);
        String requestBody = "{\"account\":\"APP\",\"pluginName\":\"oidc\","
                + "\"payload\":\"" + oversized + "\"}";

        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_NATS_AUTHORIZE_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().is(431));
    }

    @Test
    void natsAuthorize_missingRequiredFields_returns400() throws Exception {
        // Missing `account` — @NotBlank must fire ahead of any verification.
        String requestBody = "{\"pluginName\":\"oidc\",\"payload\":\"x\"}";

        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_NATS_AUTHORIZE_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isBadRequest());
    }

    @Test
    void natsAuthorize_unexpectedAccount_returns403BeforeTokenVerification() throws Exception {
        String bogusToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig";
        String requestBody = "{\"account\":\"SYS\",\"pluginName\":\"oidc\","
                + "\"payload\":\"" + bogusToken + "\"}";

        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_NATS_AUTHORIZE_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isForbidden());
    }

    @Test
    void natsAuthorize_unexpectedPluginName_returns403BeforeTokenVerification() throws Exception {
        String bogusToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig";
        String requestBody = "{\"account\":\"APP\",\"pluginName\":\"nkey\","
                + "\"payload\":\"" + bogusToken + "\"}";

        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_NATS_AUTHORIZE_URL)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(requestBody))
                .andExpect(MockMvcResultMatchers.status().isForbidden());
    }
}
