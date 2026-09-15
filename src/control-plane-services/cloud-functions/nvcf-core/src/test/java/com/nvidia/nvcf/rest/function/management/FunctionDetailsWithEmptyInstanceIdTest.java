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
package com.nvidia.nvcf.rest.function.management;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LIST_FUNCTIONS_DETAILS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.icms.client.IcmsStubService;
import com.nvidia.nvcf.icms.client.IcmsStubService.GetInstancesResponse.InstanceRequest.InstanceState;
import com.nvidia.nvcf.icms.client.IcmsStubService.GetInstancesResponse.InstanceRequest.Placement;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@AutoConfigureTestRestTemplate
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class FunctionDetailsWithEmptyInstanceIdTest {

    private static final String VALID_INSTANCE_ID = "valid-instance-id";

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestManagementService managementService;

    @Autowired
    private TestDeploymentService deploymentService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @BeforeAll
    void beforeAll() {
        MockIcmsServer.start(9096, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        MockEssServer.start(essBaseUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void afterAll() {
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockEssServer.stop();
    }

    @AfterEach
    void afterEach() {
        testCommonService.reset();
    }

    @Test
    void shouldIgnoreActiveSisInstanceWithoutInstanceIdInFunctionDetails()
            throws JsonProcessingException {
        managementService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);
        deploymentService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);
        stubSisInstancesResponse();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(
                TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_FUNCTIONS_DETAILS), 100);
        var request = RequestEntity.get(
                        "/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                + "/versions/" + TEST_VERSION_ID_1)
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                .build();

        var response = testRestTemplate.exchange(request, FunctionResponse.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().function().activeInstances())
                .singleElement()
                .extracting(instance -> instance.getInstanceId())
                .isEqualTo(VALID_INSTANCE_ID);
    }

    private void stubSisInstancesResponse() throws JsonProcessingException {
        var instances = IcmsStubService.Instances.builder()
                .Instances(List.of(
                        runningInstance(null),
                        runningInstance(VALID_INSTANCE_ID)))
                .build();
        var path = "/v1/si/accounts/" + TEST_NCA_ID
                + "/workloads/" + TEST_DEPLOYMENT_ID + "/instances";
        MockIcmsServer.getMockIcmsServer().stubFor(
                get(urlPathEqualTo(path))
                        .atPriority(1)
                        .willReturn(aResponse()
                                .withStatus(HttpStatus.OK.value())
                                .withHeader(HttpHeaders.CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                .withBody(jsonMapper.writeValueAsString(instances))));
    }

    private static IcmsStubService.Instance runningInstance(String instanceId) {
        var now = Instant.parse("2024-05-03T15:04:14.830Z");
        return IcmsStubService.Instance.builder()
                .createTime(now)
                .instanceId(instanceId)
                .cloudProvider("GFN")
                .instanceType("g6.full")
                .placement(Placement.builder().availabilityZone("NQ-SJC6B-01").build())
                .state(InstanceState.builder().code(16).name("running").build())
                .launchRequestId(UUID.randomUUID().toString())
                .deploymentId(TEST_DEPLOYMENT_ID)
                .gpuSpecificationId(TEST_GPU_SPEC_ID)
                .build();
    }
}
