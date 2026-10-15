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

import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_SCOPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_TOKEN;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static com.nvidia.icms.util.TestUtil.INSTANCE_STATUS_UPDATE_SCOPE;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.eq;

import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.service.internal.InternalInstanceService;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import org.assertj.core.api.Assertions;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.http.converter.HttpMessageNotReadableException;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;

@ExtendWith(MockitoExtension.class)
class InternalInstanceControllerTest extends IntegrationTest {

    private static final String UPDATE_REQUEST_STATUS_URL = "/v1/sirs/{spotInstanceRequestId}";
    private static final String UPDATE_INSTANCE_STATUS_URL =
            "/v1/sirs/{spotInstanceRequestId}/{instanceId}";

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private InternalInstanceService internalInstanceService;
    @Autowired
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Test
    void updateInstanceRequestStatus_withValidParams_returnsSuccess()
            throws Exception {
        String requestBodyJsonString = getRequestUpdateRequestBody();
        Assertions.assertThat(requestBodyJsonString).isNotEqualTo("{}");

        mockMvc.perform(MockMvcRequestBuilders.put(UPDATE_REQUEST_STATUS_URL, DUMMY_REQUEST_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.SPOT_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        Assertions.assertThat(icmsConfigurationProperties.getRequestCancelDurationInMin())
                .isEqualTo(30);
    }

    @Test
    void updateInstanceRequestStatus_withNoRequestBody_returnsAsBadRequest()
            throws Exception {
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.put(UPDATE_REQUEST_STATUS_URL, DUMMY_REQUEST_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.SPOT_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isBadRequest()).andReturn();

        Exception exception = mvcResult.getResolvedException();
        assertInstanceOf(HttpMessageNotReadableException.class, exception);
        assertTrue(exception.getMessage().contains("Required request body is missing"));
    }

    @Test
    void updateInstanceRequestStatus_withErrorFromServiceLayer_returnsAsInternalServerError()
            throws Exception {

        Mockito.doThrow(new IcmsInternalServerException("dummy_internal_error"))
                .when(internalInstanceService)
                .updateInstanceRequestStatus(eq(DUMMY_CUSTOMER_1), Mockito.anyString(),
                                         Mockito.any(SpotInstanceRequestStatusUpdateRequest.class),
                                         Mockito.any());

        String requestBodyJsonString = getRequestUpdateRequestBody();
        Assertions.assertThat(requestBodyJsonString).isNotEqualTo("{}");

        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.put(UPDATE_REQUEST_STATUS_URL, DUMMY_REQUEST_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.SPOT_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isInternalServerError()).andReturn();

        Exception exception = mvcResult.getResolvedException();
        assertInstanceOf(IcmsInternalServerException.class, exception);
        assertTrue(exception.getMessage().contains("dummy_internal_error"));
    }

    @Test
    void updateInstanceRequestStatus_withInvalidAuthentication_returnsAsUnauthorized()
            throws Exception {
        SpotInstanceRequestStatusUpdateRequest requestBody =
                new SpotInstanceRequestStatusUpdateRequest(
                        SpotRequestStatusCode.PENDING_FULFILLMENT, null, null,
                        new SpotInstanceStatusUpdateRequest.InstancePlacement(DUMMY_ZONE), null, null);
        String requestBodyJsonString = GsonCompatMapper.toJson(requestBody);
        Assertions.assertThat(requestBodyJsonString).isNotEqualTo("{}");

        mockMvc.perform(
                        MockMvcRequestBuilders.put(UPDATE_REQUEST_STATUS_URL, DUMMY_REQUEST_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION, DUMMY_TOKEN))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized()).andReturn();
    }

    @Test
    void updateInstanceRequestStatus_withUnauthorizedToken_returnsAsForbidden()
            throws Exception {
        String requestBodyJsonString = getRequestUpdateRequestBody();
        Assertions.assertThat(requestBodyJsonString).isNotEqualTo("{}");

        mockMvc.perform(
                        MockMvcRequestBuilders.put(UPDATE_REQUEST_STATUS_URL, DUMMY_REQUEST_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();
    }

    @Test
    void updateInstanceStatus_withValidParams_returnsSuccess()
            throws Exception {

        String requestBodyJsonString = getInstanceUpdateRequestBody();
        mockMvc.perform(MockMvcRequestBuilders.post(UPDATE_INSTANCE_STATUS_URL, DUMMY_REQUEST_ID,
                                                    DUMMY_INSTANCE_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.SPOT_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());
    }

    @Test
    void updateInstanceStatus_instanceTerminationUpdateWithHeathInfo_returnsSuccess()
            throws Exception {

        String requestBodyJsonString = getInstanceTerminationUpdateWithHeathInfo();
        mockMvc.perform(MockMvcRequestBuilders.post(UPDATE_INSTANCE_STATUS_URL, DUMMY_REQUEST_ID,
                                                    DUMMY_INSTANCE_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.SPOT_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());
    }

    @Test
    void updateInstanceStatus_withNoRequestBody_returnsAsBadRequest()
            throws Exception {
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.post(UPDATE_INSTANCE_STATUS_URL, DUMMY_REQUEST_ID,
                                                    DUMMY_INSTANCE_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.SPOT_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isBadRequest()).andReturn();

        Exception exception = mvcResult.getResolvedException();
        assertInstanceOf(HttpMessageNotReadableException.class, exception);
        assertTrue(exception.getMessage().contains("Required request body is missing"));
    }

    @Test
    void updateInstanceStatus_withErrorFromServiceLayer_returnsAsInternalServerError()
            throws Exception {

        Mockito.doThrow(new IcmsInternalServerException("dummy_internal_error"))
                .when(internalInstanceService)
                .updateInstanceStatus(Mockito.anyString(), Mockito.anyString(),
                                          Mockito.any(SpotInstanceStatusUpdateRequest.class),
                                          eq(DUMMY_CUSTOMER_1), Mockito.any());

        String requestBodyJsonString = getInstanceUpdateRequestBody();

        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.post(UPDATE_INSTANCE_STATUS_URL, DUMMY_REQUEST_ID,
                                                    DUMMY_INSTANCE_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  TestUtil.SPOT_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isInternalServerError()).andReturn();

        Exception exception = mvcResult.getResolvedException();
        assertInstanceOf(IcmsInternalServerException.class, exception);
        assertTrue(exception.getMessage().contains("dummy_internal_error"));
    }

    @Test
    void updateInstanceStatus_withInvalidAuthentication_returnsAsUnauthorized()
            throws Exception {
        String requestBodyJsonString = getInstanceUpdateRequestBody();

        mockMvc.perform(
                        MockMvcRequestBuilders.post(UPDATE_INSTANCE_STATUS_URL, DUMMY_REQUEST_ID,
                                                    DUMMY_INSTANCE_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION, DUMMY_TOKEN))
                .andExpect(MockMvcResultMatchers.status().isUnauthorized()).andReturn();
    }

    @Test
    void updateInstanceStatus_withUnauthorizedToken_returnsAsForbidden()
            throws Exception {

        String requestBodyJsonString = getInstanceUpdateRequestBody();

        mockMvc.perform(
                        MockMvcRequestBuilders.post(UPDATE_INSTANCE_STATUS_URL, DUMMY_REQUEST_ID,
                                                    DUMMY_INSTANCE_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isForbidden()).andReturn();
    }

    //**************************************************
    //* New scope: instance-status-update
    //**************************************************

    @Test
    void updateInstanceRequestStatus_withInstanceStatusUpdateScope_returnsSuccess() throws Exception {
        String requestBodyJsonString = getRequestUpdateRequestBody();

        mockMvc.perform(MockMvcRequestBuilders.put(UPDATE_REQUEST_STATUS_URL, DUMMY_REQUEST_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  INSTANCE_STATUS_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());
    }

    @Test
    void updateInstanceStatus_withInstanceStatusUpdateScope_returnsSuccess() throws Exception {
        String requestBodyJsonString = getInstanceUpdateRequestBody();

        mockMvc.perform(MockMvcRequestBuilders.post(UPDATE_INSTANCE_STATUS_URL, DUMMY_REQUEST_ID,
                                                    DUMMY_INSTANCE_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                  INSTANCE_STATUS_UPDATE_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk());
    }

    private String getInstanceUpdateRequestBody() {
        return "{\n" +
                "    \"action\": \"RequestSpotInstances\",\n" +
                "    \"status\": \"fulfilled\",\n" +
                "    \"instanceState\": \"running\",\n" +
                "    \"requestState\": \"active\",\n" +
                "    \"imageId\": \"dummy-image-id\",\n" +
                "    \"placement\": {\n" +
                "        \"availabilityZone\": \"localhost\"\n" +
                "    }\n" +
                "}";
    }

    private String getRequestUpdateRequestBody() {
        return "{\n" +
                "    \"status\": \"pending-fulfillment\"\n" +
                "}";
    }

    private String getInstanceTerminationUpdateWithHeathInfo() {
        return """
                {
                 "status": "instance-terminated-by-user",
                 "instanceState": "terminated",
                 "requestState": "closed",
                 "action": "TerminateInstances",
                  "placement": {
                           "availabilityZone": "localhost"
                       },
                  "imageId": "dummy_image",
                  "healthInfo":{
                           "errorLog":"2023/09/25 07:59:30 [notice] 1#1: worker process 260 exited with code 0"
                       }
                }
                """;

    }
}
