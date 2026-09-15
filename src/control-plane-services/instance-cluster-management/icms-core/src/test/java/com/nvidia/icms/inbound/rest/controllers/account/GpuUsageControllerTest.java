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

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.nvidia.icms.inbound.rest.model.account.DeploymentGpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.service.account.AccountInfoService;
import com.nvidia.icms.service.account.GpuUsageService;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import static com.nvidia.icms.util.TestUtil.GPU_USAGE_SCOPE;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;

class GpuUsageControllerTest extends IntegrationTest {

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private GpuUsageService gpuUsageService;

    @MockitoBean
    private AccountInfoService accountInfoService;

    private static final String NCA_ID = "test-nca-id";
    private static final String DEPLOYMENT_ID = "test-deployment-id";
    private static final String SPOT_GPU_USAGE_SCOPE = "spot-gpu-usage";
    private static final String OTHER_SCOPE = "other_scope";

    @BeforeEach
    void setUp() {
        when(gpuUsageService.getGpuUsage(any())).thenReturn(new GpuUsageResponse());
        when(gpuUsageService.getDeploymentGpuUsage(any(), any())).thenReturn(
                new DeploymentGpuUsageResponse());
        when(accountInfoService.getInstanceTypeAvailability(any())).thenReturn(
                new InstanceTypeAvailabilityResponse());
    }

    // Tests for /v1/si/accounts/{ncaId}/gpu/usage endpoint
    @Test
    void getGpuUsage_shouldReturnOk()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/gpu/usage", NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  SPOT_GPU_USAGE_SCOPE)))
                .andExpect(status().isOk())
                .andExpect(content().contentType(MediaType.APPLICATION_JSON));
    }

    @Test
    void getGpuUsage_withoutRequiredAuthority_shouldReturnUnauthorized()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/gpu/usage", NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void getGpuUsage_withIncorrectScope_shouldReturnForbidden()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/gpu/usage", NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  OTHER_SCOPE)))
                .andExpect(status().isForbidden());
    }

    // Tests for /v1/si/accounts/{ncaId}/deployments/{deploymentId}/gpu/usage endpoint
    @Test
    void getDeploymentGpuUsage_shouldReturnOk()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/deployments/{deploymentId}/gpu/usage", NCA_ID,
                            DEPLOYMENT_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  SPOT_GPU_USAGE_SCOPE)))
                .andExpect(status().isOk())
                .andExpect(content().contentType(MediaType.APPLICATION_JSON));
    }

    @Test
    void getDeploymentGpuUsage_withoutRequiredAuthority_shouldReturnUnauthorized()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/deployments/{deploymentId}/gpu/usage", NCA_ID,
                            DEPLOYMENT_ID)
                                .contentType(MediaType.APPLICATION_JSON))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void getDeploymentGpuUsage_withIncorrectScope_shouldReturnForbidden()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/deployments/{deploymentId}/gpu/usage", NCA_ID,
                            DEPLOYMENT_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  OTHER_SCOPE)))
                .andExpect(status().isForbidden());
    }

    // Tests for /v1/si/accounts/{ncaId}/instanceTypes/availability endpoint
    @Test
    void getInstanceTypeAvailability_shouldReturnOk()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/instanceTypes/availability", NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  SPOT_GPU_USAGE_SCOPE)))
                .andExpect(status().isOk())
                .andExpect(content().contentType(MediaType.APPLICATION_JSON));
    }

    @Test
    void getInstanceTypeAvailability_withoutRequiredAuthority_shouldReturnUnauthorized()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/instanceTypes/availability", NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void getInstanceTypeAvailability_withIncorrectScope_shouldReturnForbidden()
            throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/instanceTypes/availability", NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  OTHER_SCOPE)))
                .andExpect(status().isForbidden());
    }

    // Tests for new gpu-usage scope

    @Test
    void getGpuUsage_withGpuUsageScope_shouldReturnOk() throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/gpu/usage", NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  GPU_USAGE_SCOPE)))
                .andExpect(status().isOk())
                .andExpect(content().contentType(MediaType.APPLICATION_JSON));
    }

    @Test
    void getDeploymentGpuUsage_withGpuUsageScope_shouldReturnOk() throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/deployments/{deploymentId}/gpu/usage", NCA_ID,
                            DEPLOYMENT_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  GPU_USAGE_SCOPE)))
                .andExpect(status().isOk())
                .andExpect(content().contentType(MediaType.APPLICATION_JSON));
    }

    @Test
    void getInstanceTypeAvailability_withGpuUsageScope_shouldReturnOk() throws Exception {
        mockMvc.perform(get("/v1/si/accounts/{ncaId}/instanceTypes/availability", NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1,
                                                                  GPU_USAGE_SCOPE)))
                .andExpect(status().isOk())
                .andExpect(content().contentType(MediaType.APPLICATION_JSON));
    }
}
