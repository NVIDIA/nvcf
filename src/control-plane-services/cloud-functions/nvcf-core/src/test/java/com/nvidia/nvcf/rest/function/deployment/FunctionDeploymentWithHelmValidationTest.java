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
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.INACTIVE;
import static com.nvidia.nvcf.rest.function.deployment.dto.ValidationPolicyNameEnum.DEFAULT;
import static com.nvidia.nvcf.rest.function.deployment.dto.ValidationPolicyNameEnum.UNRESTRICTED;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.buildHelmValidationPolicyDto;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.HelmValidationPolicyDto;
import com.nvidia.nvcf.rest.function.deployment.dto.HelmValidationPolicyDto.KubernetesType;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.HelmValidationPolicyMapperService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import com.nvidia.nvcf.util.MockRevalServer;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.http.ResponseEntity;
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
class FunctionDeploymentWithHelmValidationTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private HelmValidationPolicyMapperService helmValidationPolicyMapperService;

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

    @Value("${nvcf.reval.base-url}")
    private URI revalBaseUrl;

    private MockRevalServer mockRevalServer;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        mockRevalServer = new MockRevalServer(revalBaseUrl);
        mockRevalServer.start();

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
        mockRevalServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        resetToDefault();
    }

    // -------------------------------------------------------------------------
    // Deploy with helmValidationPolicy → verify controller stores and returns it
    // -------------------------------------------------------------------------
    @Test
    void shouldDeployWithFullHelmValidationPolicy() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME, INACTIVE);

        var response = deploy(buildHelmValidationPolicyDto());

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        var deployment = response.getBody();
        assertThat(deployment).isNotNull();
        var specs = deployment.deployment().deploymentSpecifications();
        assertThat(specs).isNotEmpty();
        specs.forEach(spec -> {
            var returned = spec.helmValidationPolicy();
            assertThat(returned).isNotNull();
            assertThat(returned.name()).isEqualTo(UNRESTRICTED);
            assertThat(returned.extraKubernetesTypes()).hasSize(2);
            var deployment1 = returned.extraKubernetesTypes().get(0);
            assertThat(deployment1.group()).isEqualTo("apps");
            assertThat(deployment1.version()).isEqualTo("v1");
            assertThat(deployment1.kind()).isEqualTo("Deployment");
            var service = returned.extraKubernetesTypes().get(1);
            assertThat(service.group()).isEqualTo("infra");
            assertThat(service.version()).isEqualTo("v1");
            assertThat(service.kind()).isEqualTo("Service");
        });
    }

    @Test
    void shouldDeployWithHelmValidationPolicyNameOnly() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME, INACTIVE);

        var policy = HelmValidationPolicyDto.builder()
                .name(DEFAULT)
                .build();

        var response = deploy(policy);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        var deployment = response.getBody();
        assertThat(deployment).isNotNull();
        deployment.deployment().deploymentSpecifications().forEach(spec -> {
            var returned = spec.helmValidationPolicy();
            assertThat(returned).isNotNull();
            assertThat(returned.name()).isEqualTo(DEFAULT);
            assertThat(returned.extraKubernetesTypes()).isNull();
        });
    }

    @Test
    void shouldDeployWithoutExtraKubernetesTypesList() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME, INACTIVE);

        // extraKubernetesTypes not specified -- meaning it is null.
        var policy = HelmValidationPolicyDto.builder()
                .name(UNRESTRICTED)
                .build();

        var response = deploy(policy);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        var deployment = response.getBody();
        assertThat(deployment).isNotNull();
        deployment.deployment().deploymentSpecifications().forEach(spec -> {
            var returned = spec.helmValidationPolicy();
            assertThat(returned).isNotNull();
            assertThat(returned.name()).isEqualTo(UNRESTRICTED);
            assertThat(returned.extraKubernetesTypes()).isNull();
        });
    }

    @Test
    void shouldFailToDeployWithEmptyKubernetesTypes() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME, INACTIVE);

        var policy = HelmValidationPolicyDto.builder()
                .name(UNRESTRICTED)
                .extraKubernetesTypes(List.of())
                .build();

        var response = deploy(policy);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldDeployWithoutHelmValidationPolicy() {
        // Container-based function.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        var response = deploy(null);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        var deployment = response.getBody();
        assertThat(deployment).isNotNull();
        deployment.deployment().deploymentSpecifications()
                .forEach(spec -> assertThat(spec.helmValidationPolicy()).isNull());
    }

    /**
     * Verifies that when helmValidationPolicy is present but the name field is missing,
     * the deployment fail with 400 BAD_REQUEST. This is because GpuSpecificationDto does
     * annotate helmValidationPolicy with @Valid, so inner @NotNull constraints are
     * cascade-validated by the framework. The policy is serialized to JSON and stored as-is.
     */
    @Test
    void shouldRejectDeploymentRequestWhenHelmValidationPolicyNameIsNull() {
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var payload = """
                {
                  "deploymentSpecifications": [{
                    "gpu": "%s",
                    "backend": "%s",
                    "instanceType": "%s",
                    "maxInstances": 4,
                    "minInstances": 1,
                    "helmValidationPolicy": {
                      "extraKubernetesTypes": [
                        {"group": "x", "version": "1", "kind": "y"}
                      ]
                    }
                  }]
                }""".formatted(T10, GFN, T10_INSTANCE_TYPE);

        var requestEntity = RequestEntity
                .post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                              + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(payload.getBytes());

        // Nested @NotNull on HelmValidationPolicyDto.name is not cascade-validated because
        // GpuSpecificationDto.helmValidationPolicy is missing @Valid — request succeeds.
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    /**
     * Verifies that when helmValidationPolicy is present with valid the name field and
     * extraKubernetesTypes field, but extraKubernetesTypes is not properly populated,
     * the deployment fail with 400 BAD_REQUEST. This is because GpuSpecificationDto does
     * annotate helmValidationPolicy with @Valid, so inner @NotNull constraints are
     * cascade-validated by the framework. The policy is serialized to JSON and stored as-is.
     */
    @Test
    void shouldRejectDeploymentRequestWhenHelmValidationPolicyHasPartialK8sTypes() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME, INACTIVE);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var payload = """
                {
                  "deploymentSpecifications": [{
                    "gpu": "%s",
                    "backend": "%s",
                    "instanceType": "%s",
                    "maxInstances": 4,
                    "minInstances": 1,
                    "helmValidationPolicy": {
                      "name": "DEFAULT",
                      "extraKubernetesTypes": [
                        {"group": "group_01", "version": ""}
                      ]
                    }
                  }]
                }""".formatted(T10, GFN, T10_INSTANCE_TYPE);

        var requestEntity = RequestEntity
                .post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                              + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(payload.getBytes());

        // Nested @NotNull on HelmValidationPolicyDto.name is not cascade-validated because
        // GpuSpecificationDto.helmValidationPolicy is missing @Valid — request succeeds.
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldRejectDeploymentRequestWhenHelmValidationPolicyIsEmpty() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME, INACTIVE);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var payload = """
                {
                  "deploymentSpecifications": [{
                    "gpu": "%s",
                    "backend": "%s",
                    "instanceType": "%s",
                    "maxInstances": 4,
                    "minInstances": 1,
                    "helmValidationPolicy": {
                      "name": "DEFAULT",
                      "extraKubernetesTypes": []
                    }
                  }]
                }""".formatted(T10, GFN, T10_INSTANCE_TYPE);

        var requestEntity = RequestEntity
                .post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                              + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(payload.getBytes(StandardCharsets.UTF_8));

        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldDeployWhenHelmValidationPolicyIsNotSpecified() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME, INACTIVE);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var payload = """
                {
                  "deploymentSpecifications": [{
                    "gpu": "%s",
                    "backend": "%s",
                    "instanceType": "%s",
                    "maxInstances": 4,
                    "minInstances": 1,
                    "helmValidationPolicy": {
                      "name": "DEFAULT"
                    }
                  }]
                }""".formatted(T10, GFN, T10_INSTANCE_TYPE);

        var requestEntity = RequestEntity
                .post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                              + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(payload.getBytes(StandardCharsets.UTF_8));

        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    // -------------------------------------------------------------------------
    // Round-trip: verify helmValidationPolicy persists in DB and resolves back
    // -------------------------------------------------------------------------

    @Test
    void shouldPersistHelmValidationPolicyAndResolveFromDb() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME, INACTIVE);

        var policy = HelmValidationPolicyDto.builder()
                .name(UNRESTRICTED)
                .extraKubernetesTypes(List.of(
                        KubernetesType.builder().group("batch").version("v1").kind("Job").build()))
                .build();

        // POST: deploy → stored in DB
        var deployResponse = deploy(policy);
        assertThat(deployResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        var deploymentId = deployResponse.getBody().deployment().deploymentId();

        // GET: read back from DB via GET /v2/nvcf/deployments/{deploymentId}
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var getRequest = RequestEntity.get("/v2/nvcf/deployments/" + deploymentId)
                .header("Authorization", "Bearer " + token)
                .build();
        var getResponse = testRestTemplate.exchange(getRequest, DeploymentResponse.class);

        assertThat(getResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        var body = getResponse.getBody();
        assertThat(body).isNotNull();
        body.deployment().deploymentSpecifications().forEach(spec -> {
            var returned = spec.helmValidationPolicy();
            assertThat(returned).isNotNull();
            assertThat(returned.name()).isEqualTo(UNRESTRICTED);
            assertThat(returned.extraKubernetesTypes()).hasSize(1);
            assertThat(returned.extraKubernetesTypes().get(0).group()).isEqualTo("batch");
            assertThat(returned.extraKubernetesTypes().get(0).version()).isEqualTo("v1");
            assertThat(returned.extraKubernetesTypes().get(0).kind()).isEqualTo("Job");
        });
    }

    @Test
    void shouldResolveHelmValidationPolicyFromExistingDbEntity() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        var policy = HelmValidationPolicyDto.builder()
                .name(DEFAULT)
                .extraKubernetesTypes(List.of(
                        KubernetesType.builder().group("networking.k8s.io")
                                .version("v1").kind("Ingress").build()))
                .build();

        var helmPolicyJson = helmValidationPolicyMapperService.toHelmValidationPolicyJson(policy);

        var gpuSpec = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(T10)
                .instanceType(T10_INSTANCE_TYPE)
                .maxInstances(4).minInstances(1)
                .helmValidationPolicy(helmPolicyJson)
                .build();

        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID,
                                           Set.of(gpuSpec));

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var getRequest = RequestEntity.get("/v2/nvcf/deployments/" + TEST_DEPLOYMENT_ID)
                .header("Authorization", "Bearer " + token)
                .build();
        var getResponse = testRestTemplate.exchange(getRequest, DeploymentResponse.class);

        assertThat(getResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        var body = getResponse.getBody();
        assertThat(body).isNotNull();
        var specs = body.deployment().deploymentSpecifications();
        assertThat(specs).hasSize(1);
        var returned = specs.get(0).helmValidationPolicy();
        assertThat(returned).isNotNull();
        assertThat(returned.name()).isEqualTo(DEFAULT);
        assertThat(returned.extraKubernetesTypes()).hasSize(1);
        assertThat(returned.extraKubernetesTypes().get(0).group())
                .isEqualTo("networking.k8s.io");
        assertThat(returned.extraKubernetesTypes().get(0).version()).isEqualTo("v1");
        assertThat(returned.extraKubernetesTypes().get(0).kind()).isEqualTo("Ingress");
    }

    @Test
    void shouldReturnNullHelmValidationPolicyForExistingEntityWithoutPolicy() {
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        var gpuSpec = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(T10)
                .instanceType(T10_INSTANCE_TYPE)
                .maxInstances(4).minInstances(1)
                .build();

        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID,
                                           Set.of(gpuSpec));

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var getRequest = RequestEntity.get("/v2/nvcf/deployments/" + TEST_DEPLOYMENT_ID)
                .header("Authorization", "Bearer " + token)
                .build();
        var getResponse = testRestTemplate.exchange(getRequest, DeploymentResponse.class);

        assertThat(getResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        var body = getResponse.getBody();
        assertThat(body).isNotNull();
        body.deployment().deploymentSpecifications()
                .forEach(spec -> assertThat(spec.helmValidationPolicy()).isNull());
    }

    // -------------------------------------------------------------------------
    // Helper
    // -------------------------------------------------------------------------

    private ResponseEntity<DeploymentResponse> deploy(
            HelmValidationPolicyDto helmValidationPolicy) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var gpuSpec = GpuSpecificationDto.builder()
                .gpu(T10).backend(GFN).instanceType(T10_INSTANCE_TYPE)
                .maxInstances(4).minInstances(1)
                .helmValidationPolicy(helmValidationPolicy)
                .build();

        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(List.of(gpuSpec))
                .build();

        var requestEntity = RequestEntity
                .post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                              + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        return testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
    }
}
