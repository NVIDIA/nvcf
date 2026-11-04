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
package com.nvidia.nvcf.rest.function.deployment;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.TestAuthorizedPartiesService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
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
import org.springframework.http.ProblemDetail;
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
class PublicFunctionDeploymentTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestDeploymentService testDeploymentService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    private List<GpuSpecificationDto> validGpuSpecDtos = List.of(
            GpuSpecificationDto.builder()
                    .gpu(T10).backend(GFN).instanceType(T10_INSTANCE_TYPE)
                    .maxInstances(8).minInstances(6).maxRequestConcurrency(9).build(),
            GpuSpecificationDto.builder()
                    .gpu(L40G).backend(GFN).instanceType(L40G_INSTANCE_TYPE)
                    .maxInstances(9).minInstances(7).maxRequestConcurrency(99).build());

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        resetToDefault();
    }

    Stream<Arguments> authDeploymentForPublicFunctionArgs() {
        return Stream.of(
                // JWT - Account Admin
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100)),
                // apikey - Authorized with account-functions wildcard
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of(SCOPE_DEPLOY_FUNCTION));
                    return "nvapi-api-key";
                }),
                // apikey - Authorized for specific function with wildcard versions
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", TEST_FUNCTION_ID + "/*")),
                                List.of(SCOPE_DEPLOY_FUNCTION));
                    return "nvapi-api-key";
                }),
                // apikey - Authorized for specific function and version
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                List.of(SCOPE_DEPLOY_FUNCTION));
                    return "nvapi-api-key";
                })
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("authDeploymentForPublicFunctionArgs")
    void shouldFailToCreateDeploymentForPublicFunction(Object tokenSupplier) {
        // Account Admin cannot perform this operation.
        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Associate  AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        var token = getToken(tokenSupplier);
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(validGpuSpecDtos).build();
        var requestEntity = RequestEntity.post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                                                       + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot create deployment for public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("authDeploymentForPublicFunctionArgs")
    void shouldFailToUpdateDeploymentForPublicFunction(Object tokenSupplier) {
        // Account Admin cannot perform this operation.
        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        var token = getToken(tokenSupplier);
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(validGpuSpecDtos).build();
        var requestEntity = RequestEntity.put("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                                                      + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot update deployment for public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("authDeploymentForPublicFunctionArgs")
    void shouldFailToDeleteDeploymentForPublicFunction(Object tokenSupplier) {
        // Account Admin cannot perform this operation.
        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        var token = getToken(tokenSupplier);
        var requestEntity =
                RequestEntity.delete("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                                             + "/versions/" + TEST_VERSION_ID_1)
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot delete deployment for public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    Stream<Arguments> listDeploymentForPublicFunctionArgs() {
        return Stream.of(
                // JWT - Account Admin (success)
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             HttpStatus.OK),
                // apikey - No resource entries in policy
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-api-key";
                             },
                             HttpStatus.FORBIDDEN),
                // apikey - Incorrect resource type
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("authorized-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-api-key";
                             },
                             HttpStatus.FORBIDDEN),
                // apikey - Authorized with account-functions wildcard
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-api-key";
                             },
                             HttpStatus.OK),
                // apikey - Authorized for specific function with wildcard versions
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-api-key";
                             },
                             HttpStatus.OK),
                // apikey - Authorized for specific function and version
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-api-key";
                             },
                             HttpStatus.OK)
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("listDeploymentForPublicFunctionArgs")
    void shouldListDeploymentForPublicFunction(Object tokenSupplier, HttpStatus expectedStatus) {
        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID and deploy it.
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);
        testDeploymentService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                     TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        var token = getToken(tokenSupplier);
        // Get function deployment
        var requestEntity = RequestEntity.get("/v2/nvcf/deployments"
                                                      + "/functions/" + TEST_FUNCTION_ID
                                                      + "/versions/" + TEST_VERSION_ID_1)
                .header("Authorization", "Bearer " + token).build();
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployment().functionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.deployment().functionStatus()).isEqualTo(DEPLOYING);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSize(2);
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("authDeploymentForPublicFunctionArgs")
    void shouldFailToUpdateGpuSpecificationForPublicFunction(Object tokenSupplier) {
        // Account Admin cannot perform this operation for public functions.
        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID and deploy it.
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);
        testDeploymentService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                     TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        var token = getToken(tokenSupplier);
        var requestBody = UpdateGpuSpecificationRequest.builder()
                .minInstances(0).maxInstances(10).build();
        var requestEntity = RequestEntity
                .patch("/v2/nvcf/deployments/" + TEST_DEPLOYMENT_ID
                               + "/gpu-specifications/" + TEST_GPU_SPEC_ID)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot update deployment for public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }
}
