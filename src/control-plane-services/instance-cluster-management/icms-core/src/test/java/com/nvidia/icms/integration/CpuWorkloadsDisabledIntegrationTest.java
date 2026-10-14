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
package com.nvidia.icms.integration;

import static com.nvidia.icms.util.JwtKeyUtils.getAuthHeader;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.icms.errors.CpuWorkloadsNotReadyException;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.util.TestUtil;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;

/**
 * With the switch off, which is the default, a CPU shape is refused with an explicit error and
 * nothing is stored. It is never accepted and treated as one GPU.
 */
class CpuWorkloadsDisabledIntegrationTest extends IntegrationTest {

    private static final String CLUSTER_CREATION_URL = "/v1/accounts/{ncaId}/clusters";

    private static final String CREATE_CLUSTER_WITH_CPU_SHAPE = """
            {
              "cloudProvider": "ON-PREM",
              "clusterDescription": "cpu shape",
              "clusterGroupName": "cg-cpu",
              "clusterName": "cpu-cluster",
              "ncaId": "dummy_nca_id_1",
              "nvcaVersion": "2.0.0",
              "authorizedNCAIds": ["dummy_byoc_nca_id"],
              "capabilities": [],
              "attributes": [],
              "gpus": [{
                "name": "CPU", "capacity": 10,
                "instanceTypes": [{
                  "name": "NCP.CPU.4c16g", "value": "NCP.CPU.4c16g",
                  "description": "4 cores and 16 GB, no GPU", "default": true, "cpuCores": 4,
                  "systemMemory": "16G", "gpuCount": 0, "cpuArch": "x86",
                  "os": "Linux 64 bit", "storage": "80G"
                }]
              }],
              "oAuthClientId": "nvssa-stg-dummy_cpu_off",
              "region": "US-West-1"
            }
            """;

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ClusterRepository clusterRepository;

    @Test
    void cpuShapeIsRefusedWithAnExplicitErrorAndNothingIsStored() throws Exception {
        MvcResult result = mockMvc.perform(
                        MockMvcRequestBuilders.post(CLUSTER_CREATION_URL, "dummy_nca_id_1")
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(CREATE_CLUSTER_WITH_CPU_SHAPE)
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader("dummy_nca_id_1",
                                                      TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isConflict()).andReturn();

        assertThat(result.getResponse().getContentAsString())
                .contains(CpuWorkloadsNotReadyException.ERROR_CODE)
                .contains("disabled");
        assertThat(clusterRepository.getAllClusters()).isEmpty();
    }
}
