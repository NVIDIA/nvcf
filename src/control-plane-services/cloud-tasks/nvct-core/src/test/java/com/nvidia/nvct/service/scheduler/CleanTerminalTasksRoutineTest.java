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
package com.nvidia.nvct.service.scheduler;

import static com.nvidia.nvct.persistence.task.entity.TaskStatus.COMPLETED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.QUEUED;
import static com.nvidia.nvct.util.MockIcmsServer.InstancesState.NO_CAPACITY;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.service.event.EventService;
import com.nvidia.nvct.service.result.ResultService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import java.net.URL;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class CleanTerminalTasksRoutineTest {

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private CleanTerminalTasksRoutine cleanTerminalTasksRoutine;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private EventService eventService;

    @Autowired
    private TaskService taskService;

    @Autowired
    private ResultService resultService;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockEssServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testTaskService.clearAll();
    }


    @Test
    @SneakyThrows
    void testUnder7DaysTerminalTasksCleanup() {
        var contexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                       .instanceState(NO_CAPACITY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts);

        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        // set the last heartbeat to now and status to completed
        taskService.updateTask(TEST_TASK_ID_1, Instant.now());
        taskService.updateTask(TEST_TASK_ID_1, COMPLETED);
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isNotNull();
        var results = resultService.fetchResults(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(results).isNotNull();

        cleanTerminalTasksRoutine.runUnchecked();

        // Verify tasksRepository did not change
        var updatedTask = taskService.fetchTask(TEST_TASK_ID_1);
        assertThat(updatedTask).isEqualTo(taskEntity.get());

        // Verify entry in the EventsByTaskRepository did not change
        var updatedEvents = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isEqualTo(updatedEvents);

        // Verify entry in the ResultsByTaskRepository did not change
        var updatedResults = resultService.fetchResults(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(updatedResults).isEqualTo(results);
    }

    @Test
    @SneakyThrows
    void testQueuedTasksCleanup() {
        var contexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                       .instanceState(NO_CAPACITY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts);

        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        // set the last heartbeat to more than 7 days ago and status to non-terminal such as queued
        taskService.updateTask(TEST_TASK_ID_1, Instant.now().minus(Duration.ofDays(8)));
        taskService.updateTask(TEST_TASK_ID_1, QUEUED);
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isNotNull();
        var results = resultService.fetchResults(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(results).isNotNull();

        cleanTerminalTasksRoutine.runUnchecked();

        // Verify tasksRepository did not change
        var updatedTask = taskService.fetchTask(TEST_TASK_ID_1);
        assertThat(updatedTask).isEqualTo(taskEntity.get());

        // Verify entry in the EventsByTaskRepository did not change
        var updatedEvents = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isEqualTo(updatedEvents);

        // Verify entry in the ResultsByTaskRepository did not change
        var updatedResults = resultService.fetchResults(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(updatedResults).isEqualTo(results);
    }

    @Test
    @SneakyThrows
    void testTerminalTasksCleanup() {
        var contexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                       .instanceState(NO_CAPACITY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts);

        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        // set the last heartbeat to more than 7 days ago and status to terminal such as completed
        taskService.updateTask(TEST_TASK_ID_1, Instant.now().minus(Duration.ofDays(8)));
        taskService.updateTask(TEST_TASK_ID_1, COMPLETED);
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isNotNull();
        var results = resultService.fetchResults(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(results).isNotNull();

        cleanTerminalTasksRoutine.runUnchecked();

        // Verify tasksRepository does not have the task anymore
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> taskService.fetchTask(TEST_TASK_ID_1));

        // Verify entry in the EventsByTaskRepository does not exist
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1));

        // Verify entry in the results does not exist
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> resultService.fetchResults(TEST_NCA_ID, TEST_TASK_ID_1));
    }
}
