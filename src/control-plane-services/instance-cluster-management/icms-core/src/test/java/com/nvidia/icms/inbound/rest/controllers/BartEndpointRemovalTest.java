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

import static com.nvidia.icms.util.TestUtil.BYOC_REGISTRATION_SCOPE;
import static com.nvidia.icms.util.TestUtil.CLUSTER_HEARTBEAT_SCOPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.util.JwtKeyUtils;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;

class BartEndpointRemovalTest extends IntegrationTest {

    @Autowired
    private MockMvc mockMvc;

    @Test
    void bartRoutes_areNotFound() throws Exception {
        String registrationAuth = JwtKeyUtils.getAuthHeader(
                DUMMY_CUSTOMER_ID, BYOC_REGISTRATION_SCOPE);
        String heartbeatAuth = JwtKeyUtils.getAuthHeader(
                DUMMY_CUSTOMER_ID, CLUSTER_HEARTBEAT_SCOPE);

        mockMvc.perform(MockMvcRequestBuilders.post("/v1/bart")
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION, registrationAuth))
                .andExpect(MockMvcResultMatchers.status().isNotFound());
        mockMvc.perform(MockMvcRequestBuilders.delete("/v1/bart")
                                .header(HttpHeaders.AUTHORIZATION, registrationAuth))
                .andExpect(MockMvcResultMatchers.status().isNotFound());
        mockMvc.perform(MockMvcRequestBuilders.get("/v1/bart/creds")
                                .header(HttpHeaders.AUTHORIZATION, registrationAuth))
                .andExpect(MockMvcResultMatchers.status().isNotFound());
        mockMvc.perform(MockMvcRequestBuilders.put("/v1/bart/heartbeat")
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION, heartbeatAuth))
                .andExpect(MockMvcResultMatchers.status().isNotFound());
    }
}
