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
package com.nvidia.nvct.rest.result;

import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_EVENTS;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_RESULTS;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.TestConstants.RESULT_ID_1;
import static com.nvidia.nvct.util.TestConstants.RESULT_ID_2;
import static com.nvidia.nvct.util.TestConstants.RESULT_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT_2;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_2;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.result.ResultsByTaskRepository;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.rest.result.dto.ListResultsResponse;
import com.nvidia.nvct.rest.result.dto.ResultDto;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvct.service.apikeys.ApiKeysService;
import com.nvidia.nvct.util.MockApiKeysServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.TestUtil;
import java.net.URI;
import java.net.URL;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.SneakyThrows;
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
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class ResultControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private TestResultService testResultService;

    @Autowired
    private AccountService accountService;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private ResultsByTaskRepository resultsByTaskRepository;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private ApiKeysService apiKeysService;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockNvcfServer.start(nvcfBaseUrl);
        tasksRepository.deleteAll();
        resultsByTaskRepository.deleteAll();
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockApiKeysServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @SneakyThrows
    @BeforeEach
    void beforeEach() {
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID.
        var task1 = TestUtil.createTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID,
                                              TEST_TASK_NAME_1, jsonMapper);
        var task2 = TestUtil.createTaskEntity(TEST_TASK_ID_2, TEST_NCA_ID,
                                              TEST_TASK_NAME_2, jsonMapper);

        // Save tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);

        var metadataTemplate = """
                {
                   "fine_tuned_model_checkpoint": "ckpt-step-%s",
                   "metrics": {
                        "full_valid_loss": %s,
                        "full_valid_mean_token_accuracy": %s
                    },
                    "fine_tuning_job_id": "ftjob-abc123",
                    "step_number": %s,
                    "percentComplete": %s
                }
                """;
        // Results for TEST_TASK_ID_1.
        var metadata1 = metadataTemplate.formatted(1000, 0.2, 0.3, 1000, 10);
        testResultService.populateResultForTask(RESULT_ID_1, TEST_NCA_ID, TEST_TASK_ID_1,
                                                (ObjectNode) jsonMapper.readTree(metadata1));

        var metadata2 = metadataTemplate.formatted(2000, 0.3, 0.4, 2000, 20);
        testResultService.populateResultForTask(RESULT_ID_2, TEST_NCA_ID, TEST_TASK_ID_1,
                                                (ObjectNode) jsonMapper.readTree(metadata2));

        var metadata3 = metadataTemplate.formatted(3000, 0.4, 0.5, 3000, 30);
        testResultService.populateResultForTask(RESULT_ID_3, TEST_NCA_ID, TEST_TASK_ID_1,
                                                (ObjectNode) jsonMapper.readTree(metadata3));

        // Results for TEST_TASK_ID_2 to make sure there is no cross contamination query
        var otherMetadata1 = metadataTemplate.formatted(3000, 0.4, 0.5, 3000, 30);
        testResultService.populateResultForTask(RESULT_ID_1, TEST_NCA_ID, TEST_TASK_ID_2,
                                                (ObjectNode) jsonMapper.readTree(otherMetadata1));
    }

    @AfterEach
    void reset() {
        MockApiKeysServer.resetToDefault();
        // use of MockApiKeysServer with different scopes causes the api-keys cache to dirty
        apiKeysService.invalidateCache();
        accountService.invalidateCache();
        tasksRepository.deleteAll();
        resultsByTaskRepository.deleteAll();
        testTaskService.clearAll();
    }

    Stream<Arguments> taskResultsArgs() {
        var jwtCases = Stream.of(
                // TEST_TASK_ID_1 belongs to TEST_NCA_ID.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(RESULT_ID_1, RESULT_ID_2, RESULT_ID_3),
                             HttpStatus.OK),
                // TEST_TASK_ID_2 belongs to TEST_NCA_ID.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_2,
                             Set.of(RESULT_ID_1),
                             HttpStatus.OK),
                // Task in different account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT_2,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.NOT_FOUND),
                // Non-existent client.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("non-existent-client",
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.NOT_FOUND),
                // Non-existent task.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_3,
                             Set.of(),
                             HttpStatus.NOT_FOUND),
                // Missing scopes.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.FORBIDDEN),
                // No token.
                Arguments.of(null,
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.UNAUTHORIZED)
        );

        var apiKeyCases = Stream.of(
                // api-key with list_results scope and account-tasks resource
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("account-tasks", "*")),
                                                               List.of("list_results"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(RESULT_ID_1, RESULT_ID_2, RESULT_ID_3),
                             HttpStatus.OK),
                // api-key with list_results scope and specific task resource (matching)
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_1.toString())),
                                                               List.of("list_results"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(RESULT_ID_1, RESULT_ID_2, RESULT_ID_3),
                             HttpStatus.OK),
                // api-key with list_results scope and specific task resource (not matching)
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_2.toString())),
                                                               List.of("list_results"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.FORBIDDEN),
                // api-key with scope but no resources
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(),
                                                               List.of("list_results"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.FORBIDDEN),
                // api-key with missing scope
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("account-tasks", "*")),
                                                               List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("taskResultsArgs")
    void shouldListResults(Object tokenSupplier,
                           String ncaId,
                           UUID taskId,
                           Set<UUID> resultIds,
                           HttpStatus expectedStatus) {
        var token = TestUtil.getToken(tokenSupplier);
        var requestEntity =
                RequestEntity.get(URI.create("/v1/nvct/tasks/" + taskId + "/results"))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListResultsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.cursor()).isNull();
        assertThat(responseBody.limit()).isNull();
        assertThat(responseBody.results()).hasSize(resultIds.size());
        for (var resultDto : responseBody.results()) {
            assertThat(resultDto.createdAt()).isNotNull();
            assertThat(resultDto.ncaId()).isEqualTo(ncaId);
            assertThat(resultDto.resultId()).isIn(resultIds);
            assertThat(resultDto.taskId()).isEqualTo(taskId);
            assertThat(resultDto.metadata()).isNotNull();
            assertThat(resultDto.metadata()).hasSize(5);
            assertThat(resultDto.metadata().get("step_number").asInt())
                    .isIn(Set.of(1000, 2000, 3000));
            assertThat(resultDto.metadata().get("percentComplete").asInt())
                    .isIn(Set.of(10, 20, 30));
            assertThat(resultDto.name()).isNotBlank();
        }
    }

    Stream<Arguments> taskResultsWithPaginationLimitArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             1,
                             Set.of(RESULT_ID_1, RESULT_ID_2, RESULT_ID_3)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             100,
                             Set.of(RESULT_ID_1, RESULT_ID_2, RESULT_ID_3))
                        );
    }

    @ParameterizedTest
    @MethodSource("taskResultsWithPaginationLimitArgs")
    void shouldListResultsWithPaginationLimit(String token,
                                              String ncaId,
                                              UUID taskId,
                                              int limit,
                                              Set<UUID> expectedResultIds) {
        var requestEntity =
                RequestEntity.get(URI.create("/v1/nvct/tasks/" + taskId + "/results?limit=" + limit))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListResultsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        if (limit < 3) {
            assertThat(responseBody.results()).hasSize(limit);
            assertThat(responseBody.cursor()).isNotNull();
            assertThat(responseBody.limit()).isEqualTo(limit);
        } else {
            assertThat(responseBody.results()).hasSize(3);
            assertThat(responseBody.cursor()).isNull();
            assertThat(responseBody.limit()).isNull();
        }

        verifyReturnedResultsIntegrity(expectedResultIds, responseBody.results(), ncaId, taskId);
    }

    void verifyReturnedResultsIntegrity(
            Set<UUID> expectedResults, List<ResultDto> results, String ncaId, UUID taskId) {
        for (var resultDto : results) {
            assertThat(resultDto.createdAt()).isNotNull();
            assertThat(resultDto.ncaId()).isEqualTo(ncaId);
            assertThat(resultDto.resultId()).isIn(expectedResults);
            assertThat(resultDto.taskId()).isEqualTo(taskId);
            assertThat(resultDto.metadata()).isNotNull();
            assertThat(resultDto.metadata()).hasSize(5);
            assertThat(resultDto.metadata().get("step_number").asInt())
                    .isIn(Set.of(1000, 2000, 3000));
            assertThat(resultDto.name()).isNotBlank();
        }
    }

    @Test
    void shouldListResultsWithPaginationCursor() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LIST_EVENTS,
                                                              SCOPE_LIST_TASKS,
                                                              SCOPE_LIST_RESULTS),
                                                    100);
        var expectedResults = Set.of(RESULT_ID_1, RESULT_ID_2, RESULT_ID_3);

        // get the first 2 out of 3 results first
        var url = "/v1/nvct/tasks/" + TEST_TASK_ID_1 + "/results?limit=2";
        var requestEntity =
                RequestEntity.get(URI.create(url))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListResultsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var currentCursor = responseBody.cursor();
        assertThat(currentCursor).isNotNull();
        var firstPageResult = responseBody.results();
        assertThat(firstPageResult).hasSize(2);
        assertThat(responseBody.limit()).isEqualTo(2);

        // using the cursor from previous response, continue to listing the rest of the results
        url = "/v1/nvct/tasks/" + TEST_TASK_ID_1 + "/results?limit=2&cursor=" + currentCursor;
        requestEntity =
                RequestEntity.get(URI.create(url))
                        .header("Authorization", "Bearer " + token)
                        .build();
        responseEntity =
                testRestTemplate.exchange(requestEntity, ListResultsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // make sure the last result is correct
        responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        currentCursor = responseBody.cursor();
        assertThat(responseBody.limit()).isNull();
        assertThat(currentCursor).isNull();
        var secondPageResult = responseBody.results();
        assertThat(secondPageResult).hasSize(1);

        // make sure all 3 results have the right metadata
        verifyReturnedResultsIntegrity(expectedResults,
                                       Stream.concat(firstPageResult.stream(),
                                                     secondPageResult.stream()).toList(),
                                       TEST_NCA_ID,
                                       TEST_TASK_ID_1);
    }

}
