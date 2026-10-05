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
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENCODED_INFERENCE_CONTAINER_ENV;
import static com.nvidia.icms.util.TestUtil.INSTANCE_TYPES_LISTING_SCOPE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.amazonaws.services.sqs.AmazonSQS;
import com.amazonaws.services.sqs.model.SendMessageBatchRequestEntry;
import com.amazonaws.services.sqs.model.SendMessageBatchResult;
import com.amazonaws.services.sqs.model.SendMessageBatchResultEntry;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationResponse;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import org.mockito.Mockito;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.test.annotation.DirtiesContext;
import org.springframework.test.context.TestPropertySource;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.ObjectMapper;

/**
 * Walks a CPU-only workload through ICMS with the switch on: a cluster registers a CPU shape, the
 * shape is listed with its capacity counted in instances, a request for it is accepted, and the
 * message sent to the cluster says no GPU is needed instead of one.
 */
@TestPropertySource(properties = "icms.cpu-workloads.enabled=true")
@DirtiesContext(classMode = DirtiesContext.ClassMode.AFTER_CLASS)
class CpuWorkloadsEnabledIntegrationTest extends IntegrationTest {

    private static final String CLUSTER_CREATION_URL = "/v1/accounts/{ncaId}/clusters";
    private static final String HEARTBEAT_URL = "/v1/nvca/clusters/{clusterId}/heartbeat";
    private static final String REGISTER_URL = "/v1/nvca/clusters/{clusterId}/register";
    private static final String STATUS_URL = "/v1/nvca/clusters/{clusterId}/cpu-workloads-status";
    private static final String INSTANCE_TYPES_URL = "/v1/si/accounts/{ncaId}/instanceTypes";
    private static final String CPU_SHAPE = "NCP.CPU.4c16g";

    private static final String CREATE_CLUSTER_WITH_CPU_SHAPE = """
            {
              "cloudProvider": "ON-PREM",
              "clusterDescription": "gpu and cpu shape",
              "clusterGroupName": "cg-cpu",
              "clusterName": "cpu-cluster",
              "ncaId": "dummy_nca_id_1",
              "nvcaVersion": "2.0.0",
              "authorizedNCAIds": ["dummy_byoc_nca_id"],
              "capabilities": [],
              "attributes": [],
              "gpus": [
                {
                  "name": "B200", "capacity": 1,
                  "instanceTypes": [{
                    "name": "Standard_ND96amsr_B200_v4_1x", "value": "Standard_ND96amsr_B200_v4",
                    "description": "Single 80 GB B200 GPU", "default": true, "cpuCores": 4,
                    "systemMemory": "16G", "gpuMemory": "80G", "gpuCount": 1, "cpuArch": "x86",
                    "os": "Linux 64 bit", "driverVersion": "v.353.145.36", "storage": "80G"
                  }]
                },
                {
                  "name": "CPU", "capacity": 10,
                  "instanceTypes": [{
                    "name": "NCP.CPU.4c16g", "value": "NCP.CPU.4c16g",
                    "description": "4 cores and 16 GB, no GPU", "default": true, "cpuCores": 4,
                    "systemMemory": "16G", "gpuCount": 0, "cpuArch": "x86",
                    "os": "Linux 64 bit", "storage": "80G"
                  }]
                }
              ],
              "oAuthClientId": "nvssa-stg-dummy_cpu",
              "region": "US-West-1"
            }
            """;

    private static final String REGISTER = """
            {"status": "READY", "k8sVersion": "v1.29.2", "allowClusterTargeting": true}
            """;

    private static final String HEARTBEAT = """
            {
              "status": "healthy",
              "gpuUsage": {
                "B200": {"capacity": 4, "allocated": 1, "available": 3},
                "CPU": {"capacity": 10, "allocated": 2, "available": 8}
              }
            }
            """;

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ObjectMapper objectMapper;

    @Autowired
    private ClusterRepository clusterRepository;

    @MockitoBean
    AmazonSQS client;

    @BeforeEach
    void stubQueue() {
        SendMessageBatchResult result = Mockito.mock(SendMessageBatchResult.class);
        when(result.getSuccessful()).thenReturn(List.of(new SendMessageBatchResultEntry()));
        when(client.sendMessageBatch(anyString(), any())).thenReturn(result);
    }

    private String createCluster() throws Exception {
        return createCluster(CREATE_CLUSTER_WITH_CPU_SHAPE);
    }

