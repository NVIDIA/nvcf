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

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_UPDATE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import com.nvidia.nvcf.util.MockRevalServer;
import java.net.URI;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
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
class FunctionsWithLlmModelsTest {

    private static final String TEST_LLM_MODEL_NAME = "meta/llama-3.1-8b-instruct";

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestQueueService testQueueService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private FunctionMapperService functionMapperService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.reval.base-url}")
    private URI revalBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    private MockRevalServer mockRevalServer;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        mockRevalServer = new MockRevalServer(revalBaseUrl);
        mockRevalServer.start();
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, JsonMapper.builder().build());
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        MockEssServer.start(essBaseUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();
        mockRevalServer.stop();
        MockApiKeysServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockEssServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testQueueService.clearQueues();
        resetToDefault();
    }

    @Test
    void shouldCreateUpdateAndDeployLlmFunctionWithModels() {
        var createToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createRequest = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME + "-" + Instant.now().toEpochMilli())
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel("1-M", "round-robin")))
                .build();

        var createEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createToken)
                .body(createRequest);
        var createResponse = testRestTemplate.exchange(createEntity, CreateFunctionResponse.class);

        assertThat(createResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(createResponse.getBody()).isNotNull();
        var createdFunction = createResponse.getBody().function();
        assertThat(createdFunction.functionType()).isEqualTo(FunctionTypeEnum.LLM);
        assertThat(createdFunction.models()).hasSize(1);
        assertThat(createdFunction.models().getFirst().getLlmConfig()).isNotNull();
        assertThat(createdFunction.models().getFirst().getLlmConfig().getTokenRateLimit()).isEqualTo("1-M");
        assertThat(createdFunction.models().getFirst().getLlmConfig().getRoutingMethod()).isEqualTo("round-robin");

        var functionId = createdFunction.id();
        var versionId = createdFunction.versionId();

        var updateToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_UPDATE_FUNCTION), 100);
        var updateRequest = UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName(TEST_LLM_MODEL_NAME)
                        .llmConfig(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                                .tokenRateLimit("5-S")
                                .routingMethod("pulsar")
                                .build())
                        .build()))
                .build();

        var updateEntity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + functionId
                                                        + "/versions/" + versionId))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + updateToken)
                .body(updateRequest);
        var updateResponse = testRestTemplate.exchange(updateEntity, FunctionResponse.class);

        assertThat(updateResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(updateResponse.getBody()).isNotNull();
        var updatedModel = updateResponse.getBody().function().models().getFirst();
        assertThat(updatedModel.getLlmConfig()).isNotNull();
        assertThat(updatedModel.getLlmConfig().getTokenRateLimit()).isEqualTo("5-S");
        assertThat(updatedModel.getLlmConfig().getRoutingMethod()).isEqualTo("pulsar");

        var storedFunction = functionLookupService.lookupUsingVersionIdOrThrow(versionId);
        assertThat(storedFunction.getFunctionType()).isEqualTo(FunctionType.LLM);
        var storedModel = functionMapperService.toFunctionModels(storedFunction.getModelSpecs()).getFirst();
        assertThat(storedModel.getLlmConfig()).isNotNull();
        assertThat(storedModel.getLlmConfig().getTokenRateLimit()).isEqualTo("5-S");
        assertThat(storedModel.getLlmConfig().getRoutingMethod()).isEqualTo("pulsar");

        var deployToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_DEPLOY_FUNCTION), 100);
        var deployRequest = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(List.of(
                        GpuSpecificationDto.builder()
                                .gpu(T10)
                                .instanceType(T10_INSTANCE_TYPE)
                                .maxInstances(5)
                                .minInstances(5)
                                .maxRequestConcurrency(10)
                                .build(),
                        GpuSpecificationDto.builder()
                                .gpu(L40G)
                                .instanceType(L40G_INSTANCE_TYPE)
                                .maxInstances(5)
                                .minInstances(2)
                                .maxRequestConcurrency(10)
                                .build()))
                .build();

        var deployEntity = RequestEntity.post(URI.create("/v2/nvcf/deployments/functions/" + functionId
                                                         + "/versions/" + versionId))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + deployToken)
                .body(deployRequest);
        var deployResponse = testRestTemplate.exchange(deployEntity, DeploymentResponse.class);

        assertThat(deployResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(MockIcmsServer.getMockIcmsServer().getAllServeEvents()).isNotEmpty();
    }

    @Test
    void createVersionShouldRejectMismatchedUrisAcrossVersions() {
        var functionName = TEST_FUNCTION_NAME + "-uris-" + Instant.now().toEpochMilli();
        var functionId = createInitialLlmFunction(functionName, "1-M", "round-robin").id();

        var createVersionToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                                 List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createVersionRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel("2-M", "round-robin",
                                         List.of("/v1/chat/completions"),
                                         "meta-llama-tokenizer")))
                .build();

        var createVersionEntity = RequestEntity.post(URI.create(
                        "/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createVersionToken)
                .body(createVersionRequest);
        var response = testRestTemplate.exchange(createVersionEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("llmConfig.uris");
    }

    @Test
    void createVersionShouldRejectMismatchedTokenizerAcrossVersions() {
        var functionName = TEST_FUNCTION_NAME + "-tok-" + Instant.now().toEpochMilli();
        var functionId = createInitialLlmFunction(functionName, "1-M", "round-robin").id();

        var createVersionToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                                 List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createVersionRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel("2-M", "round-robin",
                                         List.of("/v1/chat/completions", "/v1/responses"),
                                         "different-tokenizer")))
                .build();

        var createVersionEntity = RequestEntity.post(URI.create(
                        "/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createVersionToken)
                .body(createVersionRequest);
        var response = testRestTemplate.exchange(createVersionEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("llmConfig.tokenizer");
    }

    @Test
    void createVersionShouldPropagateTokenRateLimitAndRoutingMethodToExistingVersions() {
        var functionName = TEST_FUNCTION_NAME + "-propcreate-" + Instant.now().toEpochMilli();
        var firstVersion = createInitialLlmFunction(functionName, "1-M", "round-robin");
        var functionId = firstVersion.id();
        var firstVersionId = firstVersion.versionId();

        var createVersionToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                                 List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createVersionRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel("7-M", "pulsar")))
                .build();

        var createVersionEntity = RequestEntity.post(URI.create(
                        "/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createVersionToken)
                .body(createVersionRequest);
        var createVersionResponse = testRestTemplate.exchange(createVersionEntity,
                                                              CreateFunctionResponse.class);

        assertThat(createVersionResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(createVersionResponse.getBody()).isNotNull();
        var newVersionDto = createVersionResponse.getBody().function();
        assertThat(newVersionDto.models().getFirst().getLlmConfig().getTokenRateLimit())
                .isEqualTo("7-M");
        assertThat(newVersionDto.models().getFirst().getLlmConfig().getRoutingMethod())
                .isEqualTo("pulsar");

        assertLlmConfigPersisted(firstVersionId, "7-M", "pulsar");
        assertLlmConfigPersisted(newVersionDto.versionId(), "7-M", "pulsar");
    }

    @Test
    void updateShouldPropagateTokenRateLimitAndRoutingMethodAcrossAllVersions() {
        var functionName = TEST_FUNCTION_NAME + "-propupdate-" + Instant.now().toEpochMilli();
        var firstVersion = createInitialLlmFunction(functionName, "1-M", "round-robin");
        var functionId = firstVersion.id();
        var firstVersionId = firstVersion.versionId();

        var secondVersionId = createAdditionalLlmFunctionVersion(functionId, functionName,
                                                                 "1-M", "round-robin").versionId();

        var updateToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_UPDATE_FUNCTION), 100);
        var updateRequest = UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName(TEST_LLM_MODEL_NAME)
                        .llmConfig(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                                .tokenRateLimit("9-M")
                                .routingMethod("pulsar")
                                .build())
                        .build()))
                .build();

        var updateEntity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + functionId
                                                        + "/versions/" + firstVersionId))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + updateToken)
                .body(updateRequest);
        var updateResponse = testRestTemplate.exchange(updateEntity, FunctionResponse.class);

        assertThat(updateResponse.getStatusCode()).isEqualTo(HttpStatus.OK);

        assertLlmConfigPersisted(firstVersionId, "9-M", "pulsar");
        assertLlmConfigPersisted(secondVersionId, "9-M", "pulsar");
    }

    @Test
    void createVersionShouldRejectWhenExistingSiblingIsNotLlm() {
        var functionName = TEST_FUNCTION_NAME + "-mixed-create-" + Instant.now().toEpochMilli();

        var createToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createDefaultRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .build();
        var createDefaultEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createToken)
                .body(createDefaultRequest);
        var createDefaultResponse = testRestTemplate.exchange(createDefaultEntity,
                                                              CreateFunctionResponse.class);
        assertThat(createDefaultResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(createDefaultResponse.getBody()).isNotNull();
        var defaultFunction = createDefaultResponse.getBody().function();
        assertThat(defaultFunction.functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);
        var functionId = defaultFunction.id();

        var createLlmVersionRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel("1-M", "round-robin")))
                .build();
        var createLlmVersionEntity = RequestEntity.post(URI.create(
                        "/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createToken)
                .body(createLlmVersionRequest);
        var response = testRestTemplate.exchange(createLlmVersionEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("functionType");
    }

    @Test
    void createVersionShouldRejectNonLlmVersionOfLlmFunction() {
        var functionName = TEST_FUNCTION_NAME + "-nonllm-version-" + Instant.now().toEpochMilli();
        var functionId = createInitialLlmFunction(functionName, "1-M", "round-robin").id();

        var createVersionToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                                 List.of(SCOPE_REGISTER_FUNCTION), 100);
        // functionType omitted defaults to DEFAULT, which must be rejected against an LLM family.
        var createVersionRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .build();
        var createVersionEntity = RequestEntity.post(URI.create(
                        "/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createVersionToken)
                .body(createVersionRequest);
        var response = testRestTemplate.exchange(createVersionEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("functionType");
    }

    @Test
    void createVersionShouldAcceptDefaultVersionWhenSiblingTypeIsNull() {
        var functionName = TEST_FUNCTION_NAME + "-legacy-null-type-" + Instant.now().toEpochMilli();

        // Create a normal (DEFAULT) function through the API.
        var createToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .build();
        var createResponse = testRestTemplate.exchange(
                RequestEntity.post(URI.create("/v2/nvcf/functions"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + createToken)
                        .body(createRequest),
                CreateFunctionResponse.class);
        assertThat(createResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(createResponse.getBody()).isNotNull();
        var created = createResponse.getBody().function();

        // Simulate a legacy row whose function_type was never set (null), which is semantically
        // DEFAULT. Seed it directly since the API always writes a concrete type.
        var seed = functionLookupService.lookupUsingVersionIdOrThrow(created.versionId());
        var legacyNullSibling = seed.toBuilder()
                .functionVersionId(UUID.randomUUID())
                .functionType(null)
                .build();
        functionsRepository.save(legacyNullSibling);

        // A new version with functionType omitted defaults to DEFAULT and must be accepted, not
        // rejected against the null (DEFAULT) sibling.
        var createVersionToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                                 List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createVersionRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .build();
        var response = testRestTemplate.exchange(
                RequestEntity.post(URI.create("/v2/nvcf/functions/" + created.id() + "/versions"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + createVersionToken)
                        .body(createVersionRequest),
                String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    void updateShouldRejectWhenSiblingIsNotLlm() {
        var functionName = TEST_FUNCTION_NAME + "-mixed-update-" + Instant.now().toEpochMilli();
        var firstVersion = createInitialLlmFunction(functionName, "1-M", "round-robin");
        var functionId = firstVersion.id();
        var llmVersionId = firstVersion.versionId();

        // Seed a non-LLM sibling directly in Cassandra so we land in a mixed-type state that
        // the API guards would otherwise prevent us from reaching.
        var llmEntity = functionLookupService.lookupUsingVersionIdOrThrow(llmVersionId);
        var defaultSibling = llmEntity.toBuilder()
                .functionVersionId(UUID.randomUUID())
                .functionType(FunctionType.DEFAULT)
                .modelSpecs(null)
                .build();
        functionsRepository.save(defaultSibling);

        var updateToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_UPDATE_FUNCTION), 100);
        var updateRequest = UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName(TEST_LLM_MODEL_NAME)
                        .llmConfig(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                                .tokenRateLimit("5-S")
                                .routingMethod("pulsar")
                                .build())
                        .build()))
                .build();
        var updateEntity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + functionId
                                                        + "/versions/" + llmVersionId))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + updateToken)
                .body(updateRequest);
        var response = testRestTemplate.exchange(updateEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("functionType");
    }

    private FunctionDto createInitialLlmFunction(
            String functionName, String tokenRateLimit, String routingMethod) {
        var createToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel(tokenRateLimit, routingMethod)))
                .build();
        var createEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createToken)
                .body(createRequest);
        var response = testRestTemplate.exchange(createEntity, CreateFunctionResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        return response.getBody().function();
    }

    private FunctionDto createAdditionalLlmFunctionVersion(
            UUID functionId, String functionName,
            String tokenRateLimit, String routingMethod) {
        var createToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createRequest = CreateFunctionRequest.builder()
                .name(functionName)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel(tokenRateLimit, routingMethod)))
                .build();
        var createEntity = RequestEntity.post(URI.create(
                        "/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createToken)
                .body(createRequest);
        var response = testRestTemplate.exchange(createEntity, CreateFunctionResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        return response.getBody().function();
    }

    @Test
    void shouldRejectCreateWithInvalidRoutingMethod() {
        var createToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createRequest = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME + "-" + Instant.now().toEpochMilli())
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel("1-M", "not-a-method")))
                .build();
        var createEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createToken)
                .body(createRequest);

        var response = testRestTemplate.exchange(createEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("llmConfig.routingMethod");
    }

    @Test
    void shouldRejectCreateWithInvalidTokenRateLimit() {
        var createToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createRequest = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME + "-" + Instant.now().toEpochMilli())
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel("20-X", "round-robin")))
                .build();
        var createEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + createToken)
                .body(createRequest);

        var response = testRestTemplate.exchange(createEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("llmConfig.tokenRateLimit");
    }

    @Test
    void shouldRejectUpdateWithInvalidRoutingMethod() {
        var functionName = TEST_FUNCTION_NAME + "-" + Instant.now().toEpochMilli();
        var function = createInitialLlmFunction(functionName, "1-M", "round-robin");

        var updateToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_UPDATE_FUNCTION), 100);
        var updateRequest = UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName(TEST_LLM_MODEL_NAME)
                        .llmConfig(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                                .routingMethod("not-a-method")
                                .build())
                        .build()))
                .build();
        var updateEntity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + function.id()
                                                        + "/versions/" + function.versionId()))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + updateToken)
                .body(updateRequest);

        var response = testRestTemplate.exchange(updateEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("llmConfig.routingMethod");
    }

    @Test
    void shouldRejectUpdateWithInvalidTokenRateLimit() {
        var functionName = TEST_FUNCTION_NAME + "-" + Instant.now().toEpochMilli();
        var function = createInitialLlmFunction(functionName, "1-M", "round-robin");

        var updateToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_UPDATE_FUNCTION), 100);
        var updateRequest = UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName(TEST_LLM_MODEL_NAME)
                        .llmConfig(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                                .tokenRateLimit("20-X")
                                .build())
                        .build()))
                .build();
        var updateEntity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + function.id()
                                                        + "/versions/" + function.versionId()))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + updateToken)
                .body(updateRequest);

        var response = testRestTemplate.exchange(updateEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("llmConfig.tokenRateLimit");
    }

    private void assertLlmConfigPersisted(UUID versionId, String tokenRateLimit,
                                          String routingMethod) {
        var stored = functionLookupService.lookupUsingVersionIdOrThrow(versionId);
        var storedModel = functionMapperService.toFunctionModels(stored.getModelSpecs())
                .getFirst();
        assertThat(storedModel.getLlmConfig()).isNotNull();
        assertThat(storedModel.getLlmConfig().getTokenRateLimit()).isEqualTo(tokenRateLimit);
        assertThat(storedModel.getLlmConfig().getRoutingMethod()).isEqualTo(routingMethod);
    }

    private static FunctionModelDto llmModel(String tokenRateLimit, String routingMethod) {
        return llmModel(tokenRateLimit, routingMethod,
                        List.of("/v1/chat/completions", "/v1/responses"),
                        "meta-llama-tokenizer");
    }

    private static FunctionModelDto llmModel(String tokenRateLimit, String routingMethod,
                                             List<String> uris, String tokenizer) {
        return FunctionModelDto.builder()
                .name(TEST_LLM_MODEL_NAME)
                .version("1.0")
                .uri(URI.create(TEST_MODEL_URL_1))
                .llmConfig(FunctionModelDto.LlmConfigDto.builder()
                        .uris(uris)
                        .tokenRateLimit(tokenRateLimit)
                        .tokenizer(tokenizer)
                        .routingMethod(routingMethod)
                        .build())
                .build();
    }
}
