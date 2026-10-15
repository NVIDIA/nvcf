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
package com.nvidia.icms.inbound.rest.controllers.byoc;

import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.eq;

import com.amazonaws.services.sqs.model.QueueDeletedRecentlyException;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.byoc.AwsQueueAccessInfo;
import com.nvidia.icms.inbound.rest.model.byoc.BartAccessCreds;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationCredentialsResponse;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.service.ByocService;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import java.util.HashMap;
import java.util.Map;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
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
import org.springframework.web.bind.MethodArgumentNotValidException;

@ExtendWith(MockitoExtension.class)
public class ByocControllerTest extends IntegrationTest {

    private static final String BART_REGISTRATION_URL = "/v1/bart";
    private static final String BART_CREDS_URL = "/v1/bart/creds";
    private static final String CLUSTER_HEARTBEAT_URL = "/v1/bart/heartbeat";

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ObjectMapper objectMapper;

    @MockitoBean
    private ByocService service;

    @Test
    void registerCluster_success()
            throws Exception {

        // Prepare
        Map<String, Object> auditProps = new HashMap<>();
        Mockito.doReturn(BartRegistrationResponse.builder().clusterId("clusterId")
                                 .clusterGroupId("clusterGroupId").build())
                .when(service).registerCluster(Mockito.any(), eq(DUMMY_CUSTOMER_1), Mockito.any());
        String requestBodyJsonString = getClusterRegistrationBody();

        // Act
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.post(BART_REGISTRATION_URL)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .content(requestBodyJsonString)
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              JwtKeyUtils.getAuthHeader(
                                                                      DUMMY_CUSTOMER_1,
                                                                      TestUtil.BYOC_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String responseBody = mvcResult.getResponse().getContentAsString();
        BartRegistrationResponse bartRegistrationResponse =
                objectMapper.readValue(responseBody, BartRegistrationResponse.class);
        Assertions.assertEquals("clusterId", bartRegistrationResponse.getClusterId());
        Assertions.assertEquals("clusterGroupId", bartRegistrationResponse.getClusterGroupId());
        Mockito.verify(service).registerCluster(Mockito.any(), eq(DUMMY_CUSTOMER_1), Mockito.any());
    }

    @Test
    void registerCluster_gcp_success()
            throws Exception {
        // Prepare
        Map<String, Object> auditProps = new HashMap<>();
        Mockito.doReturn(BartRegistrationResponse.builder().clusterId("clusterId")
                                 .clusterGroupId("clusterGroupId").build())
                .when(service).registerCluster(Mockito.any(), eq(DUMMY_CUSTOMER_1), Mockito.any());
        BartRegistrationRequest request = objectMapper
                .readValue(getClusterRegistrationBody(), BartRegistrationRequest.class);
        request.setClusterProvider(ClusterProviderEnum.GCP);
        String requestBodyJsonString = objectMapper.writeValueAsString(request);
        // Act
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.post(BART_REGISTRATION_URL)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .content(requestBodyJsonString)
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              JwtKeyUtils.getAuthHeader(
                                                                      DUMMY_CUSTOMER_1,
                                                                      TestUtil.BYOC_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String responseBody = mvcResult.getResponse().getContentAsString();
        BartRegistrationResponse bartRegistrationResponse =
                objectMapper.readValue(responseBody, BartRegistrationResponse.class);
        Assertions.assertEquals("clusterId", bartRegistrationResponse.getClusterId());
        Assertions.assertEquals("clusterGroupId", bartRegistrationResponse.getClusterGroupId());
        Mockito.verify(service).registerCluster(Mockito.any(), eq(DUMMY_CUSTOMER_1), Mockito.any());
    }

    @Test
    void registerCluster_withEmptyValueForClusterName_throwsException()
            throws Exception {

        // Prepare
        String requestBodyJsonString = getClusterRegistrationBodyWithEmptyClusterName();

        // Act
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.post(BART_REGISTRATION_URL)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .content(requestBodyJsonString)
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              JwtKeyUtils.getAuthHeader(
                                                                      DUMMY_CUSTOMER_1,
                                                                      TestUtil.BYOC_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isBadRequest()).andReturn();

        // Assert
        Exception exception = mvcResult.getResolvedException();
        assertInstanceOf(MethodArgumentNotValidException.class, exception);
        assertTrue(exception.getMessage().contains("must not be blank"));
    }

    @Test
    void registerCluster_errorFromService_throwsError()
            throws Exception {

        // Prepare
        Map<String, Object> auditProps = new HashMap<>();
        Mockito.doThrow(new IcmsInternalServerException("dummy_internal_error"))
                .when(service).registerCluster(Mockito.any(), eq(DUMMY_CUSTOMER_1), Mockito.any());
        String requestBodyJsonString = getClusterRegistrationBody();

        // Act
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.post(BART_REGISTRATION_URL)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .content(requestBodyJsonString)
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              JwtKeyUtils.getAuthHeader(
                                                                      DUMMY_CUSTOMER_1,
                                                                      TestUtil.BYOC_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isInternalServerError()).andReturn();

        // Assert
        Exception exception = mvcResult.getResolvedException();
        assertInstanceOf(IcmsInternalServerException.class, exception);
        assertTrue(exception.getMessage().contains("dummy_internal_error"));
        Mockito.verify(service).registerCluster(Mockito.any(), eq(DUMMY_CUSTOMER_1), Mockito.any());
    }

    @Test
    void registerCluster_errorFromService_throwsQueueCreationError()
            throws Exception {

        // Prepare
        Mockito.doThrow(new QueueDeletedRecentlyException("dummy_queue_creation_error"))
                .when(service).registerCluster(Mockito.any(), eq(DUMMY_CUSTOMER_1), Mockito.any());
        String requestBodyJsonString = getClusterRegistrationBody();

        // Act
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.post(BART_REGISTRATION_URL)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .content(requestBodyJsonString)
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              JwtKeyUtils.getAuthHeader(
                                                                      DUMMY_CUSTOMER_1,
                                                                      TestUtil.BYOC_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isTooManyRequests()).andReturn();

        // Assert
        Exception exception = mvcResult.getResolvedException();
        assertInstanceOf(QueueDeletedRecentlyException.class, exception);
        assertTrue(exception.getMessage().contains("dummy_queue_creation_error"));
        Mockito.verify(service).registerCluster(Mockito.any(), eq(DUMMY_CUSTOMER_1), Mockito.any());
    }

    @Test
    void getClusterQueueCreds_success()
            throws Exception {

        // Prepare
        Mockito.doReturn(BartRegistrationCredentialsResponse.builder().credentials(
                                BartAccessCreds.builder().creationQueue(
                                                AwsQueueAccessInfo.builder().url("createUrl").build())
                                        .terminationQueue(AwsQueueAccessInfo.builder().url("terminateUrl").build())
                                        .build())
                                 .build())
                .when(service).getClusterQueuesInfo(DUMMY_CUSTOMER_ID);

        // Act
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.get(BART_CREDS_URL)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              JwtKeyUtils.getAuthHeader(
                                                                      DUMMY_CUSTOMER_ID,
                                                                      TestUtil.BYOC_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String responseBody = mvcResult.getResponse().getContentAsString();
        BartRegistrationCredentialsResponse credentialsResponse =
                objectMapper.readValue(responseBody, BartRegistrationCredentialsResponse.class);
        Assertions.assertEquals("createUrl",
                                credentialsResponse.getCredentials().getCreationQueue().getUrl());
        Assertions.assertEquals("terminateUrl",
                                credentialsResponse.getCredentials().getTerminationQueue()
                                        .getUrl());

        Mockito.verify(service).getClusterQueuesInfo(DUMMY_CUSTOMER_ID);
    }

    @Test
    void clusterHeartbeat_success()
            throws Exception {

        // Prepare
        Mockito.doNothing()
                .when(service).registerClusterHeartbeat(Mockito.any(), eq(DUMMY_CUSTOMER_ID));

        // Act
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.put(CLUSTER_HEARTBEAT_URL)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .content(getClusterHeartbeatRequestBody())
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              JwtKeyUtils.getAuthHeader(
                                                                      DUMMY_CUSTOMER_ID,
                                                                      TestUtil.CLUSTER_HEARTBEAT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        Mockito.verify(service).registerClusterHeartbeat(Mockito.any(), eq(DUMMY_CUSTOMER_ID));
    }

    private String getClusterRegistrationBody() {
        return "{\n" +
                "    \"ncaId\": \"ncaId1\",\n" +
                "    \"clusterName\": \"byoc-cluster-name-1\",\n" +
                "    \"clusterDescription\": \"Test cluster\",\n" +
                "    \"clusterGroup\": \"cluster-group-1\",\n" +
                "    \"authorizedNcaIds\": [\n" +
                "        \"ncaId1\",\n" +
                "        \"ncaId2\"\n" +
                "    ],\n" +
                "    \"clusterProvider\": \"GDN\",\n" +
                "    \"status\": \"READY\",\n" +
                "    \"k8sVersion\": \"1.26.0\",\n" +
                "\"gpus\": [\n" +
                "        {\n" +
                "            \"name\": \"DUMMY_GPU_1\",\n" +
                "            \"instanceTypes\": [\n" +
                "                {\n" +
                "                    \"name\": \"dummy_gpu_1.large\",\n" +
                "                    \"value\": \"dummy_gpu_1.large\",\n" +
                "                    \"description\": \"One Nvidia Ada GPU\",\n" +
                "                    \"default\": true,\n" +
                "                    \"cpuCores\": 8,\n" +
                "                    \"systemMemory\": \"24G\",\n" +
                "                    \"gpuCount\": 8,\n" +
                "                    \"gpuMemory\": \"28G\"\n" +
                "                }\n" +
                "            ]\n" +
                "        }\n" +
                "    ]" +
                "}";
    }

    private String getClusterRegistrationBodyWithEmptyClusterName() {
        return "{\n" +
                "    \"ncaId\": \"ncaId1\",\n" +
                "    \"clusterName\": \"\",\n" +
                "    \"clusterDescription\": \"Test cluster\",\n" +
                "    \"clusterGroup\": \"cluster-group-1\",\n" +
                "    \"authorizedNcaIds\": [\n" +
                "        \"ncaId1\",\n" +
                "        \"ncaId2\"\n" +
                "    ],\n" +
                "    \"clusterProvider\": \"GDN\",\n" +
                "    \"status\": \"READY\",\n" +
                "    \"k8sVersion\": \"1.26.0\",\n" +
                "\"gpus\": [\n" +
                "        {\n" +
                "            \"name\": \"DUMMY_GPU_1\",\n" +
                "            \"instanceTypes\": [\n" +
                "                {\n" +
                "                    \"name\": \"dummy_gpu_1.large\",\n" +
                "                    \"value\": \"dummy_gpu_1.large\",\n" +
                "                    \"description\": \"One Nvidia Ada GPU\",\n" +
                "                    \"default\": true,\n" +
                "                    \"cpuCores\": 8,\n" +
                "                    \"systemMemory\": \"24G\",\n" +
                "                    \"gpuCount\": 8,\n" +
                "                    \"gpuMemory\": \"28G\"\n" +
                "                }\n" +
                "            ]\n" +
                "        }\n" +
                "    ]" +
                "}";
    }

    private String getClusterHeartbeatRequestBody() {
        return "{\"status\":\"healthy\"}";
    }
}
