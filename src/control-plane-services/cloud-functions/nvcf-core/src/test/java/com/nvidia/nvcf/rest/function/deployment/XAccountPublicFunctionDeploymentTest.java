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
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
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
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentDto;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.ListDeploymentsResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
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
class XAccountPublicFunctionDeploymentTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestDeploymentService testDeploymentService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

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


    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
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
        resetToDefault();
    }

    Stream<Arguments> gpuSpecDeploymentArgs() {
        return Stream.of(
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                        List.of(
                                GpuSpecificationDto.builder()
                                        .gpu(T10).backend(GFN)
                                        .instanceType(T10_INSTANCE_TYPE).maxInstances(8)
                                        .minInstances(6).maxRequestConcurrency(9).build(),
                                GpuSpecificationDto.builder()
                                        .gpu(L40G).backend(GFN)
                                        .instanceType(L40G_INSTANCE_TYPE).maxInstances(9)
                                        .minInstances(7).maxRequestConcurrency(99).build()),
                        HttpStatus.OK,
                        FunctionStatusEnum.DEPLOYING),
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                        List.of(
                                GpuSpecificationDto.builder()
                                        .gpu(T10).backend(GFN)
                                        .instanceType(T10_INSTANCE_TYPE).maxInstances(8)
                                        .minInstances(0).maxRequestConcurrency(9).build(),
                                GpuSpecificationDto.builder()
                                        .gpu(L40G).backend(GFN)
                                        .instanceType(L40G_INSTANCE_TYPE).maxInstances(9)
                                        .minInstances(0).maxRequestConcurrency(99).build()),
                        HttpStatus.OK,
                        FunctionStatusEnum.ACTIVE));
    }

    Stream<Arguments> authDeploymentForPublicFunctionArgs() {
        var validGpuSpecs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .instanceType(T10_INSTANCE_TYPE).maxInstances(8)
                        .minInstances(6).maxRequestConcurrency(9).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN)
                        .instanceType(L40G_INSTANCE_TYPE).maxInstances(9)
                        .minInstances(7).maxRequestConcurrency(99).build());

        return Stream.of(
                // JWT - Authorized
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             validGpuSpecs,
                             HttpStatus.OK,
                             FunctionStatusEnum.DEPLOYING)
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource({"authDeploymentForPublicFunctionArgs", "gpuSpecDeploymentArgs"})
    void shouldCreateDeploymentForPublicFunction(
            Object tokenSupplier,
            List<GpuSpecificationDto> validGpuSpecDtos,
            HttpStatus expectedStatus,
            FunctionStatusEnum expectedFunctionStatus) {
        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

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
        var requestEntity = RequestEntity.post("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                       + "/deployments/functions/"
                                                       + TEST_FUNCTION_ID
                                                       + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployment().functionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.deployment().functionStatus()).isEqualTo(expectedFunctionStatus);
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSize(2);
    }


    @SneakyThrows
    @ParameterizedTest
    @MethodSource({"authDeploymentForPublicFunctionArgs", "gpuSpecDeploymentArgs"})
    void shouldUpdateDeploymentForPublicFunction(
            Object tokenSupplier,
            List<GpuSpecificationDto> validGpuSpecDtos,
            HttpStatus expectedStatus,
            FunctionStatusEnum ignoreFunctionStatus) {
        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID and deploy it.
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);
        testDeploymentService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                     TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // The above call creates a function deployment with two GPU specs with gpu-spec-ids
        // TEST_GPU_SPEC_ID(for T10) and TEST_GPU_SPEC_ID_2(for L40G). The gpu-spec DTOs that are
        // passed in do not have gpu-spec-id set. Set TEST_GPU_SPEC_ID and TEST_GPU_SPEC_ID_2 in
        // the DTOs that are passed in. By setting the gpu-spec-id selectively just for this
        // deprecated endpoint, we don't set gpu-spec-ids unnecessarily for other endpoints. It
        // is plain wrong to specify gpu-spec-id when creating a new deployment. When we delete
        // the legacy Update Function Deployment endpoint, this test will get deleted.
        var gpuSpecificationDtosWithIds = List.of(
                validGpuSpecDtos.get(0).toBuilder().gpuSpecificationId(TEST_GPU_SPEC_ID).build(),
                validGpuSpecDtos.get(1).toBuilder().gpuSpecificationId(TEST_GPU_SPEC_ID_2).build()
        );

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
                .deploymentSpecifications(gpuSpecificationDtosWithIds).build();
        var requestEntity = RequestEntity.put("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                      + "/deployments/functions/" + TEST_FUNCTION_ID
                                                      + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployment().functionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.deployment().functionStatus()).isEqualTo(
                FunctionStatusEnum.DEPLOYING);
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSize(2);
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("authDeploymentForPublicFunctionArgs")
    void shouldDeleteDeploymentForPublicFunction(
            Object tokenSupplier,
            List<GpuSpecificationDto> ignoredSpecs,
            HttpStatus expectedStatus,
            FunctionStatusEnum ignoredStatus) {
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
        var requestEntity = RequestEntity.delete("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                         + "/deployments/functions/"
                                                         + TEST_FUNCTION_ID
                                                         + "/versions/" + TEST_VERSION_ID_1)
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("authDeploymentForPublicFunctionArgs")
    void shouldListDeploymentForPublicFunction(
            Object tokenSupplier,
            List<GpuSpecificationDto> ignoredSpecs,
            HttpStatus expectedStatus,
            FunctionStatusEnum ignoredStatus) {
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
        var requestEntity = RequestEntity.get("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                      + "/deployments/functions/" + TEST_FUNCTION_ID
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

    Stream<Arguments> getAllFunctionDeploymentsArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             HttpStatus.OK,
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2))
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("getAllFunctionDeploymentsArgs")
    void shouldGetAllFunctionDeploymentsWithPrivateAndPublicFunctions(
            Object tokenSupplier,
            HttpStatus expectedStatus,
            List<UUID> expectedFunctions) {
        // Create private function and deployment
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);
        testDeploymentService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // Create public function and deployment
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME_2,
                                                       FunctionStatus.DEPLOYING);
        testDeploymentService.createDeploymentEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // Make TEST_FUNCTION_ID_2 public by associating wildcard account.
        var authorizedParties = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID_2,
                                                    Optional.empty(), authorizedParties);

        var token = getToken(tokenSupplier);
        // Get function deployments
        var builder = RequestEntity
                .get("/v2/nvcf/accounts/" + TEST_NCA_ID
                             + "/deployments");
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListDeploymentsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var actualFunctionIds = responseBody.deployments().stream()
                .map(FunctionDeploymentDto::functionId)
                .toList();
        assertThat(actualFunctionIds).containsExactlyInAnyOrderElementsOf(expectedFunctions);

        for (var deployment : responseBody.deployments()) {
            assertThat(deployment.functionStatus()).isEqualTo(DEPLOYING);
            assertThat(deployment.ncaId()).isEqualTo(TEST_NCA_ID);
            assertThat(deployment.deploymentSpecifications()).hasSize(2);
        }
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("authDeploymentForPublicFunctionArgs")
    void shouldUpdateGpuSpecificationForPublicFunction(
            Object tokenSupplier,
            List<GpuSpecificationDto> ignoredSpecs,
            HttpStatus expectedStatus,
            FunctionStatusEnum ignoredStatus) {
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
                .patch("/v2/nvcf/accounts/" + TEST_NCA_ID
                               + "/deployments/" + TEST_DEPLOYMENT_ID
                               + "/gpu-specifications/" + TEST_GPU_SPEC_ID)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       UpdateGpuSpecificationResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.gpuSpecification()).isNotNull();
        assertThat(responseBody.gpuSpecification().minInstances()).isEqualTo(0);
        assertThat(responseBody.gpuSpecification().maxInstances()).isEqualTo(10);
    }
}
