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

import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.DESCRIBE_SPOT_INSTANCE_REQUESTS;
import static com.nvidia.icms.util.JwtKeyUtils.getAuthHeader;
import static com.nvidia.icms.util.TestUtil.ACTION;
import static com.nvidia.icms.util.TestUtil.ATTRIBUTES_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENCODED_INFERENCE_CONTAINER_ENV;
import static com.nvidia.icms.util.TestUtil.DUMMY_MESSAGE_BATCH_ID;
import static com.nvidia.icms.util.TestUtil.INSTANCE_TYPES_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.NGC_CLUSTER_NAME_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.NGC_GPU_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.NGC_REGION_LISTING_SCOPE;
import static com.nvidia.icms.util.TestUtil.PUBLIC_SIS_ENDPOINT;
import static com.nvidia.icms.util.TestUtil.SPOT_INSTANCE_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.SPOT_REQUEST_SCOPE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.amazonaws.services.sqs.AmazonSQS;
import com.amazonaws.services.sqs.model.SendMessageBatchResult;
import com.amazonaws.services.sqs.model.SendMessageBatchResultEntry;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.byoc.AwsQueueAccessInfo;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaRegistrationResponse;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import java.nio.charset.StandardCharsets;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.Mockito;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.core.io.ClassPathResource;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.mock.web.MockHttpServletResponse;
import org.springframework.test.annotation.DirtiesContext;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.MvcResult;
import org.springframework.test.web.servlet.request.MockHttpServletRequestBuilder;
import org.springframework.test.web.servlet.request.MockMvcRequestBuilders;
import org.springframework.test.web.servlet.result.MockMvcResultMatchers;
import org.springframework.util.LinkedMultiValueMap;
import org.springframework.util.MultiValueMap;


@DirtiesContext(classMode = DirtiesContext.ClassMode.AFTER_EACH_TEST_METHOD)
public class IcmsTest extends IntegrationTest {
    private static final String ClUSTER_CREATION_URL = "/v1/accounts/{ncaId}/clusters";
    private static final String NVCA_REGISTRATION_URL = "/v1/nvca/clusters/{clusterId}/register";
    private static final String GPU_API_URL = "/v1/si/accounts/{ncaId}/gpus";
    private static final String REGIONS_API_URL = "/v1/si/accounts/{ncaId}/regions";
    private static final String CLUSTER_NAMES_API_URL = "/v1/si/accounts/{ncaId}/clusterNames";
    private static final String ATTRIBUTES_API_URL = "/v1/si/accounts/{ncaId}/attributes";
    private static final String INSTANCE_TYPES_API_URL = "/v1/si/accounts/{ncaId}/instanceTypes";
    private final String NVCA_RECORD_HEARTBEAT_URL = "/v1/nvca/clusters/{clusterId}/heartbeat";

    private static final String[] EMPTY_ARRAY = {};
    public static final String KEY_SPACE = "test";
    public static final String CLUSTER_CREATE_REQUEST_PATH = "requests/cluster_create_request.json";
    public static final String REGISTER_CLUSTER_REQUEST_PATH =
            "requests/register_cluster_request.json";
    public static final String SSA_CLIENT_ID_1 = "nvssa-stg-dummy_1";
    public static final String SSA_CLIENT_ID_2 = "nvssa-stg-dummy_2";

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ClusterRepository clusterRepository;

    @MockitoBean
    AmazonSQS client;

    @Autowired
    private ObjectMapper objectMapper;

    @BeforeEach
    void beforeEach() {

        SendMessageBatchResult result = Mockito.mock(SendMessageBatchResult.class);
        when(result.getSuccessful()).thenReturn(List.of(new SendMessageBatchResultEntry()));
        when(client.sendMessageBatch(any(), any())).thenReturn(result);
    }

