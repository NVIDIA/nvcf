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
package com.nvidia.nvcf.rest.misc;

import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.github.tomakehurst.wiremock.matching.EqualToPattern;
import com.github.tomakehurst.wiremock.matching.RequestPatternBuilder;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentDto;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.misc.dto.RolloverRequest;
import com.nvidia.nvcf.rest.misc.dto.RolloverSpecificationDto;
import com.nvidia.nvcf.rest.misc.dto.RolloverWorkersResponse;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.net.URI;
import java.util.Collections;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class RolloverWorkersTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private FunctionDeploymentService deploymentService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    private AuditEventPayload.Builder auditEventPayloadBuilder;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        auditEventPayloadBuilder = testCommonService.getAuditEventPayloadBuilder();
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @BeforeEach
    void init() {
        MockIcmsServer.start(9096, jsonMapper);
        // Create test function.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockEssServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockIcmsServer.stop();
        MockEssServer.clearSecrets();
    }

    Stream<Arguments> rolloverWorkersUsingRolloverSpecArgs() {
        var validSpec = RolloverSpecificationDto.builder().numInstances(3).build();
        var missingGpuSpecId = RolloverSpecificationDto.builder().numInstances(3).build();
        var missingNumInstances = RolloverSpecificationDto.builder().gpuSpecId(UUID.randomUUID())
                .build();
        var zeroNumInstances = RolloverSpecificationDto.builder().gpuSpecId(UUID.randomUUID())
                .numInstances(0).build();
        var negativeNumInstances = RolloverSpecificationDto.builder().gpuSpecId(UUID.randomUUID())
                .numInstances(-2).build();
        var numInstancesHigherThanMaxInstances =
                RolloverSpecificationDto.builder().numInstances(100).build();
        return Stream.of(
                Arguments.of(null, List.of(validSpec), false, false, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", List.of(validSpec), false, false,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(), 100),
                             List.of(validSpec),
                             false,
                             false,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             List.of(),
                             false,
                             false,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             List.of(missingGpuSpecId),
                             false,
                             true,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             List.of(missingNumInstances),
                             false,
                             false,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             List.of(negativeNumInstances),
                             false,
                             false,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             List.of(zeroNumInstances),
                             false,
                             false,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             List.of(numInstancesHigherThanMaxInstances),
                             false,
                             false,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             List.of(validSpec),
                             true,
                             false,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                              List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             List.of(validSpec, validSpec),
                             false,
                             false,
                             HttpStatus.OK));
    }

    @ParameterizedTest
    @MethodSource("rolloverWorkersUsingRolloverSpecArgs")
    void shouldRolloverWorkersUsingRolloverSpec(
            String token,
            List<RolloverSpecificationDto> rolloverSpecs,
            boolean useUnknownGpuSpecId,
            boolean omitGpuSpecId,
            HttpStatus expectedStatus) {
        var dto = createDeployment();
        assertThat(dto.deploymentSpecifications().stream()
                           .map(GpuSpecificationDto::instanceType)
                           .collect(Collectors.toSet()))
                .containsExactlyInAnyOrderElementsOf(Set.of(T10_INSTANCE_TYPE, L40G_INSTANCE_TYPE));
        var deploymentId = dto.deploymentId();
        var firstGpuSpecificationId = useUnknownGpuSpecId
                ? UUID.randomUUID()
                : dto.deploymentSpecifications().stream()
                        .filter(spec -> T10_INSTANCE_TYPE.equals(spec.instanceType()))
                        .findFirst()
                        .orElseThrow()
                        .gpuSpecificationId();
        var secondGpuSpecificationId = dto.deploymentSpecifications().stream()
                .filter(spec -> L40G_INSTANCE_TYPE.equals(spec.instanceType()))
                .findFirst()
                .orElseThrow()
                .gpuSpecificationId();
        var requestSpecs = rolloverSpecs.stream()
                .map(spec -> RolloverSpecificationDto.builder()
                        .gpuSpecId(omitGpuSpecId ? null : spec.gpuSpecId() == null
                                ? firstGpuSpecificationId : spec.gpuSpecId())
                        .numInstances(spec.numInstances())
                        .build())
                .collect(Collectors.toList());
        if (requestSpecs.size() == 2) {
            requestSpecs.set(1, RolloverSpecificationDto.builder()
                    .gpuSpecId(secondGpuSpecificationId)
                    .numInstances(requestSpecs.get(1).numInstances())
                    .build());
        }
        var rolloverRequest = RolloverRequest.builder()
                .rolloverSpecifications(requestSpecs)
                .build();

        // Reset MockIcmsServer.
        MockIcmsServer.stop();
        MockIcmsServer.start(9096, jsonMapper);

        // Invoke endpoint to rollover the requested GPU specifications.
        var requestEntity = RequestEntity
                .put(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/rolloverWorkers"
                                        + "/deployments/" + deploymentId))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(rolloverRequest);
        var responseEntity = testRestTemplate.exchange(requestEntity, RolloverWorkersResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        // Confirm ICMS requests to spawn new workers.
        var expectedIcmsRequest = post(urlPathEqualTo("/v1/si"))
                                .withQueryParam("Action",
                                                new EqualToPattern("RequestInstances"))
                .build()
                .getRequest();
        MockIcmsServer.getMockIcmsServer()
                .verify(requestSpecs.size(), RequestPatternBuilder.like(expectedIcmsRequest));

        // Validate the ICMS request IDs returned in the response.
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.icmsRequestIds()).hasSize(requestSpecs.size()).doesNotContainNull();
    }

    @Test
    void shouldRejectDuplicateGpuSpecificationIds() {
        var deployment = createDeployment();
        resetMockIcmsServer();
        var gpuSpecId = deployment.deploymentSpecifications().getFirst().gpuSpecificationId();
        var rolloverSpec = RolloverSpecificationDto.builder()
                .gpuSpecId(gpuSpecId)
                .numInstances(5)
                .build();
        var request = RolloverRequest.builder()
                .rolloverSpecifications(List.of(rolloverSpec, rolloverSpec))
                .build();

        var response = rolloverWorkers(deployment.deploymentId(), request);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        verifyNoIcmsRequests();
    }

    @Test
    void shouldRejectNullRolloverSpecification() {
        var deployment = createDeployment();
        resetMockIcmsServer();
        var request = RolloverRequest.builder()
                .rolloverSpecifications(Collections.singletonList(null))
                .build();

        var response = rolloverWorkers(deployment.deploymentId(), request);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        verifyNoIcmsRequests();
    }

    private FunctionDeploymentDto createDeployment() {
        var specs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .instanceType("g6.full")
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(9).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN)
                        .instanceType("gl40g_1.br25_2xlarge")
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99).build());
        var request = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs)
                .build();
        deploymentService.createFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                   TEST_VERSION_ID_1, request,
                                                   auditEventPayloadBuilder,
                                                   x -> true);
        return testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID, TEST_VERSION_ID_1);
    }

    private void resetMockIcmsServer() {
        MockIcmsServer.stop();
        MockIcmsServer.start(9096, jsonMapper);
    }

    private org.springframework.http.ResponseEntity<RolloverWorkersResponse> rolloverWorkers(
            UUID deploymentId, RolloverRequest request) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(
                TEST_ADMIN_SUBJECT, List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100);
        var requestEntity = RequestEntity
                .put(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/rolloverWorkers"
                                        + "/deployments/" + deploymentId))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);
        return testRestTemplate.exchange(requestEntity, RolloverWorkersResponse.class);
    }

    private static void verifyNoIcmsRequests() {
        var expectedIcmsRequest = post(urlPathEqualTo("/v1/si"))
                .withQueryParam("Action", new EqualToPattern("RequestInstances"))
                .build()
                .getRequest();
        MockIcmsServer.getMockIcmsServer()
                .verify(0, RequestPatternBuilder.like(expectedIcmsRequest));
    }

}