    private String createCluster(String body) throws Exception {
        MvcResult result = mockMvc.perform(
                        MockMvcRequestBuilders.post(CLUSTER_CREATION_URL, "dummy_nca_id_1")
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(body)
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader("dummy_nca_id_1",
                                                      TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
        return objectMapper.readTree(result.getResponse().getContentAsString())
                .get("clusterId").stringValue();
    }

    private String clusterAuth(String clusterId) {
        return JwtKeyUtils.getAuthHeader(clusterId, TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE);
    }

    @Test
    void cpuShapeIsRegisteredListedRequestedAndSentToTheClusterWithNoGpu() throws Exception {
        // 1. The cluster registers a GPU and a CPU shape. The CPU shape has no GPU memory.
        String clusterId = createCluster();
        Optional<ClusterEntity> stored = clusterRepository.getClusterInfoByClusterId(clusterId, true);
        assertThat(stored).isPresent();
        assertThat(stored.get().getGpusV5()).extracting("name").contains("B200", "CPU");

        // 2. The cluster asks for the status and is told CPU workloads are ready.
        mockMvc.perform(MockMvcRequestBuilders.get(STATUS_URL, clusterId)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isOk())
                .andExpect(MockMvcResultMatchers.jsonPath("$.state").value("READY"));

        // 3. The cluster registers, which makes it ready, then reports capacity for both, with
        //    CPU counted in instances.
        mockMvc.perform(MockMvcRequestBuilders.put(REGISTER_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON).content(REGISTER)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isOk());
        mockMvc.perform(MockMvcRequestBuilders.post(HEARTBEAT_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON).content(HEARTBEAT)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        // 4. The CPU shape is listed under the reserved name, alongside the GPU.
        MvcResult listing = mockMvc.perform(
                        MockMvcRequestBuilders.get(INSTANCE_TYPES_URL, DUMMY_BYOC_NCA_ID)
                                .contentType(MediaType.APPLICATION_JSON)
                                .header(HttpHeaders.AUTHORIZATION,
                                        getAuthHeader(DUMMY_CUSTOMER_1, INSTANCE_TYPES_LISTING_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
        JsonNode catalog = objectMapper.readTree(listing.getResponse().getContentAsString());
        assertThat(catalog.has("CPU")).isTrue();
        assertThat(catalog.has("B200")).isTrue();
        assertThat(catalog.get("CPU").get(0).get("name").stringValue()).isEqualTo(CPU_SHAPE);

        // 5. A request for the CPU shape is accepted.
        mockMvc.perform(MockMvcRequestBuilders.post("/v1/si")
                                .header("Authorization", getAuthHeader(DUMMY_CUSTOMER_ID, "spot-request"))
                                .param("Action", "RequestSpotInstances")
                                .param("LaunchSpecification.InstanceType", CPU_SHAPE)
                                .param("LaunchSpecification.NcaId", DUMMY_BYOC_NCA_ID)
                                .param("LaunchSpecification.Gpu", "CPU")
                                .param("LaunchSpecification.ContainerImage", DUMMY_CONTAINER_IMAGE)
                                .param("LaunchSpecification.Environment",
                                       DUMMY_ENCODED_INFERENCE_CONTAINER_ENV)
                                .param("LaunchSpecification.CredentialRef", "")
                                .param("InstanceCount", "2")
                                .param("FunctionDetails.FunctionId", UUID.randomUUID().toString())
                                .param("FunctionDetails.FunctionVersionId", UUID.randomUUID().toString())
                                .param("FunctionDetails.OwnerNcaId", "dummy-owner-nca-id")
                                .param("FunctionDetails.FunctionType", "STREAMING"))
                .andExpect(MockMvcResultMatchers.status().isAccepted());

        // 6. The message sent to the cluster names the CPU shape and asks for no GPU.
        ArgumentCaptor<List<SendMessageBatchRequestEntry>> entries = ArgumentCaptor.forClass(List.class);
        verify(client).sendMessageBatch(anyString(), entries.capture());
        JsonNode message = objectMapper.readTree(entries.getValue().get(0).getMessageBody());
        JsonNode launch = message.get("launchSpecification");
        assertThat(launch.get("gpuName").stringValue()).isEqualTo("CPU");
        assertThat(launch.get("gpuType").stringValue()).isEqualTo("CPU");
        assertThat(launch.get("instanceTypeName").stringValue()).isEqualTo(CPU_SHAPE);
        // Zero, not one: ICMS used to turn a count of 0 into 1 for every instance type.
        assertThat(launch.get("requestedGPUCount").asInt(-1)).isZero();
        assertThat(message.get("requestedGPUCount").asInt(-1)).isZero();
    }

    @Test
    void cpuShapeTaskRequestIsAcceptedAndSentToTheClusterWithNoGpu() throws Exception {
        String clusterId = createCluster(CREATE_CLUSTER_WITH_CPU_SHAPE
                .replace("cg-cpu", "cg-cpu-task")
                .replace("cpu-cluster", "cpu-cluster-task")
                .replace("nvssa-stg-dummy_cpu", "nvssa-stg-dummy_cpu_task"));
        mockMvc.perform(MockMvcRequestBuilders.put(REGISTER_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content("""
                                        {"status": "READY", "k8sVersion": "v1.29.2",
                                         "allowClusterTargeting": true,
                                         "allowTaskClusterCreationQueues": true}
                                        """)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isOk());
        mockMvc.perform(MockMvcRequestBuilders.post(HEARTBEAT_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON).content(HEARTBEAT)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isOk());
        String taskId = UUID.randomUUID().toString();

        mockMvc.perform(MockMvcRequestBuilders.post("/v1/si")
                                .header("Authorization", getAuthHeader(DUMMY_CUSTOMER_ID, "spot-request"))
                                .param("Action", "RequestSpotInstances")
                                .param("LaunchSpecification.InstanceType", CPU_SHAPE)
                                .param("LaunchSpecification.NcaId", DUMMY_BYOC_NCA_ID)
                                .param("LaunchSpecification.Gpu", "CPU")
                                .param("LaunchSpecification.ContainerImage", DUMMY_CONTAINER_IMAGE)
                                .param("LaunchSpecification.Environment",
                                       DUMMY_ENCODED_INFERENCE_CONTAINER_ENV)
                                .param("LaunchSpecification.CredentialRef", "")
                                .param("LaunchSpecification.MaxQueuedDuration", "PT1H")
                                .param("LaunchSpecification.MaxRuntimeDuration", "PT2H")
                                .param("LaunchSpecification.TerminationGracePeriodDuration", "PT10M")
                                .param("LaunchSpecification.ResultHandlingStrategy", "NONE")
                                .param("InstanceCount", "1")
                                .param("TaskDetails.TaskId", taskId)
                                .param("TaskDetails.TaskName", "cpu-task")
                                .param("TaskDetails.AccountName", "dummy-account")
                                .param("TaskDetails.OwnerNcaId", "dummy-owner-nca-id"))
                .andExpect(MockMvcResultMatchers.status().isAccepted());

        ArgumentCaptor<List<SendMessageBatchRequestEntry>> entries = ArgumentCaptor.forClass(List.class);
        verify(client).sendMessageBatch(anyString(), entries.capture());
        JsonNode message = objectMapper.readTree(entries.getValue().get(0).getMessageBody());
        JsonNode launch = message.get("launchSpecification");
        assertThat(message.get("taskDetails").get("taskId").stringValue()).isEqualTo(taskId);
        assertThat(launch.get("gpuType").stringValue()).isEqualTo("CPU");
        assertThat(launch.get("instanceTypeName").stringValue()).isEqualTo(CPU_SHAPE);
        assertThat(launch.get("requestedGPUCount").asInt(-1)).isZero();
        assertThat(message.get("requestedGPUCount").asInt(-1)).isZero();
    }

    @Test
    void dynamicDiscoveryRegistrationAcceptsCpuShapeNewAndRepeated() throws Exception {
        String body = """
                {
                  "cloudProvider": "ON-PREM",
                  "clusterDescription": "dynamic cpu shape",
                  "clusterGroupName": "cg-cpu-dynamic",
                  "clusterName": "cpu-cluster-dynamic",
                  "ncaId": "dummy_nca_id_1",
                  "nvcaVersion": "2.0.0",
                  "authorizedNCAIds": ["dummy_byoc_nca_id"],
                  "capabilities": ["DynamicGPUDiscovery"],
                  "attributes": [],
                  "oAuthClientId": "nvssa-stg-dummy_cpu_dyn",
                  "region": "US-West-1"
                }
                """;
        String clusterId = createCluster(body);

        // The first registration adds the CPU shape, the second repeats it unchanged. Both are
        // validated, and both pass while CPU workloads are ready.
        String repeated = registrationWithCpuCores(4);
        mockMvc.perform(MockMvcRequestBuilders.put(REGISTER_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON).content(repeated)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isOk());
        mockMvc.perform(MockMvcRequestBuilders.put(REGISTER_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON).content(repeated)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isOk());

        // A changed CPU shape arrives as a new entry through the NVCA registration path.
        String changed = registrationWithCpuCores(8);
        mockMvc.perform(MockMvcRequestBuilders.put(REGISTER_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON).content(changed)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isOk());
        ClusterEntity stored = clusterRepository.getClusterInfoByClusterId(clusterId, true).orElseThrow();
        assertThat(stored.getGpusV5()).filteredOn(g -> "CPU".equals(g.getName()))
                .flatExtracting(g -> g.getInstanceTypes())
                .extracting("cpuCores").containsExactly(8);

        // Two CPU shapes in one cluster are refused in this release.
        String two = registrationWithTwoCpuShapes();
        mockMvc.perform(MockMvcRequestBuilders.put(REGISTER_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON).content(two)
                                .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                .andExpect(MockMvcResultMatchers.status().isBadRequest());

        // A shape NVCA could not build is refused: a value that differs from the name, memory or
        // storage that is not a positive quantity, no storage, and a name that is not a label value.
        String goodStorage = ", \"storage\": \"80G\"";
        for (String bad : new String[] {
            cpuInstanceTypeWith(CPU_SHAPE, "NCP.CPU.8c32g", "16G", goodStorage),
            cpuInstanceTypeWith(CPU_SHAPE, CPU_SHAPE, "lots", goodStorage),
            cpuInstanceTypeWith(CPU_SHAPE, CPU_SHAPE, "0Gi", goodStorage),
            cpuInstanceTypeWith(CPU_SHAPE, CPU_SHAPE, "16G", ""),
            cpuInstanceTypeWith(CPU_SHAPE, CPU_SHAPE, "16G", ", \"storage\": \"big\""),
            cpuInstanceTypeWith("NCP.CPU.bad-", "NCP.CPU.bad-", "16G", goodStorage)}) {
            mockMvc.perform(MockMvcRequestBuilders.put(REGISTER_URL, clusterId)
                                    .contentType(MediaType.APPLICATION_JSON)
                                    .content(registrationWithCpuInstanceType(bad))
                                    .header(HttpHeaders.AUTHORIZATION, clusterAuth(clusterId)))
                    .andExpect(MockMvcResultMatchers.status().isBadRequest())
                    // The CPU shape validator's own message, so a bad shape was not stopped by an
                    // earlier request check.
                    .andExpect(result -> assertThat(result.getResponse().getContentAsString())
                            .contains("is reserved for CPU-only workloads"));
        }
    }

    private static String cpuInstanceTypeWith(String name, String value, String memory, String storageField) {
        return """
                {"name": "%s", "value": "%s", "description": "cpu", "default": true, "cpuCores": 4,
                 "systemMemory": "%s", "gpuCount": 0, "cpuArch": "x86",
                 "os": "Linux 64 bit"%s}
                """.formatted(name, value, memory, storageField);
    }

    private static String registrationWithCpuInstanceType(String instanceType) {
        return """
                {"status": "READY", "k8sVersion": "v1.29.2", "gpus": [%s,
                 {"name": "CPU", "capacity": 10, "instanceTypes": [%s]}]}
                """.formatted(B200_GPU, instanceType);
    }

    private static String cpuInstanceType(String name, int cores) {
        return """
                {"name": "%s", "value": "%s", "description": "cpu", "default": %s, "cpuCores": %d,
                 "systemMemory": "16G", "gpuCount": 0, "cpuArch": "x86",
                 "os": "Linux 64 bit", "storage": "80G"}
                """.formatted(name, name, name.equals(CPU_SHAPE), cores);
    }

    private static final String B200_GPU = """
            {"name": "B200", "capacity": 1,
             "instanceTypes": [{"name": "Standard_ND96amsr_B200_v4_1x", "value": "Standard_ND96amsr_B200_v4",
               "description": "Single 80 GB B200 GPU", "default": true, "cpuCores": 4,
               "systemMemory": "16G", "gpuMemory": "80G", "gpuCount": 1, "cpuArch": "x86",
               "os": "Linux 64 bit", "driverVersion": "v.353.145.36", "storage": "80G"}]}
            """;

    private static String registrationWithCpuCores(int cores) {
        return """
                {"status": "READY", "k8sVersion": "v1.29.2", "gpus": [%s,
                 {"name": "CPU", "capacity": 10, "instanceTypes": [%s]}]}
                """.formatted(B200_GPU, cpuInstanceType(CPU_SHAPE, cores));
    }

    private static String registrationWithTwoCpuShapes() {
        return """
                {"status": "READY", "k8sVersion": "v1.29.2", "gpus": [%s,
                 {"name": "CPU", "capacity": 10, "instanceTypes": [%s, %s]}]}
                """.formatted(B200_GPU, cpuInstanceType(CPU_SHAPE, 8),
                              cpuInstanceType("NCP.CPU.8c32g", 8));
    }
}