    @Test
    void testMultipleTerminationQueue() throws Exception {
        String clusterId1 = createCluster("cluster1", "dummy_nca_id_1", SSA_CLIENT_ID_1);
        NvcaRegistrationResponse registrationResponse1 = registerCluster(clusterId1);
        validateRegistrationResonse(registrationResponse1);
        var clusterCreationQueues1 =
                registrationResponse1.getCredentials().getClusterCreationQueue();
        var terminationQueue1 = registrationResponse1.getCredentials().getTerminationQueue();
        sendClusterHeartbeat("requests/cluster_heartbeat.json", clusterId1);

        String clusterId2 = createCluster("cluster2", "dummy_nca_id_2", SSA_CLIENT_ID_2);
        NvcaRegistrationResponse registrationResponse2 = registerCluster(clusterId2);
        validateRegistrationResonse(registrationResponse2);
        var clusterCreationQueues2 =
                registrationResponse2.getCredentials().getClusterCreationQueue();
        var terminationQueue2 = registrationResponse2.getCredentials().getTerminationQueue();
        sendClusterHeartbeat("requests/cluster_heartbeat.json", clusterId2);

        var gpuName = "B200";
        var instanceType = "Standard_ND96amsr_B200_v4_1x";
        CreateSpotInstancesResponse instancesResponse =
                requestInstances(instanceType, gpuName, Set.of(), Set.of(), Set.of());
        validateInstanceRequestResponse(Set.of(clusterCreationQueues1, clusterCreationQueues2), gpuName,
                                    instancesResponse);

        var instanceRequestId = instancesResponse.getRequestId();
        // update status pending
        requestUpdateStatusPending(instanceRequestId, clusterId1);

        // update status running
        requestUpdateStatusRunning(instanceRequestId, clusterId1, "instance1");
        requestUpdateStatusRunning(instanceRequestId, clusterId2, "instance2");

        // Describe instance requests
        GetSpotInstanceRequests instanceRequestsResponse = describeInstance(instanceRequestId);
        assertNotNull(instanceRequestsResponse);

        // terminate instance
        TerminateInstancesResponse terminateInstancesResponse =
                terminateInstance(instancesResponse.getRequestId());
        validateTerminationResponse(terminateInstancesResponse,
                                    List.of(terminationQueue1, terminationQueue2));
    }

    @Test
    void testRequestInstances() throws Exception {
        String clusterId = createCluster("cluster4", "dummy_nca_id_1", SSA_CLIENT_ID_1);
        NvcaRegistrationResponse registrationResponse = registerCluster(clusterId);
        validateRegistrationResonse(registrationResponse);
        sendClusterHeartbeat("requests/cluster_heartbeat.json", clusterId);
        var clusterCreationQueues = registrationResponse.getCredentials().getClusterCreationQueue();
        var terminationQueue = registrationResponse.getCredentials().getTerminationQueue();

        Set<String> gpus = requestGpus();
        // validateGpus(gpus);

        String[] gpusArray = gpus.toArray(new String[] {});
        Set<String> regions = requestRegions(gpusArray, EMPTY_ARRAY, EMPTY_ARRAY);
        // validateRegions(regions);

        String[] regionsArray = regions.toArray(new String[] {});
        Set<String> clusters = requestClusters(gpusArray, regionsArray, EMPTY_ARRAY);
        // validateClusters(clusters);

        String[] clustersArray = clusters.toArray(new String[] {});
        Set<String> attributes = requestAttributes(gpusArray, clustersArray, regionsArray);
        // validateAttributes(attributes);

        String[] attributesArray = attributes.toArray(new String[] {});
        Map<String, List<Map<String, String>>> instanceTypes =
                requestInstanceTypes(gpusArray, clustersArray, attributesArray, regionsArray);
        // validateInstanceTypes(instanceTypes);

        // request instance
        var gpuName = "B200";
        var instanceType = instanceTypes.get(gpuName).stream().findAny().orElseThrow().get("name");
        CreateSpotInstancesResponse instancesResponse =
                requestInstances(instanceType, gpuName, regions, clusters, attributes);
        validateInstanceRequestResponse(Set.of(clusterCreationQueues), gpuName, instancesResponse);

        // update status pending
        requestUpdateStatusPending(instancesResponse.getRequestId(), clusterId);

        // update status running
        requestUpdateStatusRunning(instancesResponse.getRequestId(), clusterId, "instance1");

        // Describe instance requests
        GetSpotInstanceRequests instanceRequestsResponse =
                describeInstance(instancesResponse.getRequestId());
        assertNotNull(instanceRequestsResponse);

        // terminate instance
        TerminateInstancesResponse terminateInstancesResponse =
                terminateInstance(instancesResponse.getRequestId());
        validateTerminationResponse(terminateInstancesResponse, List.of(terminationQueue));
    }

    private void validateTerminationResponse(TerminateInstancesResponse terminateInstancesResponse,
                                             List<AwsQueueAccessInfo> terminationQueues) {
        assertNotNull(terminateInstancesResponse.getTerminatingInstances());
        for (AwsQueueAccessInfo terminationQueue : terminationQueues) {
            verify(client, times(1)).sendMessageBatch(eq(terminationQueue.getUrl()), any());
        }
    }

