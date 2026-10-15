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
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_UPDATE_FUNCTION;
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
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.LlmInvocationConfigDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.PriorityDto;
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
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
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
class FunctionWithLlmInvocationConfigPriorityTest {

    private static final String TEST_LLM_MODEL_NAME = "meta/llama-3.1-8b-instruct";
    private static final String OVERRIDE_NCA_ID = "nca-override";
    // u32 max enforced by PriorityDto.
    private static final long MAX_PRIORITY = 4294967295L;

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
    private JsonMapper jsonMapper;

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
        MockIcmsServer.start(9096, jsonMapper);
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
    void createWithPriorityReturnsAndPersistsIt() {
        var name = uniqueName("create");
        var priority = priorityConfig(7L, Map.of(OVERRIDE_NCA_ID, 3L));
        var created = createLlmFunction(name, priority);

        assertThat(created.llmInvocationConfig()).isNotNull();
        assertThat(created.llmInvocationConfig().priority().defaultPriority()).isEqualTo(7L);
        assertThat(created.llmInvocationConfig().priority().perAccountPriority())
                .containsEntry(OVERRIDE_NCA_ID, 3L);
        assertThat(storedPriority(created.versionId())).isEqualTo(priority);
    }

    @Test
    void updateReplacesAndPropagatesPriorityAcrossVersions() {
        var name = uniqueName("update-prop");
        var first = createLlmFunction(name, priorityConfig(7L, null));
        var functionId = first.id();
        var secondVersionId = addLlmVersion(functionId, name, null).versionId();

        var response = updateLlmInvocationConfig(functionId, first.versionId(), priorityConfig(2L, null));
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);

