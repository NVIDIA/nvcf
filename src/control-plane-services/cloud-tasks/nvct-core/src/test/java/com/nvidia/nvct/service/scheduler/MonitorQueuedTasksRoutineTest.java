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

import static com.nvidia.nvct.persistence.task.entity.TaskStatus.ERRORED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.EXCEEDED_MAX_QUEUED_DURATION;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.QUEUED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.RUNNING;
import static com.nvidia.nvct.util.MockIcmsServer.InstancesState.NO_CAPACITY;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.service.event.EventService;
import com.nvidia.nvct.service.task.TaskMapperService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.MockNvcfServer;
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
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class MonitorQueuedTasksRoutineTest {

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private MonitorQueuedTasksRoutine monitorQueuedTasksRoutine;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private EventService eventService;

    @Autowired
    private TaskService taskService;

    @Autowired
    private TaskMapperService taskMapperService;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testTaskService.clearAll();
    }

    @Test
    @SneakyThrows
    void testNonQueuedTaskRun() {
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        taskService.updateTask(TEST_TASK_ID_1, RUNNING);
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);

        monitorQueuedTasksRoutine.runUnchecked();

        // Verify task entity did not change
        var updatedTaskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(updatedTaskEntity).isEqualTo(taskEntity);

        // Verify entry in the EventsByTaskRepository did not change
        var updatedEvents = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isEqualTo(updatedEvents);
    }


    @Test
    @SneakyThrows
    void testNoCapacityRun() {
        var contexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                       .instanceState(NO_CAPACITY)
                                       .createTime(Instant.now().minus(Duration.ofMinutes(100))
                                                           .toString())
                                       .build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts);

        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();

        monitorQueuedTasksRoutine.runUnchecked();

        // Verify health and status of task is updated
        var updatedTask = taskService.fetchTask(TEST_TASK_ID_1);
        var healthInfo = taskMapperService.deserializeHealth(updatedTask.getHealth())
                .orElseThrow();
        assertThat(healthInfo).isNotNull();

        var expectedErrorLog = "Instance terminated due to to capacity constraint";
        assertThat(healthInfo.error()).contains(expectedErrorLog);
        assertThat(updatedTask.getStatus()).isEqualTo(EXCEEDED_MAX_QUEUED_DURATION);

        // Verify entry in the EventsByTaskRepository is updated
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isNotNull();
        assertThat(events.getLast().message()).contains(expectedErrorLog);
    }

    @Test
    @SneakyThrows
    void testNoCapacityButNotTimedOutRun() {
        var contexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                       .instanceState(NO_CAPACITY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts);

        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isPresent();
        assertThat(taskEntity.get().getStatus()).isEqualTo(QUEUED);
        assertThat(taskEntity.get().getHealth()).isNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isEmpty();

        monitorQueuedTasksRoutine.runUnchecked();

        // Verify task entity did not change
        var updatedTaskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(updatedTaskEntity).isPresent();
        assertThat(updatedTaskEntity.get().getStatus()).isEqualTo(ERRORED);
        assertThat(updatedTaskEntity.get().getHealth()).isNotNull();
        assertThat(taskMapperService.deserializeHealth(updatedTaskEntity.get().getHealth())
                           .orElseThrow()
                           .error()).isNotBlank();

        // Verify an event has been raised for the status change.
        var updatedEvents = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(updatedEvents).hasSize(1);
    }

    @Test
    @SneakyThrows
    void testHasCapacityRun() {
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);

        monitorQueuedTasksRoutine.runUnchecked();

        // Verify task entity did not change
        var updatedTaskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(updatedTaskEntity).isEqualTo(taskEntity);

        // Verify entry in the EventsByTaskRepository did not change
        var updatedEvents = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isEqualTo(updatedEvents);
    }

    @Test
    @SneakyThrows
    void testExceededMaxQueuedDurationRun() {
        var contexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                       .instanceState(MockIcmsServer.InstancesState.HEALTHY)
                                       .createTime(Instant.now().minus(Duration.ofHours(10))
                                                           .toString())
                                       .build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts);

        // Create a task with createdAt date 10 hours earlier
        testTaskService.createTask(TEST_NCA_ID,
                                   TEST_TASK_ID_1,
                                   TEST_ICMS_REQ_ID_1,
                                   Instant.now().minus(Duration.ofHours(10)));
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();

        monitorQueuedTasksRoutine.runUnchecked();

        // Verify task status is updated
        var updatedTask = taskService.fetchTask(TEST_TASK_ID_1);
        assertThat(updatedTask.getStatus()).isEqualTo(EXCEEDED_MAX_QUEUED_DURATION);

        // Verify entry in the EventsByTaskRepository is updated
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        var expectedErrorLog =
                "Changing status from 'QUEUED' to 'EXCEEDED_MAX_QUEUED_DURATION' with error";
        assertThat(events).isNotNull();
        assertThat(events.getLast().message()).contains(expectedErrorLog);
    }

    @Test
    @SneakyThrows
    void testNonExceededMaxQueuedDurationRun() {
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        // Create a task with createdAt date 1 min earlier
        testTaskService.createTask(TEST_NCA_ID,
                                   TEST_TASK_ID_1,
                                   TEST_ICMS_REQ_ID_1,
                                   Instant.now().minus(Duration.ofMinutes(1)));
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);

        monitorQueuedTasksRoutine.runUnchecked();

        // Verify task entity did not change
        var updatedTaskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(updatedTaskEntity).isEqualTo(taskEntity);

        // Verify entry in the EventsByTaskRepository did not change
        var updatedEvents = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isEqualTo(updatedEvents);
    }

}
