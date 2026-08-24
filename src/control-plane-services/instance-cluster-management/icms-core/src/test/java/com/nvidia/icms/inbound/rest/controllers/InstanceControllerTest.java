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
package com.nvidia.icms.inbound.rest.controllers;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.DESCRIBE_SPOT_INSTANCE_REQUESTS;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.REQUEST_INSTANCES;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.REQUEST_INSTANCES_FOR_TASK;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES_FOR_TASK;
import static com.nvidia.icms.util.JwtKeyUtils.getAuthHeader;
import static com.nvidia.icms.util.TestUtil.ACTION;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_SCOPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_TOKEN;
import static com.nvidia.icms.util.TestUtil.INSTANCE_COUNT_KEY;
import static com.nvidia.icms.util.TestUtil.LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY;
import static com.nvidia.icms.util.TestUtil.LAUNCH_SPECIFICATION_HELM_CHART_KEY;
import static com.nvidia.icms.util.TestUtil.LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY;
import static com.nvidia.icms.util.TestUtil.PUBLIC_SIS_ENDPOINT;
import static com.nvidia.icms.util.TestUtil.SPOT_INSTANCE_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.ADMIN_INSTANCE_REQUEST_DESCRIBE_SCOPE;
import static com.nvidia.icms.util.TestUtil.INSTANCE_REQUEST_SCOPE;
import static com.nvidia.icms.util.TestUtil.SPOT_REQUEST_SCOPE;
import static com.nvidia.icms.util.TestUtil.SPOT_STATE_FILTER;
import static com.nvidia.icms.util.TestUtil.getObjectMapperInstance;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.factory.RandomFactory;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.FunctionType;
import com.nvidia.icms.inbound.rest.model.GetActiveInstanceInfoResponse;
import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.InstanceInfo;
import com.nvidia.icms.inbound.rest.model.SpotInstance;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceLaunchSpecification;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceState;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.InstanceService;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashSet;
import java.util.List;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import org.assertj.core.api.Assertions;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mockito;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.mock.web.MockHttpServletResponse;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;

class InstanceControllerTest extends IntegrationTest {

    private static final ObjectMapper objectMapper = getObjectMapperInstance();

    private static final String INSTANCE_LISTING_API_URL = "/v1/si/clusters/{zoneName}/instances";
    private static final String INSTANCES_BY_DEPLOYMENT_API_URL = "/v1/si/accounts/{ncaId}/deployments/{deploymentId}/instances";

    @Autowired
    private MockMvc mockMvc;
    @MockitoBean
    private InstanceService instanceService;

    //**************************************************
    //* describeInstances
    //**************************************************