        // A present llmInvocationConfig replaces the target and propagates to every sibling version.
        assertThat(storedPriority(first.versionId())).isEqualTo(priorityConfig(2L, null));
        assertThat(storedPriority(secondVersionId)).isEqualTo(priorityConfig(2L, null));
    }

    @Test
    void emptyLlmInvocationConfigClearsPriority() {
        var name = uniqueName("clear");
        var created = createLlmFunction(name, priorityConfig(7L, null));

        var response = updateLlmInvocationConfig(created.versionId(), new LlmInvocationConfigDto(null),
                                       created.id());
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Plain CRUD (no empty-collapsing): an empty llmInvocationConfig overwrites the stored config with
        // an empty, no-priority value rather than clearing the column to null. It still resolves
        // to no effective priority.
        var stored = storedPriority(created.versionId());
        assertThat(stored).isNotNull();
        assertThat(stored.priority()).isNull();
    }

    @Test
    void versionCreateInheritsPriorityWhenOmitted() {
        var name = uniqueName("inherit");
        var first = createLlmFunction(name, priorityConfig(7L, Map.of(OVERRIDE_NCA_ID, 3L)));

        var newVersion = addLlmVersion(first.id(), name, null);

        // An omitted llmInvocationConfig on version-create inherits the function's existing value.
        assertThat(storedPriority(newVersion.versionId()))
                .isEqualTo(priorityConfig(7L, Map.of(OVERRIDE_NCA_ID, 3L)));
    }

    @Test
    void createNonLlmWithLlmInvocationConfigIsRejected() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var request = CreateFunctionRequest.builder()
                .name(uniqueName("non-llm"))
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .llmInvocationConfig(priorityConfig(7L, null))
                .build();
        var entity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);

        var response = testRestTemplate.exchange(entity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("llmInvocationConfig");
    }

    @Test
    void priorityWithOverridesButNoDefaultIsRejected() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var request = CreateFunctionRequest.builder()
                .name(uniqueName("no-default"))
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel()))
                .llmInvocationConfig(priorityConfig(null, Map.of(OVERRIDE_NCA_ID, 3L)))
                .build();
        var entity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);

        var response = testRestTemplate.exchange(entity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("defaultPriority");
    }

    @Test
    void getVersionReturnsPriority() {
        var name = uniqueName("get");
        var created = createLlmFunction(name, priorityConfig(3L, null));

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LIST_FUNCTIONS), 100);
        var entity = RequestEntity.get(URI.create("/v2/nvcf/functions/" + created.id()
                                                  + "/versions/" + created.versionId()))
                .header("Authorization", "Bearer " + token)
                .build();
        var response = testRestTemplate.exchange(entity, FunctionResponse.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        var fetched = response.getBody().function();
        assertThat(fetched.llmInvocationConfig()).isNotNull();
        assertThat(fetched.llmInvocationConfig().priority().defaultPriority()).isEqualTo(3L);
        assertThat(fetched.llmInvocationConfig().priority().perAccountPriority()).isNull();
    }

    @Test
    void versionCreateWithPriorityPropagatesToExistingVersions() {
        var name = uniqueName("version-prop");
        var first = createLlmFunction(name, priorityConfig(1L, null));

        var newVersion = addLlmVersion(first.id(), name,
                                       priorityConfig(9L, Map.of(OVERRIDE_NCA_ID, 2L)));

        // A supplied llmInvocationConfig on version-create replaces and propagates to every sibling version.
        assertThat(newVersion.llmInvocationConfig().priority().defaultPriority()).isEqualTo(9L);
        assertThat(storedPriority(first.versionId()))
                .isEqualTo(priorityConfig(9L, Map.of(OVERRIDE_NCA_ID, 2L)));
        assertThat(storedPriority(newVersion.versionId()))
                .isEqualTo(priorityConfig(9L, Map.of(OVERRIDE_NCA_ID, 2L)));
    }

    @Test
    void updateWithoutLlmInvocationConfigPreservesPriority() {
        var name = uniqueName("preserve");
        var created = createLlmFunction(name, priorityConfig(2L, null));

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_UPDATE_FUNCTION), 100);
        var request = UpdateFunctionRequest.builder().tags(Set.of("team-a")).build();
        var entity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + created.id()
                                                  + "/versions/" + created.versionId()))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);
        var response = testRestTemplate.exchange(entity, FunctionResponse.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        // An omitted llmInvocationConfig preserves the stored value.
        assertThat(response.getBody().function().llmInvocationConfig()).isNotNull();
        assertThat(response.getBody().function().llmInvocationConfig().priority().defaultPriority())
                .isEqualTo(2L);
        assertThat(storedPriority(created.versionId())).isEqualTo(priorityConfig(2L, null));
    }

    @Test
    void updateNonLlmWithLlmInvocationConfigIsRejected() {
        var registerToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                            List.of(SCOPE_REGISTER_FUNCTION), 100);
        var createRequest = CreateFunctionRequest.builder()
                .name(uniqueName("non-llm-update"))
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .build();
        var createEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + registerToken)
                .body(createRequest);
        var createResponse = testRestTemplate.exchange(createEntity, CreateFunctionResponse.class);
        assertThat(createResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(createResponse.getBody()).isNotNull();
        var defaultFunction = createResponse.getBody().function();
        assertThat(defaultFunction.functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);

        var updateToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_UPDATE_FUNCTION), 100);
        var updateRequest = UpdateFunctionRequest.builder()
                .llmInvocationConfig(priorityConfig(1L, null))
                .build();
        var updateEntity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + defaultFunction.id()
                                                        + "/versions/" + defaultFunction.versionId()))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + updateToken)
                .body(updateRequest);
        var response = testRestTemplate.exchange(updateEntity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("llmInvocationConfig");
    }

    @Test
    void createWithDefaultPriorityAboveMaxIsRejected() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var request = CreateFunctionRequest.builder()
                .name(uniqueName("over-max"))
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel()))
                .llmInvocationConfig(priorityConfig(MAX_PRIORITY + 1, null))
                .build();
        var entity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);

        var response = testRestTemplate.exchange(entity, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains("defaultPriority");
    }

    @Test
    void updateWithFractionalDefaultPriorityIsRejectedWithoutChangingPriority() {
        var created = createLlmFunction(uniqueName("fractional-default"), priorityConfig(7L, null));
        var updateToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_UPDATE_FUNCTION), 100);
        var updateEntity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + created.id()
                                                        + "/versions/" + created.versionId()))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + updateToken)
                .body("""
                        {
                          "llmInvocationConfig": {
                            "priority": {
                              "defaultPriority": 1.5
                            }
                          }
                        }
                        """);

        var updateResponse = testRestTemplate.exchange(updateEntity, String.class);

        assertThat(updateResponse.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var getToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                       List.of(SCOPE_LIST_FUNCTIONS), 100);
        var getEntity = RequestEntity.get(URI.create("/v2/nvcf/functions/" + created.id()
                                                     + "/versions/" + created.versionId()))
                .header("Authorization", "Bearer " + getToken)
                .build();
        var getResponse = testRestTemplate.exchange(getEntity, FunctionResponse.class);

        assertThat(getResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(getResponse.getBody()).isNotNull();
        assertThat(getResponse.getBody().function().llmInvocationConfig().priority().defaultPriority())
                .isEqualTo(7L);
    }

    @Test
    void updateWithFractionalPerAccountPriorityIsRejectedWithoutChangingPriority() {
        var created = createLlmFunction(
                uniqueName("fractional-per-account"),
                priorityConfig(7L, Map.of(OVERRIDE_NCA_ID, 3L)));
        var updateToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_UPDATE_FUNCTION), 100);
        var updateEntity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + created.id()
                                                        + "/versions/" + created.versionId()))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + updateToken)
                .body("""
                        {
                          "llmInvocationConfig": {
                            "priority": {
                              "defaultPriority": 7,
                              "perAccountPriority": {
                                "nca-override": 1.5
                              }
                            }
                          }
                        }
                        """);

        var updateResponse = testRestTemplate.exchange(updateEntity, String.class);

        assertThat(updateResponse.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var getToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                       List.of(SCOPE_LIST_FUNCTIONS), 100);
        var getEntity = RequestEntity.get(URI.create("/v2/nvcf/functions/" + created.id()
                                                     + "/versions/" + created.versionId()))
                .header("Authorization", "Bearer " + getToken)
                .build();
        var getResponse = testRestTemplate.exchange(getEntity, FunctionResponse.class);

        assertThat(getResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(getResponse.getBody()).isNotNull();
        assertThat(getResponse.getBody().function().llmInvocationConfig().priority())
                .isEqualTo(new PriorityDto(7L, Map.of(OVERRIDE_NCA_ID, 3L)));
    }

    @Test
    void updateWithModelUpdatesAndLlmInvocationConfigAppliesBothToAllVersions() {
        var name = uniqueName("update-both");
        var first = createLlmFunction(name, priorityConfig(2L, null));
        var functionId = first.id();
        var secondVersionId = addLlmVersion(functionId, name, null).versionId();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_UPDATE_FUNCTION), 100);
        var request = UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName(TEST_LLM_MODEL_NAME)
                        .llmConfig(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                                .tokenRateLimit("9-M")
                                .routingMethod("pulsar")
                                .build())
                        .build()))
                .llmInvocationConfig(priorityConfig(5L, Map.of(OVERRIDE_NCA_ID, 4L)))
                .build();
        var entity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + functionId
                                                  + "/versions/" + first.versionId()))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);
        var response = testRestTemplate.exchange(entity, FunctionResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);

        // One request carrying both a model-level override and a function-level priority applies
        // both to the target and every sibling version. The two mutations coalesce onto each
        // sibling's staged instance, so neither write clobbers the other.
        assertModelConfig(first.versionId(), "9-M", "pulsar");
        assertModelConfig(secondVersionId, "9-M", "pulsar");
        assertThat(storedPriority(first.versionId()))
                .isEqualTo(priorityConfig(5L, Map.of(OVERRIDE_NCA_ID, 4L)));
        assertThat(storedPriority(secondVersionId))
                .isEqualTo(priorityConfig(5L, Map.of(OVERRIDE_NCA_ID, 4L)));
    }

    @Test
    void versionCreateWithModelOverrideAndLlmInvocationConfigAppliesBothToAllVersions() {
        var name = uniqueName("version-both");
        var first = createLlmFunction(name, priorityConfig(1L, null));
        var functionId = first.id();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var request = CreateFunctionRequest.builder()
                .name(name)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel("7-M", "pulsar")))
                .llmInvocationConfig(priorityConfig(9L, Map.of(OVERRIDE_NCA_ID, 2L)))
                .build();
        var entity = RequestEntity.post(URI.create(
                        "/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);
        var response = testRestTemplate.exchange(entity, CreateFunctionResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        var newVersion = response.getBody().function();

        // A version-create carrying both a model-level override and a function-level priority
        // propagates both to the existing sibling and stores both on the new version.
        assertModelConfig(first.versionId(), "7-M", "pulsar");
        assertModelConfig(newVersion.versionId(), "7-M", "pulsar");
        assertThat(storedPriority(first.versionId()))
                .isEqualTo(priorityConfig(9L, Map.of(OVERRIDE_NCA_ID, 2L)));
        assertThat(storedPriority(newVersion.versionId()))
                .isEqualTo(priorityConfig(9L, Map.of(OVERRIDE_NCA_ID, 2L)));
    }

    private FunctionDto createLlmFunction(String name, LlmInvocationConfigDto llmConfig) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var request = CreateFunctionRequest.builder()
                .name(name)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel()))
                .llmInvocationConfig(llmConfig)
                .build();
        var entity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);
        var response = testRestTemplate.exchange(entity, CreateFunctionResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        return response.getBody().function();
    }

    private FunctionDto addLlmVersion(UUID functionId, String name, LlmInvocationConfigDto llmConfig) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);
        var request = CreateFunctionRequest.builder()
                .name(name)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(llmModel()))
                .llmInvocationConfig(llmConfig)
                .build();
        var entity = RequestEntity.post(URI.create(
                        "/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);
        var response = testRestTemplate.exchange(entity, CreateFunctionResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        return response.getBody().function();
    }

    private org.springframework.http.ResponseEntity<FunctionResponse> updateLlmInvocationConfig(
            UUID functionId, UUID versionId, LlmInvocationConfigDto llmConfig) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_UPDATE_FUNCTION), 100);
        var request = UpdateFunctionRequest.builder().llmInvocationConfig(llmConfig).build();
        var entity = RequestEntity.put(URI.create("/v2/nvcf/functions/" + functionId
                                                  + "/versions/" + versionId))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(request);
        return testRestTemplate.exchange(entity, FunctionResponse.class);
    }

    private org.springframework.http.ResponseEntity<FunctionResponse> updateLlmInvocationConfig(
            UUID versionId, LlmInvocationConfigDto llmConfig, UUID functionId) {
        return updateLlmInvocationConfig(functionId, versionId, llmConfig);
    }

    private LlmInvocationConfigDto storedPriority(UUID versionId) {
        var entity = functionLookupService.lookupUsingVersionIdOrThrow(versionId);
        return functionMapperService.toLlmInvocationConfigDto(entity.getLlmConfig());
    }

    private void assertModelConfig(UUID versionId, String tokenRateLimit, String routingMethod) {
        var entity = functionLookupService.lookupUsingVersionIdOrThrow(versionId);
        var model = functionMapperService.toFunctionModels(entity.getModelSpecs()).getFirst();
        assertThat(model.getLlmConfig()).isNotNull();
        assertThat(model.getLlmConfig().getTokenRateLimit()).isEqualTo(tokenRateLimit);
        assertThat(model.getLlmConfig().getRoutingMethod()).isEqualTo(routingMethod);
    }

    private static LlmInvocationConfigDto priorityConfig(Long defaultPriority,
                                                       Map<String, Long> perAccountPriority) {
        return new LlmInvocationConfigDto(new PriorityDto(defaultPriority, perAccountPriority));
    }

    private static String uniqueName(String suffix) {
        return TEST_FUNCTION_NAME + "-" + suffix + "-" + Instant.now().toEpochMilli();
    }

    private static FunctionModelDto llmModel() {
        return llmModel("1-M", "round-robin");
    }

    private static FunctionModelDto llmModel(String tokenRateLimit, String routingMethod) {
        return FunctionModelDto.builder()
                .name(TEST_LLM_MODEL_NAME)
                .version("1.0")
                .uri(URI.create(TEST_MODEL_URL_1))
                .llmConfig(FunctionModelDto.LlmConfigDto.builder()
                                   .uris(List.of("/v1/chat/completions", "/v1/responses"))
                                   .tokenRateLimit(tokenRateLimit)
                                   .tokenizer("meta-llama-tokenizer")
                                   .routingMethod(routingMethod)
                                   .build())
                .build();
    }
}
