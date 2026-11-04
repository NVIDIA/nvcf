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
package com.nvidia.nvct.rest.task;

import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.QUEUED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.RUNNING;
import static com.nvidia.nvct.util.NvctConstants.ADMIN_SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_MODELS;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_RESOURCES;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_4;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_5;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_3;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.fail;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.task.dto.ListTasksResponse;
import com.nvidia.nvct.rest.task.dto.TaskDto;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.task.TaskMapperService;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.TestUtil;
import java.net.URI;
import java.net.URL;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
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
class XAccountFilterTaskTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private TaskMapperService taskMapperService;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockEssServer.start(essBaseUrl);
        MockNvcfServer.start(nvcfBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockEssServer.stop();
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testTaskService.clearAll();
        MockEssServer.clearSecrets();
    }

    Stream<Arguments> listTasksWithPaginationLimitArgs() {
        return Stream.of(
                // List tasks in TEST_NCA_ID account with 1 limit
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_TASKS),
                                                             100),
                             TEST_NCA_ID,
                             List.of(TEST_TASK_ID_2),
                             1,
                             HttpStatus.OK),

                // List tasks in TEST_NCA_ID account with 100 limit
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_TASKS),
                                                             100),
                             TEST_NCA_ID,
                             List.of(TEST_TASK_ID_1, TEST_TASK_ID_2),
                             100,
                             HttpStatus.OK));

    }

    @ParameterizedTest
    @MethodSource("listTasksWithPaginationLimitArgs")
    void shouldListTasksWithPaginationLimit(
            String token, String ncaId, List<UUID> expectedTasks, int limit, HttpStatus expectedStatus) {
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID.
        var task1 = TestUtil.createTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                              jsonMapper);
        var task2 = TestUtil.createTaskEntity(TEST_TASK_ID_2, TEST_NCA_ID, TEST_TASK_NAME_2,
                                              jsonMapper);

        // Save tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);

        var requestEntity =
                RequestEntity.get(URI.create("/v1/nvct/accounts/" + ncaId + "/tasks?limit=" + limit))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListTasksResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        if (limit == 1) {
            assertThat(responseBody.cursor()).isNotNull();
            assertThat(responseBody.tasks()).hasSize(1);
            assertThat(responseBody.limit()).isEqualTo(limit);
        } else if (limit > 2) {
            assertThat(responseBody.cursor()).isNull();
            assertThat(responseBody.limit()).isNull();
            assertThat(responseBody.tasks()).hasSize(2);
        }
        var tasks = responseBody.tasks().stream()
                .collect(Collectors.toMap(TaskDto::id, Function.identity()));
        verifyReturnedTasksIntegrity(expectedTasks, QUEUED, tasks);
    }

    @Test
    void shouldListTasksWithPaginationCursor() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_LIST_TASKS),
                                                    100);
        var expectedTasks = List.of(TEST_TASK_ID_1, TEST_TASK_ID_2, TEST_TASK_ID_3);
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID
        var task1 = TestUtil.createTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                              jsonMapper);
        var task2 = TestUtil.createTaskEntity(TEST_TASK_ID_2, TEST_NCA_ID, TEST_TASK_NAME_2,
                                              jsonMapper);
        var task3 = TestUtil.createTaskEntity(TEST_TASK_ID_3, TEST_NCA_ID, TEST_TASK_NAME_3,
                                              jsonMapper);
        // Create Tasks in TEST_NCA_ID_2 account to make sure there is no cross contamination query
        var otherTask1 = TestUtil.createTaskEntity(TEST_TASK_ID_4, TEST_NCA_ID_2, TEST_TASK_NAME_1,
                                                   jsonMapper);
        var otherTask2 = TestUtil.createTaskEntity(TEST_TASK_ID_5, TEST_NCA_ID_2, TEST_TASK_NAME_2,
                                                   jsonMapper);

        // Save tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);
        tasksRepository.save(task3);
        tasksRepository.save(otherTask1);
        tasksRepository.save(otherTask2);

        // get the first 2 out of 3 tasks first
        var url = "/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks?limit=2";
        var requestEntity =
                RequestEntity.get(URI.create(url))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListTasksResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var currentCursor = responseBody.cursor();
        assertThat(currentCursor).isNotNull();
        var firstPageTasks = responseBody.tasks().stream()
                .collect(Collectors.toMap(TaskDto::id, Function.identity()));
        assertThat(firstPageTasks).hasSize(2);
        assertThat(responseBody.limit()).isEqualTo(2);

        // using the cursor from previous response, continue to listing the rest of the tasks
        url = "/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks?limit=2&cursor=" + currentCursor;
        requestEntity =
                RequestEntity.get(URI.create(url))
                        .header("Authorization", "Bearer " + token)
                        .build();
        responseEntity =
                testRestTemplate.exchange(requestEntity, ListTasksResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // make sure the last task is correct
        responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        currentCursor = responseBody.cursor();
        assertThat(currentCursor).isNull();
        assertThat(responseBody.limit()).isNull();
        var secondPageTask = responseBody.tasks().stream()
                .collect(Collectors.toMap(TaskDto::id, Function.identity()));
        assertThat(secondPageTask).hasSize(1);

        // make sure all 3 tasks have the right metadata
        var tasks = new HashMap<UUID, TaskDto>();
        tasks.putAll(firstPageTasks);
        tasks.putAll(secondPageTask);
        verifyReturnedTasksIntegrity(expectedTasks, QUEUED, tasks);
    }

    @Test
    void shouldListTasksWithStatus() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_LIST_TASKS),
                                                    100);
        var expectedTasks = List.of(TEST_TASK_ID_2, TEST_TASK_ID_3);
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID
        var task1 = TestUtil.createContainerBasedTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1, QUEUED,
                                                            jsonMapper);
        var task2 = TestUtil.createContainerBasedTaskEntity(TEST_TASK_ID_2, TEST_NCA_ID, TEST_TASK_NAME_2, RUNNING,
                                                            jsonMapper);
        var task3 = TestUtil.createContainerBasedTaskEntity(TEST_TASK_ID_3, TEST_NCA_ID, TEST_TASK_NAME_3, RUNNING,
                                                            jsonMapper);
        // Create Tasks in TEST_NCA_ID_2 account to make sure there is no cross contamination query
        var otherTask1 = TestUtil.createTaskEntity(TEST_TASK_ID_4, TEST_NCA_ID_2, TEST_TASK_NAME_1,
                                                   jsonMapper);
        var otherTask2 = TestUtil.createTaskEntity(TEST_TASK_ID_5, TEST_NCA_ID_2, TEST_TASK_NAME_2,
                                                   jsonMapper);

        // Save tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);
        tasksRepository.save(task3);
        tasksRepository.save(otherTask1);
        tasksRepository.save(otherTask2);

        // get all the tasks with RUNNING status
        var url = "/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks?status=RUNNING";
        var requestEntity =
                RequestEntity.get(URI.create(url))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListTasksResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var tasks = responseBody.tasks().stream()
                .collect(Collectors.toMap(TaskDto::id, Function.identity()));
        assertThat(tasks).hasSize(2);
        verifyReturnedTasksIntegrity(expectedTasks, RUNNING, tasks);
    }

    void verifyReturnedTasksIntegrity(List<UUID> expectedTasks, TaskStatus expectedStatus, Map<UUID, TaskDto> tasks) {
        for (UUID expectedTaskId : expectedTasks) {
            var taskDto = tasks.get(expectedTaskId);
            assertThat(taskDto).isNotNull();
            assertThat(taskDto.createdAt()).isNotNull();
            assertThat(taskDto.status()).isEqualTo(TaskStatusEnum.fromText(expectedStatus.toString()));
            assertThat(taskDto.containerImage()).isNotNull();
            assertThat(taskDto.containerArgs()).isNotBlank();
            assertThat(taskDto.containerEnvironment()).hasSize(3);
            if (taskDto.id().equals(TEST_TASK_ID_1)) {
                assertThat(taskDto.name()).isEqualTo(TEST_TASK_NAME_1);
            } else if (taskDto.id().equals(TEST_TASK_ID_2)) {
                assertThat(taskDto.name()).isEqualTo(TEST_TASK_NAME_2);
            } else if (taskDto.id().equals(TEST_TASK_ID_3)) {
                assertThat(taskDto.name()).isEqualTo(TEST_TASK_NAME_3);
            } else {
                fail("unknown function returned");
            }
            assertThat(taskDto.description()).isNotEmpty();
            assertThat(taskDto.tags()).isNotEmpty();
            assertThat(taskDto.gpuSpecification()).isNotNull();
            assertThat(taskDto.models())
                    .isEqualTo(taskMapperService.getModelDtos(TEST_MODELS).get());
            assertThat(taskDto.resources())
                    .isEqualTo(taskMapperService.getResourceDtos(TEST_RESOURCES).get());
        }
    }

}