    private void validateInstanceRequestResponse(
            Set<Map<String, AwsQueueAccessInfo>> clusterCreationQueuesSet, String gpuName,
            CreateSpotInstancesResponse instancesResponse) {
        for (Map<String, AwsQueueAccessInfo> clusterCreationQueues : clusterCreationQueuesSet) {
            verify(client, times(1)).sendMessageBatch(
                    eq(clusterCreationQueues.get(gpuName).getUrl()), any());
        }
        assertNotNull(instancesResponse);
        assertNotNull(instancesResponse.getRequestId());
    }

    private void validateRegistrationResonse(NvcaRegistrationResponse registrationResponse) {
        assertThat(registrationResponse.getCredentials().getCreationQueue()).isNotEmpty();
        assertThat(registrationResponse.getCredentials().getClusterCreationQueue()).isNotEmpty();
        assertThat(registrationResponse.getCredentials().getTerminationQueue()).isNotNull();
        Optional<ClusterEntity> clusterInfoByClusterId =
                clusterRepository.getClusterInfoByClusterId(registrationResponse.getClusterId(),
                                                            true);
        assertThat(clusterInfoByClusterId).isNotEmpty();
        assertThat(clusterInfoByClusterId.get().getGpusV5()).isNotEmpty();
        assertThat(clusterInfoByClusterId.get().getGpusV5()
                           .stream().findAny().get().getInstanceTypes().stream().findAny()
                           .get().getStorage()).isNotEmpty();
    }

    private GetSpotInstanceRequests describeInstance(UUID requestId) throws Exception {
        HttpHeaders httpHeaders = new HttpHeaders();
        httpHeaders.add("Authorization", getAuthHeader(DUMMY_CUSTOMER_ID, SPOT_REQUEST_SCOPE));
        MockHttpServletResponse response = mockMvc.perform(
                        MockMvcRequestBuilders.get(PUBLIC_SIS_ENDPOINT).headers(httpHeaders)
                                .param(ACTION, DESCRIBE_SPOT_INSTANCE_REQUESTS.getRequestAction())
                                .param(SPOT_INSTANCE_REQUEST_ID, requestId.toString()))
                .andExpect(status().isOk()).andReturn().getResponse();

        return objectMapper.readValue(response.getContentAsString(), GetSpotInstanceRequests.class);
    }