    @Test
    void describeInstances_withFetchingAllOpenRequests_returnsSuccess()
            throws Exception {

        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();
        List<SpotInstanceRequest> instanceRequestList = new ArrayList<>();
        SpotInstanceRequest instanceRequest = new SpotInstanceRequest();
        instanceRequest.setLaunchedAvailabilityZone(null);
        instanceRequest.setInstanceId(null);
        instanceRequest.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        instanceRequest.setStatus(
                new SpotInstanceRequestStatus("pending-evaluation", "open", Instant.now()));
        instanceRequest.setState(SpotInstanceRequestState.OPEN);
        instanceRequest.setSpotInstanceLaunchSpecification(
                new SpotInstanceLaunchSpecification(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE,
                                                    new SpotInstanceLaunchSpecification.Placement(
                                                            null), null, null, null, null));
        instanceRequest.setCreateTime(Instant.now());
        instanceRequestList.add(instanceRequest);
        instanceRequestsResponse.setSpotInstanceRequest(instanceRequestList);

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstanceRequests(DUMMY_CUSTOMER_1, null,
                                      Set.of(SpotInstanceRequestState.OPEN.toString(),
                                             SpotInstanceRequestState.ACTIVE.toString()));

        // Here we didn't set the SpotStateFilter hence it will return "open" and "active" requests (default)
        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction()))
                .andExpect(status().isOk()).andReturn().getResponse();

        // TODO(sparve): Validate complete response
        Assertions.assertThat(response.getContentAsString()).contains(DUMMY_REQUEST_ID);
    }

    @Test
    void describeInstances_withInvalidActionProvidedForGET_returnsError()
            throws Exception {

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION, REQUEST_SPOT_INSTANCES.getRequestAction()))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Invalid RequestSpotInstances action provided\"}");
    }

    @Test
    void describeInstances_withInvalidStateFilterProvidedForGET_returnsError()
            throws Exception {

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param(SPOT_STATE_FILTER, "dummy_filter")
                                .param(SPOT_INSTANCE_REQUEST_ID, DUMMY_REQUEST_ID))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(
                        "{\"error\":\"'dummy_filter' invalid SpotStateFilter provided\"}");
    }

    @Test
    void describeInstances_withInvalidStateFilterProvidedWithoutRequestIdsForGET_returnsError()
            throws Exception {

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param(SPOT_STATE_FILTER, "dummy_filter")
                                .param(SPOT_INSTANCE_REQUEST_ID, DUMMY_REQUEST_ID))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(
                        "{\"error\":\"'dummy_filter' invalid SpotStateFilter provided\"}");
    }

    @Test
    void describeInstances_withInvalidAuthorizationForGET_returnsError()
            throws Exception {

        HttpHeaders httpHeaders = new HttpHeaders();
        httpHeaders.add(HttpHeaders.AUTHORIZATION, getAuthHeader(DUMMY_CUSTOMER_1, DUMMY_SCOPE));
        MvcResult response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction()))
                .andExpect(status().isForbidden()).andReturn();

        Assertions.assertThat(response.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");

    }

    @Test
    void describeInstances_withInvalidAuthenticationTokenForGET_returnsError()
            throws Exception {

        HttpHeaders httpHeaders = new HttpHeaders();
        httpHeaders.add(HttpHeaders.AUTHORIZATION, DUMMY_TOKEN);
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction()))
                .andExpect(status().isUnauthorized()).andReturn();

        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Authentication failure - Full authentication is required to access this resource\"}");
    }

    @Test
    void describeInstancesPerDeployment_withFetchingAllOpenRequests_returnsSuccess()
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();

        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();
        List<SpotInstanceRequest> instanceRequestList = new ArrayList<>();
        SpotInstanceRequest instanceRequest = new SpotInstanceRequest();
        instanceRequest.setLaunchedAvailabilityZone(null);
        instanceRequest.setInstanceId(null);
        instanceRequest.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        instanceRequest.setStatus(
                new SpotInstanceRequestStatus("pending-evaluation", "open", Instant.now()));
        instanceRequest.setState(SpotInstanceRequestState.OPEN);
        instanceRequest.setSpotInstanceLaunchSpecification(
                new SpotInstanceLaunchSpecification(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE,
                                                    new SpotInstanceLaunchSpecification.Placement(
                                                            null), null, null, ncaId, null));
        instanceRequest.setCreateTime(Instant.now());
        instanceRequestList.add(instanceRequest);
        instanceRequestsResponse.setSpotInstanceRequest(instanceRequestList);

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, null, false, false);

        // Here we didn't set the SpotStateFilter hence it will return "open" request only
        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCES_BY_DEPLOYMENT_API_URL, ncaId, deploymentId)
                                .headers(httpHeaders))
                .andExpect(status().isOk()).andReturn().getResponse();

        // TODO(sparve): Validate complete response
        Assertions.assertThat(response.getContentAsString()).contains(DUMMY_REQUEST_ID);
    }

    @Test
    void describeInstancesPerDeployment_withCapacityType_returnsCapacityTypeInResponse()
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();

        // Create mock response with capacity type populated
        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();
        List<SpotInstance> instances = new ArrayList<>();
        
        SpotInstance instance = SpotInstance.builder()
                .instanceId("test-instance-1")
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .containerImage(DUMMY_CONTAINER_IMAGE)
                .spotCloudProvider(CloudProvider.AWS)
                .capacityType(CapacityType.RESERVED)
                .state(new SpotInstanceState(16, "running"))
                .placement(new SpotInstanceLaunchSpecification.Placement("us-west-2a"))
                .launchRequestId("test-request-id")
                .instanceIps(Set.of("10.0.0.1"))
                .createTime(Instant.now())
                .build();
        
        instances.add(instance);
        instanceRequestsResponse.setSpotInstances(instances);

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, null, false, false);

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCES_BY_DEPLOYMENT_API_URL, ncaId, deploymentId)
                                .headers(httpHeaders))
                .andExpect(status().isOk()).andReturn().getResponse();

        // Verify the service method was called with correct parameters
        verify(instanceService).describeInstancesByDeploymentId(ncaId, deploymentId, null, false, false);
        
        // Verify capacity type is present in the response
        String responseContent = response.getContentAsString();
        Assertions.assertThat(responseContent).contains("CapacityType");
        Assertions.assertThat(responseContent).contains("RESERVED");
        
        // Parse and verify the actual object
        GetSpotInstanceRequests actualResponse = objectMapper.readValue(responseContent, GetSpotInstanceRequests.class);
        assertNotNull(actualResponse.getSpotInstances());
        assertEquals(1, actualResponse.getSpotInstances().size());
        assertEquals(CapacityType.RESERVED, actualResponse.getSpotInstances().get(0).getCapacityType());
    }

    @Test
    void describeInstancesPerDeploymentWithGpuSpec_withValidParameters_returnsSuccess()
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();
        UUID gpuSpecId = UUID.randomUUID();

        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, gpuSpecId, false, false);

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        String url = "/v1/si/accounts/" + ncaId + "/deployments/" + deploymentId 
                + "/gpuSpecs/" + gpuSpecId + "/instances";
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(url)
                                .headers(httpHeaders))
                .andExpect(status().isOk()).andReturn().getResponse();

        // Verify the service method was called with correct parameters
        verify(instanceService).describeInstancesByDeploymentId(ncaId, deploymentId, gpuSpecId, false, false);
        Assertions.assertThat(response.getStatus()).isEqualTo(200);
    }

    @ParameterizedTest
    @ValueSource(strings = {"deployments", "tasks", "workloads"})
    void describeInstancesPerWorkloadAlias_routesToDeploymentLookup(String workloadPath)
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID workloadId = UUID.randomUUID();

        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, workloadId, null, false, false);

        String url = "/v1/si/accounts/" + ncaId + "/" + workloadPath + "/" + workloadId
                + "/instances";

        mockMvc.perform(MockMvcRequestBuilders.get(url)
                        .headers(generateAuthorizationHeader()))
                .andExpect(status().isOk());

        verify(instanceService).describeInstancesByDeploymentId(
                ncaId, workloadId, null, false, false);
    }

    @ParameterizedTest
    @ValueSource(strings = {"deployments", "tasks", "workloads"})
    void describeInstancesPerWorkloadAliasWithGpuSpec_routesToDeploymentLookup(
            String workloadPath)
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID workloadId = UUID.randomUUID();
        UUID gpuSpecId = UUID.randomUUID();

        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, workloadId, gpuSpecId, false, false);

        String url = "/v1/si/accounts/" + ncaId + "/" + workloadPath + "/" + workloadId
                + "/gpuSpecs/" + gpuSpecId + "/instances";

        mockMvc.perform(MockMvcRequestBuilders.get(url)
                        .headers(generateAuthorizationHeader()))
                .andExpect(status().isOk());

        verify(instanceService).describeInstancesByDeploymentId(
                ncaId, workloadId, gpuSpecId, false, false);
    }

    @Test
    void describeInstancesPerDeployment_withExpiredAckedInstancesFalse_passesFlagThrough()
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();

        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, null, false, false);

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCES_BY_DEPLOYMENT_API_URL, ncaId, deploymentId)
                                .headers(httpHeaders)
                                .param("ExpiredAckedInstances", "false"))
                .andExpect(status().isOk());

        verify(instanceService).describeInstancesByDeploymentId(ncaId, deploymentId, null, false, false);
    }

    @Test
    void describeInstancesPerDeployment_withExpiredAckedInstancesTrue_passesFlagThrough()
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();

        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, null, false, true);

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCES_BY_DEPLOYMENT_API_URL, ncaId, deploymentId)
                                .headers(httpHeaders)
                                .param("ExpiredAckedInstances", "true"))
                .andExpect(status().isOk());

        verify(instanceService).describeInstancesByDeploymentId(ncaId, deploymentId, null, false, true);
    }

    @Test
    void describeInstancesPerDeployment_withIncludeTerminatedTrue_passesFlagThrough()
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();

        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, null, true, false);

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCES_BY_DEPLOYMENT_API_URL, ncaId, deploymentId)
                                .headers(httpHeaders)
                                .param("IncludeTerminated", "true"))
                .andExpect(status().isOk());

        verify(instanceService).describeInstancesByDeploymentId(ncaId, deploymentId, null, true, false);
    }

    @Test
    void describeInstancesPerDeployment_whenServiceThrowsNotFound_returns404()
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();

        doThrow(new IcmsNotFoundException(
                "No instance requests found for deploymentId " + deploymentId))
                .when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, null, false, false);

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCES_BY_DEPLOYMENT_API_URL, ncaId, deploymentId)
                                .headers(httpHeaders))
                .andExpect(status().isNotFound());
    }

    //**************************************************
    //* cancelInstanceRequests
    //**************************************************
    @Test
    void cancelInstanceRequests_withValidInputs_returnsSuccess()
            throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId",
                                       String.format("%s_1,%s_2,%s_3", DUMMY_REQUEST_ID,
                                                     DUMMY_REQUEST_ID,
                                                     DUMMY_REQUEST_ID)))
                .andExpect(status().isOk());
    }

    @Test
    void cancelInstanceRequests_withInvalidActionProvided_returnsError()
            throws Exception {
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .headers(generateAuthorizationHeader())
                                                      .param(ACTION, "invalid_action")
                                                      .param("SpotInstanceRequestId",
                                                             String.format("%s_1,%s_2,%s_3",
                                                                           DUMMY_REQUEST_ID,
                                                                           DUMMY_REQUEST_ID,
                                                                           DUMMY_REQUEST_ID)))
                .andExpect(status().isBadRequest()).andReturn();

        Exception exception = mvcResult.getResolvedException();
        Assertions.assertThat(exception instanceof IcmsHttpUnifiedErrorException).isTrue();
        Assertions.assertThat(exception.getMessage())
                .contains("Invalid invalid_action action provided");

        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Invalid invalid_action action provided\"}");
    }

    @Test
    void cancelInstanceRequests_withInvalidAuthenticationToken_returnsError()
            throws Exception {
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              DUMMY_TOKEN)
                                                      .param(ACTION,
                                                             SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                                      .param("SpotInstanceRequestId",
                                                             String.format("%s_1,%s_2,%s_3",
                                                                           DUMMY_REQUEST_ID,
                                                                           DUMMY_REQUEST_ID,
                                                                           DUMMY_REQUEST_ID)))
                .andExpect(status().isUnauthorized()).andReturn();

        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Authentication failure - Full authentication is required to access this resource\"}");
    }

    @Test
    void cancelInstanceRequests_withInvalidAuthorization_returnsError()
            throws Exception {
        MvcResult response = mockMvc.perform(MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                                     .contentType(MediaType.APPLICATION_JSON)
                                                     .header(HttpHeaders.AUTHORIZATION,
                                                             getAuthHeader(DUMMY_CUSTOMER_1,
                                                                           DUMMY_SCOPE))
                                                     .param(ACTION,
                                                            SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                                     .param("SpotInstanceRequestId",
                                                            String.format("%s_1,%s_2,%s_3",
                                                                          DUMMY_REQUEST_ID,
                                                                          DUMMY_REQUEST_ID,
                                                                          DUMMY_REQUEST_ID)))
                .andExpect(status().isForbidden()).andReturn();

        Assertions.assertThat(response.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    //**************************************************
    //* terminateInstances
    //**************************************************
    @Test
    void terminateInstances_withValidInputs_returnsSuccess()
            throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_INSTANCES.getRequestAction())
                                .param("InstanceId",
                                       String.format("%s_1,%s_2,%s_3", DUMMY_INSTANCE_ID,
                                                     DUMMY_INSTANCE_ID,
                                                     DUMMY_INSTANCE_ID)))
                .andExpect(status().isOk());
    }

    @Test
    void terminateInstances_withInstanceIdsProvidedAndValidInputs_returnsSuccess_verifyResponse()
            throws Exception {

        TerminateInstancesResponse terminateInstancesResponse = new TerminateInstancesResponse();
        List<TerminateInstancesResponse.TerminatingInstance> terminatingInstanceList =
                new ArrayList<>();
        TerminateInstancesResponse.TerminatingInstance terminatingInstance1 =
                new TerminateInstancesResponse.TerminatingInstance();
        terminatingInstance1.setInstanceId(DUMMY_INSTANCE_ID + "_1");
        terminatingInstance1.setCurrentState(new SpotInstanceState(32, "terminated"));
        terminatingInstance1.setPreviousState(new SpotInstanceState(16, "running"));
        TerminateInstancesResponse.TerminatingInstance terminatingInstance2 =
                new TerminateInstancesResponse.TerminatingInstance();
        terminatingInstance2.setInstanceId(DUMMY_INSTANCE_ID + "_2");
        terminatingInstance2.setCurrentState(new SpotInstanceState(32, "terminated"));
        terminatingInstance2.setPreviousState(new SpotInstanceState(16, "running"));
        terminatingInstanceList.add(terminatingInstance1);
        terminatingInstanceList.add(terminatingInstance2);
        terminateInstancesResponse.setTerminatingInstances(terminatingInstanceList);

        doReturn(terminateInstancesResponse).when(instanceService)
                .terminateInstances(eq(DUMMY_CUSTOMER_1),
                                         eq(Set.of(DUMMY_INSTANCE_ID + "_1",
                                                   DUMMY_INSTANCE_ID + "_2")),
                                         Mockito.any());

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_INSTANCES.getRequestAction())
                                .param("InstanceId",
                                       String.format("%s_1,%s_2", DUMMY_INSTANCE_ID,
                                                     DUMMY_INSTANCE_ID)))
                .andExpect(status().isOk()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(getObjectMapperInstance()
                                   .writeValueAsString(terminateInstancesResponse));
    }

    @Test
    void terminateInstances_withRequestIdsProvidedAndValidInputs_returnsSuccess_verifyResponse()
            throws Exception {

        TerminateInstancesResponse terminateInstancesResponse = new TerminateInstancesResponse();
        List<TerminateInstancesResponse.TerminatingInstance> terminatingInstanceList =
                new ArrayList<>();

        // Below instance was running and marked for termination
        TerminateInstancesResponse.TerminatingInstance terminatingInstance1 =
                new TerminateInstancesResponse.TerminatingInstance();
        terminatingInstance1.setInstanceId(DUMMY_INSTANCE_ID + "_1");
        terminatingInstance1.setCurrentState(new SpotInstanceState(32, "shutting-down"));
        terminatingInstance1.setPreviousState(new SpotInstanceState(16, "running"));

        // Below instances was already in shutting-down state
        TerminateInstancesResponse.TerminatingInstance terminatingInstance2 =
                new TerminateInstancesResponse.TerminatingInstance();
        terminatingInstance2.setInstanceId(DUMMY_INSTANCE_ID + "_2");
        terminatingInstance2.setCurrentState(new SpotInstanceState(32, "shutting-down"));
        terminatingInstance2.setCurrentState(new SpotInstanceState(32, "shutting-down"));
        terminatingInstanceList.add(terminatingInstance1);
        terminatingInstanceList.add(terminatingInstance2);
        terminateInstancesResponse.setTerminatingInstances(terminatingInstanceList);

        doReturn(terminateInstancesResponse).when(instanceService)
                .terminateInstanceRequests(eq(DUMMY_CUSTOMER_1),
                                        eq(Set.of(DUMMY_REQUEST_ID)), Mockito.any());

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_SPOT_INSTANCE_REQUEST.getRequestAction())
                                .param("RequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(getObjectMapperInstance()
                                   .writeValueAsString(terminateInstancesResponse));
    }

    @ParameterizedTest
    @ValueSource(strings = {"deployments", "tasks", "workloads"})
    void terminateInstancesPerWorkloadAlias_routesToDeploymentTermination(String workloadPath)
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID workloadId = UUID.randomUUID();
        TerminateInstancesResponse terminateInstancesResponse = new TerminateInstancesResponse();

        doReturn(terminateInstancesResponse).when(instanceService)
                .instanceDeploymentTermination(eq(ncaId), eq(workloadId), eq(null), Mockito.any());

        String url = "/v1/si/accounts/" + ncaId + "/" + workloadPath + "/" + workloadId;

        mockMvc.perform(MockMvcRequestBuilders.delete(url)
                        .headers(generateAuthorizationHeader()))
                .andExpect(status().isOk());

        verify(instanceService).instanceDeploymentTermination(
                eq(ncaId), eq(workloadId), eq(null), Mockito.any());
    }

    @ParameterizedTest
    @ValueSource(strings = {"deployments", "tasks", "workloads"})
    void terminateInstancesPerWorkloadAliasWithGpuSpec_routesToDeploymentTermination(
            String workloadPath)
            throws Exception {

        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID workloadId = UUID.randomUUID();
        UUID gpuSpecId = UUID.randomUUID();
        TerminateInstancesResponse terminateInstancesResponse = new TerminateInstancesResponse();

        doReturn(terminateInstancesResponse).when(instanceService)
                .instanceDeploymentTermination(eq(ncaId), eq(workloadId), eq(gpuSpecId),
                                           Mockito.any());

        String url = "/v1/si/accounts/" + ncaId + "/" + workloadPath + "/" + workloadId
                + "/gpuSpecs/" + gpuSpecId;

        mockMvc.perform(MockMvcRequestBuilders.delete(url)
                        .headers(generateAuthorizationHeader()))
                .andExpect(status().isOk());

        verify(instanceService).instanceDeploymentTermination(
                eq(ncaId), eq(workloadId), eq(gpuSpecId), Mockito.any());
    }

    @Test
    void terminateInstances_withInstanceIdsNotProvided_returnsError()
            throws Exception {
        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_INSTANCES.getRequestAction()))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(
                        "{\"error\":\"InstanceId should be provided with TerminateInstances action\"}");
    }

    @Test
    void terminateInstances_withRequestIdsNotProvided_returnsError()
            throws Exception {
        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_SPOT_INSTANCE_REQUEST.getRequestAction()))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(
                        "{\"error\":\"RequestId should be provided with TerminateSpotInstanceRequest action\"}");
    }

    @Test
    void terminateInstances_withInvalidActionProvided_returnsError()
            throws Exception {
        MvcResult mvcResult = mockMvc.perform(MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                                      .contentType(MediaType.APPLICATION_JSON)
                                                      .header(HttpHeaders.AUTHORIZATION,
                                                              getAuthHeader(DUMMY_CUSTOMER_1,
                                                                            SPOT_REQUEST_SCOPE))
                                                      .param(ACTION,
                                                             REQUEST_SPOT_INSTANCES.getRequestAction())
                                                      .param("InstanceId",
                                                             String.format("%s_1,%s_2,%s_3",
                                                                           DUMMY_INSTANCE_ID,
                                                                           DUMMY_INSTANCE_ID,
                                                                           DUMMY_INSTANCE_ID)))
                .andExpect(status().isBadRequest()).andReturn();

        Exception exception = mvcResult.getResolvedException();
        Assertions.assertThat(exception instanceof IcmsHttpUnifiedErrorException).isTrue();
        Assertions.assertThat(exception.getMessage())
                .contains("Invalid RequestSpotInstances action provided");

        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Invalid RequestSpotInstances action provided\"}");
    }

    @Test
    void terminateInstances_withInvalidAuthenticationToken_returnsError()
            throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION, DUMMY_TOKEN)
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_INSTANCES.getRequestAction())
                                .param("InstanceId",
                                       String.format("%s_1,%s_2,%s_3", DUMMY_INSTANCE_ID,
                                                     DUMMY_INSTANCE_ID,
                                                     DUMMY_INSTANCE_ID)))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void terminateInstances_withInvalidAuthorization_returnsError()
            throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1, DUMMY_SCOPE))
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_INSTANCES.getRequestAction())
                                .param("InstanceId",
                                       String.format("%s_1,%s_2,%s_3", DUMMY_INSTANCE_ID,
                                                     DUMMY_INSTANCE_ID,
                                                     DUMMY_INSTANCE_ID)))
                .andExpect(status().isForbidden());
    }

    @ParameterizedTest
    @EnumSource(FunctionType.class)
    void requestInstances_withSupportedFunctionType_returnsSuccess(
            FunctionType functionType) throws Exception {

        UUID requestId = UUID.randomUUID();
        HttpHeaders httpHeaders = generateAuthorizationHeader();
        var createResponse = new CreateSpotInstancesResponse(requestId);

        SpotInstanceRequestSchema instanceRequestSchema = createDefaultInstanceRequestSchema();
        instanceRequestSchema.setFunctionType(functionType);

        doReturn(createResponse).when(instanceService).requestInstances(eq(DUMMY_CUSTOMER_1),
                                                                        eq(instanceRequestSchema),
                                                                        Mockito.any());
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION, REQUEST_SPOT_INSTANCES.getRequestAction())
                                .param(INSTANCE_COUNT_KEY, "1")
                                .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE)
                                .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, DUMMY_CONTAINER_IMAGE)
                                .param("LaunchSpecification.Environment", DUMMY_ENVIRONMENT_VALUE)
                                .param("LaunchSpecification.Placement.AvailabilityZone", "availabilityZone")
                                .param("LaunchSpecification.Backend", "backend")
                                .param("LaunchSpecification.Gpu", "gpu")
                                .param("LaunchSpecification.NcaId", "ncaId")
                                .param("LaunchSpecification.CacheArtifacts", "true")
                                .param("LaunchSpecification.CacheHandle", "cachingHandle")
                                .param("FunctionDetails.FunctionName", "functionName")
                                .param("FunctionDetails.FunctionType", functionType.name()))
                .andExpect(status().isAccepted()).andReturn().getResponse();
        String jsonString = objectMapper.writeValueAsString(createResponse);
        assertThat(jsonString).isEqualTo(response.getContentAsString());
    }

    //**************************************************
    //* requestInstances
    //**************************************************

    @Test
    void requestInstances_withValidInputAndHelmChart_returnsSuccess()
            throws Exception {

        UUID requestId = UUID.randomUUID();
        HttpHeaders httpHeaders = generateAuthorizationHeader();
        var createResponse = new CreateSpotInstancesResponse(requestId);

        doReturn(createResponse).when(instanceService)
                .requestInstances(eq(DUMMY_CUSTOMER_1), Mockito.any(), Mockito.any());

        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION, REQUEST_SPOT_INSTANCES.getRequestAction())
                                .param(INSTANCE_COUNT_KEY, "1")
                                .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE)
                                .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, DUMMY_CONTAINER_IMAGE)
                                .param(LAUNCH_SPECIFICATION_HELM_CHART_KEY, "helmChart")
                                .param("LaunchSpecification.Environment", DUMMY_ENVIRONMENT_VALUE)
                                .param("LaunchSpecification.Placement.AvailabilityZone", "availabilityZone")
                                .param("LaunchSpecification.Backend", "backend")
                                .param("LaunchSpecification.Gpu", "gpu")
                                .param("LaunchSpecification.NcaId", "ncaId"))

                .andExpect(status().isAccepted()).andReturn().getResponse();
        String jsonString = objectMapper.writeValueAsString(createResponse);
        assertThat(jsonString).isEqualTo(response.getContentAsString());
    }

    @Test
    void requestInstances_withInvalidInstanceCount_returnsError()
            throws Exception {

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION, REQUEST_SPOT_INSTANCES.getRequestAction())
                                .param(INSTANCE_COUNT_KEY, "0")
                                .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, "dummy_gpu_4.small")
                                .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, "dummy_container_image"))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(
                        "{\"error\":\"Instance count must be positive. Provided 0\"}");
    }

    @Test
    void requestInstances_withCacheArtifactAsFalseProvided_returnsSuccessWithModelCachingIgnored()
            throws Exception {

        UUID requestId = UUID.randomUUID();

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        var createResponse = new CreateSpotInstancesResponse(requestId);

        SpotInstanceRequestSchema instanceRequestSchema = createDefaultInstanceRequestSchema();
        instanceRequestSchema.setCacheArtifacts(false);
        instanceRequestSchema.setCacheHandle(null);

        doReturn(createResponse).when(instanceService)
                .requestInstances(eq(DUMMY_CUSTOMER_1), eq(instanceRequestSchema),
                                      Mockito.any());

        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION, REQUEST_SPOT_INSTANCES.getRequestAction())
                                .param(INSTANCE_COUNT_KEY, "1")
                                .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE)
                                .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, DUMMY_CONTAINER_IMAGE)
                                .param("LaunchSpecification.Environment", DUMMY_ENVIRONMENT_VALUE)
                                .param("LaunchSpecification.Placement.AvailabilityZone", "availabilityZone")
                                .param("LaunchSpecification.Backend", "backend")
                                .param("LaunchSpecification.Gpu", "gpu")
                                .param("LaunchSpecification.NcaId", "ncaId")
                                .param("FunctionDetails.FunctionName", "functionName")
                                .param("LaunchSpecification.CacheHandle", "cachingHandle"))
                .andExpect(status().isAccepted()).andReturn().getResponse();

        String jsonString = objectMapper.writeValueAsString(createResponse);

        assertThat(response.getContentAsString()).isEqualTo(jsonString);
    }

    @Test
    void requestInstances_withCacheHandleNotProvidedAndCacheArtifactIsProvided_returnsError()
            throws Exception {

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION, REQUEST_SPOT_INSTANCES.getRequestAction())
                                .param(INSTANCE_COUNT_KEY, "1")
                                .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, "dummy_gpu_4.small")
                                .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, "dummy_container_image")
                                .param("LaunchSpecification.CacheArtifacts", "True"))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(
                        "{\"error\":\"LaunchSpecification.CacheHandle must be provided when LaunchSpecification.CacheArtifacts is true\"}");
    }

    @ParameterizedTest()
    @ValueSource(strings = {"null", "", "Request_Spot_Instances", "requestInstances"})
    void requestInstances_withInvalidActionProvided_returnsError(String action)
            throws Exception {

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION, Objects.equals(action, "null") ? null : action)
                                .param(INSTANCE_COUNT_KEY, "1")
                                .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, "dummy_gpu_4.small")
                                .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, "dummy_container_image"))
                .andExpect(status().isBadRequest())
                .andReturn()
                .getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(String.format("{\"error\":\"Invalid %s action provided\"}", action));
    }

    @ParameterizedTest()
    @MethodSource("getActionsExceptRequestInstances")
    void requestInstances_withIncorrectActionProvided_returnsError(
            SpotInstanceRequestAction action)
            throws Exception {

        HttpHeaders httpHeaders = generateAuthorizationHeader();
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                                .headers(httpHeaders)
                                .param(ACTION, action.getRequestAction())
                                .param(INSTANCE_COUNT_KEY, "1")
                                .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, "dummy_gpu_4.small")
                                .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, "dummy_container_image"))
                .andExpect(status().isBadRequest())
                .andReturn()
                .getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(String.format("{\"error\":\"Not allowed %s action provided\"}",
                                         action.getRequestAction()));
    }

    //**************************************************
    //* getActiveInstancesForZone
    //**************************************************

    // Instance listing
    @ParameterizedTest
    @ValueSource(strings = {
            TestUtil.CLUSTER_INSTANCES_SCOPE,
            TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE
    })
    void getActiveInstancesForZone_withSupportedScope_returnsSuccess(String scope)
            throws Exception {
        // Prepare
        GetActiveInstanceInfoResponse mockedResponse = GetActiveInstanceInfoResponse.builder()
                .instances(List.of(InstanceInfo.builder()
                                           .requestId(DUMMY_REQUEST_ID)
                                           .instanceId(DUMMY_INSTANCE_ID)
                                           .instanceState(
                                                   SpotInstanceInternalState.RUNNING.getStateName())
                                           .build()))
                .build();

        when(instanceService.getActiveInstancesForZone(DUMMY_CLUSTER_ID)).thenReturn(mockedResponse);

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(INSTANCE_LISTING_API_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          scope)))
                        .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();

        // Assert
        String response = mvcResult.getResponse().getContentAsString();
        GetActiveInstanceInfoResponse activeInstanceInfoResponse = objectMapper.readValue(response,
                                                                                          GetActiveInstanceInfoResponse.class);
        assertNotNull(activeInstanceInfoResponse);
        assertNotEquals(0, activeInstanceInfoResponse.getInstances().size());
    }

    // Error: 500
    @Test
    void getActiveInstancesForZone_instanceFetchingFailed_throwsException()
            throws Exception {
        // Prepare
        when(instanceService.getActiveInstancesForZone(DUMMY_CLUSTER_ID)).thenThrow(
                new IcmsInternalServerException("dummy_exception_message"));

        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(INSTANCE_LISTING_API_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.CLUSTER_INSTANCES_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isInternalServerError())
                        .andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Internal Server Error\"}");
        verify(instanceService).getActiveInstancesForZone(DUMMY_CLUSTER_ID);
    }

    // Error: 403 - the cluster registration scope no longer grants instance listing
    @Test
    void getActiveInstancesForZone_withClusterRegistrationScope_throwsException()
            throws Exception {
        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(INSTANCE_LISTING_API_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .header(HttpHeaders.AUTHORIZATION,
                                                JwtKeyUtils.getAuthHeader(DUMMY_CLUSTER_ID,
                                                                          TestUtil.NON_BYOC_CLUSTER_REGISTRATION_SCOPE)))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    // Error: 401
    @Test
    void getActiveInstancesForZone_invalidAuthToken_throwsException()
            throws Exception {
        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(INSTANCE_LISTING_API_URL, DUMMY_CLUSTER_ID)
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
    void getActiveInstancesForZone_invalidAccess_throwsException()
            throws Exception {
        // Act
        MvcResult mvcResult =
                mockMvc.perform(
                                MockMvcRequestBuilders.get(INSTANCE_LISTING_API_URL, DUMMY_CLUSTER_ID)
                                        .contentType(MediaType.APPLICATION_JSON)
                                        .headers(generateAuthorizationHeader()))
                        .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();

        // Assert
        Assertions.assertThat(mvcResult.getResponse().getContentAsString())
                .isEqualTo("{\"error\":\"Access Denied\"}");
    }

    @Test
    void requestInstances_withInstanceRequestScope_returnsSuccess() throws Exception {
        UUID requestId = UUID.randomUUID();
        var createResponse = new CreateSpotInstancesResponse(requestId);
        doReturn(createResponse).when(instanceService)
                .requestInstances(eq(DUMMY_CUSTOMER_1), Mockito.any(), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1, INSTANCE_REQUEST_SCOPE))
                                .param(ACTION, REQUEST_SPOT_INSTANCES.getRequestAction())
                                .param(INSTANCE_COUNT_KEY, "1")
                                .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE)
                                .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, DUMMY_CONTAINER_IMAGE))
                .andExpect(status().isAccepted());
    }

    @Test
    void describeInstances_withInstanceRequestScope_returnsSuccess() throws Exception {
        doReturn(new GetSpotInstanceRequests()).when(instanceService)
                .describeInstanceRequests(eq(DUMMY_CUSTOMER_1), Mockito.any(), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1, INSTANCE_REQUEST_SCOPE))
                                .param(ACTION, DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction()))
                .andExpect(status().isOk());
    }

    @Test
    void cancelInstanceRequests_withInstanceRequestScope_returnsSuccess() throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1, INSTANCE_REQUEST_SCOPE))
                                .param(ACTION,
                                       SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk());
    }

    @Test
    void terminateInstances_withInstanceRequestScope_returnsSuccess() throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1, INSTANCE_REQUEST_SCOPE))
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_INSTANCES.getRequestAction())
                                .param("InstanceId", DUMMY_INSTANCE_ID))
                .andExpect(status().isOk());
    }

    //**************************************************
    //* New scope: admin:instance-request:describe
    //**************************************************

    @Test
    void describeAdminInstanceRequests_withLegacyAdminRequestDescribeScope_returnsSuccess()
            throws Exception {
        doReturn(new GetSpotInstanceRequests()).when(instanceService)
                .describeAdminInstanceRequests(Mockito.any(), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT + "/admin")
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1,
                                                      "admin:spot-request:describe"))
                                .param(ACTION, DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk());
    }

    @Test
    void describeAdminInstanceRequests_withAdminInstanceRequestDescribeScope_returnsSuccess()
            throws Exception {
        doReturn(new GetSpotInstanceRequests()).when(instanceService)
                .describeAdminInstanceRequests(Mockito.any(), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT + "/admin")
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1,
                                                      ADMIN_INSTANCE_REQUEST_DESCRIBE_SCOPE))
                                .param(ACTION, DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk());
    }

    @Test
    void describeAdminInstanceRequests_withInvalidScope_returnsForbidden() throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT + "/admin")
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1, DUMMY_SCOPE))
                                .param(ACTION, DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isForbidden());
    }

    @Test
    void requestInstances_withRequestInstancesAction_returnsSuccessWithConciseJson()
            throws Exception {
        UUID requestId = UUID.randomUUID();
        var createResponse = new CreateSpotInstancesResponse(requestId);
        doReturn(createResponse).when(instanceService)
                .requestInstances(eq(DUMMY_CUSTOMER_1), Mockito.any(), Mockito.any());

        MockHttpServletResponse response = mockMvc.perform(MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                        .headers(generateAuthorizationHeader())
                        .param(ACTION, REQUEST_INSTANCES.getRequestAction())
                        .param(INSTANCE_COUNT_KEY, "1")
                        .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, DUMMY_CONTAINER_IMAGE))
                .andExpect(status().isAccepted()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .contains(requestId.toString())
                .doesNotContain("Spot");
    }

    @Test
    void requestInstancesForTask_withRequestInstancesForTaskAction_returnsSuccess()
            throws Exception {
        UUID requestId = UUID.randomUUID();
        var createResponse = new CreateSpotInstancesResponse(requestId);
        doReturn(createResponse).when(instanceService)
                .requestInstances(eq(DUMMY_CUSTOMER_1), Mockito.any(), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.post(PUBLIC_SIS_ENDPOINT)
                        .headers(generateAuthorizationHeader())
                        .param(ACTION, REQUEST_INSTANCES_FOR_TASK.getRequestAction())
                        .param(INSTANCE_COUNT_KEY, "1")
                        .param(LAUNCH_SPECIFICATION_INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .param(LAUNCH_SPECIFICATION_CONTAINER_IMAGE_KEY, DUMMY_CONTAINER_IMAGE))
                .andExpect(status().isAccepted());
    }

    @Test
    void describeInstanceRequests_withDescribeInstanceRequestsAction_returnsConciseJson()
            throws Exception {
        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();
        List<SpotInstanceRequest> instanceRequestList = new ArrayList<>();
        SpotInstanceRequest instanceRequest = new SpotInstanceRequest();
        instanceRequest.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        instanceRequest.setSpotCloudProvider(CloudProvider.AWS);
        instanceRequest.setStatus(
                new SpotInstanceRequestStatus("pending-evaluation", "open", Instant.now()));
        instanceRequest.setState(SpotInstanceRequestState.OPEN);
        instanceRequest.setSpotInstanceLaunchSpecification(
                new SpotInstanceLaunchSpecification(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE,
                                                    new SpotInstanceLaunchSpecification.Placement(null),
                                                    null, null, null, null));
        instanceRequest.setCreateTime(Instant.now());
        instanceRequestList.add(instanceRequest);
        instanceRequestsResponse.setSpotInstanceRequest(instanceRequestList);

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstanceRequests(eq(DUMMY_CUSTOMER_1), Mockito.any(), Mockito.any());

        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.DESCRIBE_INSTANCE_REQUESTS.getRequestAction()))
                .andExpect(status().isOk()).andReturn().getResponse();

        String body = response.getContentAsString();
        Assertions.assertThat(body)
                .contains("\"InstanceRequests\"")
                .contains("\"InstanceRequestId\"")
                .contains("\"CloudProvider\"")
                .contains(DUMMY_REQUEST_ID)
                .doesNotContain("\"SpotInstanceRequests\"")
                .doesNotContain("\"SpotInstanceRequestId\"")
                .doesNotContain("\"SpotCloudProvider\"");
    }

    @Test
    void describeInstances_withLegacyDescribeRequestsAction_returnsLegacyJson()
            throws Exception {
        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();
        List<SpotInstanceRequest> instanceRequestList = new ArrayList<>();
        SpotInstanceRequest instanceRequest = new SpotInstanceRequest();
        instanceRequest.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        instanceRequest.setSpotCloudProvider(CloudProvider.AWS);
        instanceRequest.setStatus(
                new SpotInstanceRequestStatus("pending-evaluation", "open", Instant.now()));
        instanceRequest.setState(SpotInstanceRequestState.OPEN);
        instanceRequest.setSpotInstanceLaunchSpecification(
                new SpotInstanceLaunchSpecification(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE,
                                                    new SpotInstanceLaunchSpecification.Placement(null),
                                                    null, null, null, null));
        instanceRequest.setCreateTime(Instant.now());
        instanceRequestList.add(instanceRequest);
        instanceRequestsResponse.setSpotInstanceRequest(instanceRequestList);

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeInstanceRequests(eq(DUMMY_CUSTOMER_1), Mockito.any(), Mockito.any());

        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION, DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction()))
                .andExpect(status().isOk()).andReturn().getResponse();

        String body = response.getContentAsString();
        Assertions.assertThat(body)
                .contains("\"SpotInstanceRequests\"")
                .contains("\"SpotInstanceRequestId\"")
                .contains("\"SpotCloudProvider\"")
                .doesNotContain("\"InstanceRequests\"")
                .doesNotContain("\"InstanceRequestId\"")
                .doesNotContain("\"CloudProvider\"");
    }

    @Test
    void cancelInstanceRequests_withCancelInstanceRequestsAction_returnsSuccess()
            throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.CANCEL_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk());

        verify(instanceService).cancelInstanceRequests(eq(DUMMY_CUSTOMER_1),
                                                       eq(Set.of(DUMMY_REQUEST_ID)),
                                                       Mockito.any());
    }

    @Test
    void terminateInstanceRequest_withTerminateInstanceRequestAction_returnsConciseJson()
            throws Exception {
        TerminateInstancesResponse terminateInstancesResponse = new TerminateInstancesResponse();
        List<TerminateInstancesResponse.TerminatingInstance> terminatingInstanceList =
                new ArrayList<>();
        TerminateInstancesResponse.TerminatingInstance terminatingInstance =
                new TerminateInstancesResponse.TerminatingInstance();
        terminatingInstance.setInstanceId(DUMMY_INSTANCE_ID);
        terminatingInstance.setRequestId(DUMMY_REQUEST_ID);
        terminatingInstance.setCurrentState(new SpotInstanceState(32, "shutting-down"));
        terminatingInstance.setPreviousState(new SpotInstanceState(16, "running"));
        terminatingInstanceList.add(terminatingInstance);
        terminateInstancesResponse.setTerminatingInstances(terminatingInstanceList);

        doReturn(terminateInstancesResponse).when(instanceService)
                .terminateInstanceRequests(eq(DUMMY_CUSTOMER_1),
                                        eq(Set.of(DUMMY_REQUEST_ID)), Mockito.any());

        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_INSTANCE_REQUEST.getRequestAction())
                                .param("RequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk()).andReturn().getResponse();

        String body = response.getContentAsString();
        Assertions.assertThat(body)
                .contains("\"TerminatingInstances\"")
                .contains("\"InstanceId\"")
                .contains("\"CurrentState\"")
                .contains("\"PreviousState\"")
                .contains(DUMMY_INSTANCE_ID)
                .contains(DUMMY_REQUEST_ID);
    }

    @Test
    void terminateInstanceRequest_withRequestIdNotProvided_returnsError() throws Exception {
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.delete(PUBLIC_SIS_ENDPOINT)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.TERMINATE_INSTANCE_REQUEST.getRequestAction()))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .isEqualTo(
                        "{\"error\":\"RequestId should be provided with TerminateInstanceRequest action\"}");
    }

    @Test
    void describeAdminInstanceRequests_withAdminInstanceRequestDescribeScope_returnsConciseJson()
            throws Exception {
        GetSpotInstanceRequests instanceRequestsResponse = new GetSpotInstanceRequests();
        List<SpotInstanceRequest> instanceRequestList = new ArrayList<>();
        SpotInstanceRequest instanceRequest = new SpotInstanceRequest();
        instanceRequest.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        instanceRequest.setSpotCloudProvider(CloudProvider.AWS);
        instanceRequest.setState(SpotInstanceRequestState.OPEN);
        instanceRequestList.add(instanceRequest);
        instanceRequestsResponse.setSpotInstanceRequest(instanceRequestList);

        doReturn(instanceRequestsResponse).when(instanceService)
                .describeAdminInstanceRequests(Mockito.any(), Mockito.any());

        MockHttpServletResponse response = mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT + "/admin")
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1,
                                                      ADMIN_INSTANCE_REQUEST_DESCRIBE_SCOPE))
                                .param(ACTION,
                                       SpotInstanceRequestAction.DESCRIBE_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk()).andReturn().getResponse();

        String body = response.getContentAsString();
        Assertions.assertThat(body)
                .contains("\"InstanceRequests\"")
                .contains("\"InstanceRequestId\"")
                .contains("\"CloudProvider\"")
                .doesNotContain("\"SpotInstanceRequests\"")
                .doesNotContain("\"SpotInstanceRequestId\"")
                .doesNotContain("\"SpotCloudProvider\"");
    }

    //**************************************************
    //* describeInstancesPerDeployment / PerGpuSpec - UseConciseName
    //**************************************************

    @Test
    void describeInstancesPerDeployment_withUseConciseNameTrue_returnsInstanceShape()
            throws Exception {
        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();

        GetSpotInstanceRequests mockResult = new GetSpotInstanceRequests();
        SpotInstanceRequest sir = new SpotInstanceRequest();
        sir.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        sir.setSpotCloudProvider(CloudProvider.AWS);
        sir.setStatus(
                new SpotInstanceRequestStatus("pending-evaluation", "open", Instant.now()));
        sir.setState(SpotInstanceRequestState.OPEN);
        sir.setSpotInstanceLaunchSpecification(
                new SpotInstanceLaunchSpecification(DUMMY_NON_BYOC_INSTANCE_TYPE,
                                                   DUMMY_CONTAINER_IMAGE,
                                                   new SpotInstanceLaunchSpecification.Placement(
                                                           null),
                                                   null, null, ncaId, null));
        sir.setCreateTime(Instant.now());
        mockResult.setSpotInstanceRequest(List.of(sir));

        SpotInstance si = SpotInstance.builder()
                .instanceId("test-instance-1")
                .spotCloudProvider(CloudProvider.AWS)
                .state(new SpotInstanceState(16, "running"))
                .createTime(Instant.now())
                .build();
        mockResult.setSpotInstances(List.of(si));

        doReturn(mockResult).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, null, false, false);

        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCES_BY_DEPLOYMENT_API_URL, ncaId,
                                                   deploymentId)
                                .headers(generateAuthorizationHeader())
                                .param("UseConciseName", "true"))
                .andExpect(status().isOk()).andReturn().getResponse();

        String body = response.getContentAsString();
        Assertions.assertThat(body)
                .contains("\"InstanceRequests\"")
                .contains("\"InstanceRequestId\"")
                .contains("\"CloudProvider\"")
                .contains(DUMMY_REQUEST_ID)
                .doesNotContain("\"SpotInstanceRequests\"")
                .doesNotContain("\"SpotInstanceRequestId\"")
                .doesNotContain("\"SpotCloudProvider\"");

        assertNoLegacyFieldName(objectMapper.readTree(body));
    }

    @Test
    void describeInstancesPerDeployment_withUseConciseNameFalse_returnsLegacyShape()
            throws Exception {
        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();

        GetSpotInstanceRequests mockResult = new GetSpotInstanceRequests();
        SpotInstanceRequest sir = new SpotInstanceRequest();
        sir.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        sir.setSpotCloudProvider(CloudProvider.AWS);
        sir.setStatus(
                new SpotInstanceRequestStatus("pending-evaluation", "open", Instant.now()));
        sir.setState(SpotInstanceRequestState.OPEN);
        sir.setSpotInstanceLaunchSpecification(
                new SpotInstanceLaunchSpecification(DUMMY_NON_BYOC_INSTANCE_TYPE,
                                                   DUMMY_CONTAINER_IMAGE,
                                                   new SpotInstanceLaunchSpecification.Placement(
                                                           null),
                                                   null, null, ncaId, null));
        sir.setCreateTime(Instant.now());
        mockResult.setSpotInstanceRequest(List.of(sir));

        doReturn(mockResult).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, null, false, false);

        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCES_BY_DEPLOYMENT_API_URL, ncaId,
                                                   deploymentId)
                                .headers(generateAuthorizationHeader())
                                .param("UseConciseName", "false"))
                .andExpect(status().isOk()).andReturn().getResponse();

        String body = response.getContentAsString();
        Assertions.assertThat(body)
                .contains("\"SpotInstanceRequests\"")
                .contains("\"SpotInstanceRequestId\"")
                .contains("\"SpotCloudProvider\"")
                .doesNotContain("\"InstanceRequests\"")
                .doesNotContain("\"InstanceRequestId\"")
                .doesNotContain("\"CloudProvider\"");
    }

    @Test
    void describeInstancesPerGpuSpec_withUseConciseNameTrue_returnsInstanceShape()
            throws Exception {
        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();
        UUID gpuSpecId = UUID.randomUUID();

        GetSpotInstanceRequests mockResult = new GetSpotInstanceRequests();
        SpotInstanceRequest sir = new SpotInstanceRequest();
        sir.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        sir.setSpotCloudProvider(CloudProvider.AWS);
        sir.setState(SpotInstanceRequestState.OPEN);
        mockResult.setSpotInstanceRequest(List.of(sir));

        doReturn(mockResult).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, gpuSpecId, false, false);

        String url = "/v1/si/accounts/" + ncaId + "/deployments/" + deploymentId
                + "/gpuSpecs/" + gpuSpecId + "/instances";
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(url)
                                .headers(generateAuthorizationHeader())
                                .param("UseConciseName", "true"))
                .andExpect(status().isOk()).andReturn().getResponse();

        String body = response.getContentAsString();
        Assertions.assertThat(body)
                .contains("\"InstanceRequests\"")
                .contains("\"InstanceRequestId\"")
                .doesNotContain("\"SpotInstanceRequests\"")
                .doesNotContain("\"SpotInstanceRequestId\"");

        assertNoLegacyFieldName(objectMapper.readTree(body));
    }

    @Test
    void describeInstancesPerGpuSpec_withUseConciseNameOmitted_returnsLegacyShape()
            throws Exception {
        String ncaId = RandomFactory.getRandomStringWithPrefix("ncaid", 5);
        UUID deploymentId = UUID.randomUUID();
        UUID gpuSpecId = UUID.randomUUID();

        GetSpotInstanceRequests mockResult = new GetSpotInstanceRequests();
        SpotInstanceRequest sir = new SpotInstanceRequest();
        sir.setSpotInstanceRequestId(DUMMY_REQUEST_ID);
        sir.setSpotCloudProvider(CloudProvider.AWS);
        sir.setState(SpotInstanceRequestState.OPEN);
        mockResult.setSpotInstanceRequest(List.of(sir));

        doReturn(mockResult).when(instanceService)
                .describeInstancesByDeploymentId(ncaId, deploymentId, gpuSpecId, false, false);

        String url = "/v1/si/accounts/" + ncaId + "/deployments/" + deploymentId
                + "/gpuSpecs/" + gpuSpecId + "/instances";
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(url)
                                .headers(generateAuthorizationHeader()))
                .andExpect(status().isOk()).andReturn().getResponse();

        String body = response.getContentAsString();
        Assertions.assertThat(body)
                .contains("\"SpotInstanceRequests\"")
                .contains("\"SpotInstanceRequestId\"")
                .doesNotContain("\"InstanceRequests\"")
                .doesNotContain("\"InstanceRequestId\"");
    }

    //**************************************************
    //* InstanceRequestId alias on describe / cancel / admin
    //**************************************************

    @Test
    void describeInstances_withInstanceRequestIdAlias_returnsSuccess() throws Exception {
        Set<String> expected = Set.of(DUMMY_REQUEST_ID + "_1", DUMMY_REQUEST_ID + "_2");
        doReturn(new GetSpotInstanceRequests()).when(instanceService)
                .describeInstanceRequests(eq(DUMMY_CUSTOMER_1), eq(expected), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("InstanceRequestId",
                                       DUMMY_REQUEST_ID + "_1," + DUMMY_REQUEST_ID + "_2"))
                .andExpect(status().isOk());

        verify(instanceService).describeInstanceRequests(eq(DUMMY_CUSTOMER_1), eq(expected),
                                                 Mockito.any());
    }

    @Test
    void describeInstances_withBothRequestIdParams_unionsSets() throws Exception {
        doReturn(new GetSpotInstanceRequests()).when(instanceService)
                .describeInstanceRequests(eq(DUMMY_CUSTOMER_1), Mockito.any(), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", "a,b")
                                .param("InstanceRequestId", "b,c"))
                .andExpect(status().isOk());

        verify(instanceService).describeInstanceRequests(eq(DUMMY_CUSTOMER_1),
                                                 eq(Set.of("a", "b", "c")), Mockito.any());
    }

    @Test
    void cancelInstanceRequests_withInstanceRequestIdAlias_returnsSuccess() throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.CANCEL_INSTANCE_REQUESTS.getRequestAction())
                                .param("InstanceRequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk());

        verify(instanceService).cancelInstanceRequests(eq(DUMMY_CUSTOMER_1),
                                                       eq(Set.of(DUMMY_REQUEST_ID)),
                                                       Mockito.any());
    }

    @Test
    void cancelInstanceRequests_withBothRequestIdParams_unionsSets() throws Exception {
        mockMvc.perform(MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.CANCEL_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", "a,b")
                                .param("InstanceRequestId", "b,c"))
                .andExpect(status().isOk());

        verify(instanceService).cancelInstanceRequests(eq(DUMMY_CUSTOMER_1),
                                                       eq(Set.of("a", "b", "c")),
                                                       Mockito.any());
    }

    @Test
    void cancelInstanceRequests_withNeitherRequestIdParam_returnsBadRequest()
            throws Exception {
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.put(PUBLIC_SIS_ENDPOINT)
                                .contentType(MediaType.APPLICATION_JSON)
                                .headers(generateAuthorizationHeader())
                                .param(ACTION,
                                       SpotInstanceRequestAction.CANCEL_INSTANCE_REQUESTS.getRequestAction()))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .contains("Either SpotInstanceRequestId or InstanceRequestId must be provided");
    }

    @Test
    void describeAdminInstanceRequests_withInstanceRequestIdAlias_returnsSuccess() throws Exception {
        doReturn(new GetSpotInstanceRequests()).when(instanceService)
                .describeAdminInstanceRequests(Mockito.any(), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT + "/admin")
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1,
                                                      ADMIN_INSTANCE_REQUEST_DESCRIBE_SCOPE))
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("InstanceRequestId", DUMMY_REQUEST_ID))
                .andExpect(status().isOk());

        verify(instanceService).describeAdminInstanceRequests(eq(Set.of(DUMMY_REQUEST_ID)),
                                                      Mockito.any());
    }

    @Test
    void describeAdminInstanceRequests_withBothRequestIdParams_unionsSets() throws Exception {
        doReturn(new GetSpotInstanceRequests()).when(instanceService)
                .describeAdminInstanceRequests(Mockito.any(), Mockito.any());

        mockMvc.perform(MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT + "/admin")
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1,
                                                      ADMIN_INSTANCE_REQUEST_DESCRIBE_SCOPE))
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param("SpotInstanceRequestId", "a,b")
                                .param("InstanceRequestId", "b,c"))
                .andExpect(status().isOk());

        verify(instanceService).describeAdminInstanceRequests(eq(Set.of("a", "b", "c")), Mockito.any());
    }

    @Test
    void describeAdminInstanceRequests_withNeitherRequestIdParam_returnsBadRequest()
            throws Exception {
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT + "/admin")
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1,
                                                      ADMIN_INSTANCE_REQUEST_DESCRIBE_SCOPE))
                                .param(ACTION,
                                       DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction()))
                .andExpect(status().isBadRequest()).andReturn().getResponse();

        Assertions.assertThat(response.getContentAsString())
                .contains("Either SpotInstanceRequestId or InstanceRequestId must be provided");
    }

    //**************************************************
    //* Helper functions
    //**************************************************

    // Recursively asserts that no field key anywhere in the JSON tree
    // contains the substring "Spot". Guards against future model fields
    // accidentally re-introducing the prefix in the concise response shape.
    private static void assertNoLegacyFieldName(JsonNode node) {
        if (node == null || node.isNull()) {
            return;
        }
        if (node.isObject()) {
            for (var entry : node.properties()) {
                Assertions.assertThat(entry.getKey())
                        .as("JSON key should not contain 'Spot'")
                        .doesNotContain("Spot");
                assertNoLegacyFieldName(entry.getValue());
            }
        } else if (node.isArray()) {
            node.forEach(InstanceControllerTest::assertNoLegacyFieldName);
        }
    }

    // Helper function to generate headers with auth for dummy customer
    private HttpHeaders generateAuthorizationHeader() {
        HttpHeaders httpHeaders = new HttpHeaders();
        httpHeaders.add(HttpHeaders.AUTHORIZATION,
                        getAuthHeader(DUMMY_CUSTOMER_1, SPOT_REQUEST_SCOPE));
        return httpHeaders;
    }

    private SpotInstanceRequestSchema createDefaultInstanceRequestSchema() {
        return SpotInstanceRequestSchema.builder()
                .action(REQUEST_SPOT_INSTANCES)
                .instanceCount(1)
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .containerImage(DUMMY_CONTAINER_IMAGE)
                .environment(DUMMY_ENVIRONMENT_VALUE)
                .availabilityZone("availabilityZone")
                .backend("backend")
                .gpu("gpu")
                .ncaId("ncaId")
                .cacheArtifacts(true)
                .cacheHandle("cachingHandle")
                .functionType(FunctionType.DEFAULT)
                .functionName("functionName")
                .build();
    }

    private static Stream<SpotInstanceRequestAction> getAllActionsExcept(
            Set<SpotInstanceRequestAction> toExclude) {
        return Arrays.stream(SpotInstanceRequestAction.values())
                .filter(action -> !toExclude.contains(action));
    }

    private static Stream<SpotInstanceRequestAction> getActionsExceptRequestInstances() {
        return getAllActionsExcept(new HashSet<> (Set.of(REQUEST_SPOT_INSTANCES,
                                                         REQUEST_SPOT_INSTANCES_FOR_TASK,
                                                         REQUEST_INSTANCES,
                                                         REQUEST_INSTANCES_FOR_TASK)));
    }
}