    private void requestUpdateStatusRunning(UUID requestId, String clusterId, String instance)
            throws Exception {
        SpotInstanceStatusUpdateRequest request = SpotInstanceStatusUpdateRequest.builder()
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .instanceState(SpotInstanceInternalState.RUNNING)
                .requestState(SpotInstanceRequestState.ACTIVE).status(SpotInstanceStatus.FULFILLED)
                .imageId("test-image").build();
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        HttpHeaders httpHeaders = new HttpHeaders();
        httpHeaders.add("Authorization", getAuthHeader(clusterId, "spot-status-update"));
        mockMvc.perform(MockMvcRequestBuilders.post("/v1/sirs/{spotInstanceRequestId}/{instanceId}",
                                                    requestId, instance)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString).headers(httpHeaders))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn().getResponse();
    }

    private void requestUpdateStatusPending(UUID requestId, String clusterId) throws Exception {
        SpotInstanceRequestStatusUpdateRequest request =
                new SpotInstanceRequestStatusUpdateRequest();
        request.setStatus(SpotRequestStatusCode.PENDING_FULFILLMENT);
        request.setMessageBatchId(DUMMY_MESSAGE_BATCH_ID);
        request.setInstanceCount(1);
        String requestBodyJsonString = objectMapper.writeValueAsString(request);

        HttpHeaders httpHeaders = new HttpHeaders();
        httpHeaders.add("Authorization", getAuthHeader(clusterId, "spot-status-update"));
        mockMvc.perform(MockMvcRequestBuilders.put("/v1/sirs/{spotInstanceRequestId}", requestId)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(requestBodyJsonString).headers(httpHeaders))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn().getResponse();
    }

    private TerminateInstancesResponse terminateInstance(UUID requestId) throws Exception {
        HttpHeaders httpHeaders = new HttpHeaders();
        httpHeaders.add("Authorization", getAuthHeader(DUMMY_CUSTOMER_ID, "spot-request"));
        MockHttpServletResponse mockMvcResponse = mockMvc.perform(
                        MockMvcRequestBuilders.delete("/v1/si").headers(httpHeaders).param("Action",
                                                                                           SpotInstanceRequestAction.TERMINATE_SPOT_INSTANCE_REQUEST.getRequestAction())
                                .param("RequestId", requestId.toString()))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn().getResponse();

        return objectMapper.readValue(mockMvcResponse.getContentAsString(),
                                      TerminateInstancesResponse.class);
    }

    private CreateSpotInstancesResponse requestInstances(String instanceType, String gpuName,
                                                             Set<String> regions,
                                                             Set<String> clusters,
                                                             Set<String> attributes)
            throws Exception {

        HttpHeaders httpHeaders = new HttpHeaders();
        httpHeaders.add("Authorization", getAuthHeader(DUMMY_CUSTOMER_ID, "spot-request"));

        MultiValueMap<String, String> formParams = new LinkedMultiValueMap<>();
        formParams.put("LaunchSpecification.Clusters", clusters.stream().toList());
        formParams.put("LaunchSpecification.Regions", regions.stream().toList());
        formParams.put("LaunchSpecification.Attributes", attributes.stream().toList());

        MockHttpServletResponse mockMvcResponse = mockMvc.perform(
                        MockMvcRequestBuilders.post("/v1/si").headers(httpHeaders)
                                .param("Action", "RequestSpotInstances")
                                .param("LaunchSpecification.InstanceType", instanceType)
                                .param("LaunchSpecification.NcaId", DUMMY_BYOC_NCA_ID)
                                .param("LaunchSpecification.Gpu", gpuName)
                                .param("LaunchSpecification.ContainerImage", DUMMY_CONTAINER_IMAGE)
                                .param("LaunchSpecification.Environment",
                                       DUMMY_ENCODED_INFERENCE_CONTAINER_ENV)
                                .param("LaunchSpecification.CredentialRef", "").param("InstanceCount", "2")
                                .param("FunctionDetails.FunctionId", UUID.randomUUID().toString())
                                .param("FunctionDetails.FunctionVersionId", UUID.randomUUID().toString())
                                .param("FunctionDetails.OwnerNcaId", "dummy-owner-nca-id")
                                .param("FunctionDetails.FunctionType", "STREAMING").params(formParams))
                .andExpect(MockMvcResultMatchers.status().isAccepted()).andReturn().getResponse();

        return GsonCompatMapper.fromJson(mockMvcResponse.getContentAsString(),
                             CreateSpotInstancesResponse.class);
    }

    @SuppressWarnings("unchecked")
    private Map<String, List<Map<String, String>>> requestInstanceTypes(String[] gpus,
                                                                        String[] clusters,
                                                                        String[] attributes,
                                                                        String[] regions)
            throws Exception {
        MvcResult mvcResult =
                request(INSTANCE_TYPES_API_URL, INSTANCE_TYPES_LISTING_SCOPE, gpus, clusters,
                        attributes, regions);
        return GsonCompatMapper.fromJson(mvcResult.getResponse().getContentAsString(),
                             (Class<Map<String, List<Map<String, String>>>>) (Object) Map.class);
    }

    @SuppressWarnings("unchecked")
    private Set<String> requestAttributes(String[] gpus, String[] clusters, String[] regions)
            throws Exception {
        MvcResult mvcResult =
                request(ATTRIBUTES_API_URL, ATTRIBUTES_LISTING_SCOPE, gpus, clusters, EMPTY_ARRAY,
                        regions);
        Map<String, List<String>> attributes =
                objectMapper.readValue(mvcResult.getResponse().getContentAsString(),
                                       (Class<Map<String, List<String>>>) (Object) Map.class);
        return new HashSet<>(attributes.get("attributes"));
    }

    @SuppressWarnings("unchecked")
    private Set<String> requestClusters(String[] gpus, String[] regions, String[] attributes)
            throws Exception {
        MvcResult mvcResult =
                request(CLUSTER_NAMES_API_URL, NGC_CLUSTER_NAME_LISTING_SCOPE, gpus, EMPTY_ARRAY,
                        attributes, regions);
        Map<String, List<String>> clusters =
                objectMapper.readValue(mvcResult.getResponse().getContentAsString(),
                                       (Class<Map<String, List<String>>>) (Object) Map.class);
        return new HashSet<>(clusters.get("clusterNames"));
    }

    @SuppressWarnings("unchecked")
    private Set<String> requestRegions(String[] gpus, String[] clusters, String[] attributes)
            throws Exception {
        MvcResult mvcResult =
                request(REGIONS_API_URL, NGC_REGION_LISTING_SCOPE, gpus, clusters, attributes,
                        EMPTY_ARRAY);
        Map<String, List<String>> regions =
                objectMapper.readValue(mvcResult.getResponse().getContentAsString(),
                                       (Class<Map<String, List<String>>>) (Object) Map.class);
        return new HashSet<>(regions.get("regions"));
    }

    @SuppressWarnings("unchecked")
    private Set<String> requestGpus() throws Exception {
        MvcResult mvcResult =
                request(GPU_API_URL, NGC_GPU_LISTING_SCOPE, EMPTY_ARRAY, EMPTY_ARRAY, EMPTY_ARRAY,
                        EMPTY_ARRAY);
        Map<String, List<String>> gpus =
                objectMapper.readValue(mvcResult.getResponse().getContentAsString(),
                                       (Class<Map<String, List<String>>>) (Object) Map.class);
        return new HashSet<>(gpus.get("gpus"));
    }

    private NvcaRegistrationResponse registerCluster(String clusterId) throws Exception {
        String request = readClasspathResource(REGISTER_CLUSTER_REQUEST_PATH);
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.put(NVCA_REGISTRATION_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON).content(request)
                                .header(HttpHeaders.AUTHORIZATION, JwtKeyUtils.getAuthHeader(clusterId,
                                                                                             TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
        return objectMapper.readValue(mvcResult.getResponse().getContentAsString(),
                                      NvcaRegistrationResponse.class);
    }

    private String createCluster(String clusterName, String ncaId, String oAuthClientId)
            throws Exception {
        String request = readClasspathResource(CLUSTER_CREATE_REQUEST_PATH).replace(
                "${clusterName}", clusterName).replace("${ncaId}", ncaId)
                .replace("${oAuthClientId}", oAuthClientId);

        String ssaAuthHeader = getAuthHeader(ncaId, TestUtil.NGC_CLUSTER_MANAGEMENT_SCOPE);
        MvcResult mvcResult = mockMvc.perform(
                        MockMvcRequestBuilders.post(ClUSTER_CREATION_URL, ncaId)
                                .contentType(MediaType.APPLICATION_JSON).content(request)
                                .header(HttpHeaders.AUTHORIZATION, ssaAuthHeader))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
        return objectMapper.readValue(mvcResult.getResponse().getContentAsString(),
                                      ObjectNode.class).get("clusterId").textValue();
    }

    private void sendClusterHeartbeat(String path, String clusterId)
            throws Exception {
        String request = readClasspathResource(path);


        mockMvc.perform(MockMvcRequestBuilders.post(NVCA_RECORD_HEARTBEAT_URL, clusterId)
                                .contentType(MediaType.APPLICATION_JSON)
                                .content(request)
                                .header(HttpHeaders.AUTHORIZATION,
                                        JwtKeyUtils.getAuthHeader(clusterId,
                                                                  TestUtil.NVCA_CLUSTER_REGISTRATION_SCOPE)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
    }

    private static String readClasspathResource(String path) throws Exception {
        try (var inputStream = new ClassPathResource(path).getInputStream()) {
            return new String(inputStream.readAllBytes(), StandardCharsets.UTF_8);
        }
    }

    private MvcResult request(String url, String scope, String[] gpus, String[] clusters,
                              String[] attributes, String[] regions) throws Exception {
        MockHttpServletRequestBuilder requestBuilder =
                MockMvcRequestBuilders.get(url, DUMMY_BYOC_NCA_ID);

        applyParamFilers(requestBuilder, gpus, clusters, attributes, regions);

        return mockMvc.perform(requestBuilder.contentType(MediaType.APPLICATION_JSON)
                                       .header(HttpHeaders.AUTHORIZATION,
                                               JwtKeyUtils.getAuthHeader(DUMMY_CUSTOMER_1,
                                                                         scope)))
                .andExpect(MockMvcResultMatchers.status().isOk()).andReturn();
    }

    private static void applyParamFilers(MockHttpServletRequestBuilder requestBuilder,
                                         String[] gpus, String[] clusters, String[] attributes,
                                         String[] regions) {
        if (gpus.length > 0) {
            requestBuilder.param("gpus", gpus);
        }
        if (clusters.length > 0) {
            requestBuilder.param("clusters", clusters);
        }
        if (attributes.length > 0) {
            requestBuilder.param("attributes", attributes);
        }
        if (regions.length > 0) {
            requestBuilder.param("regions", regions);
        }
    }
}
