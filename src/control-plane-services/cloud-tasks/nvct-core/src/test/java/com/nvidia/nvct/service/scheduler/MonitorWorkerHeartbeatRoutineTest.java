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

import static com.nvidia.nvct.persistence.task.entity.TaskStatus.CANCELED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.COMPLETED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.ERRORED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.LAUNCHED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.QUEUED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.RUNNING;
import static com.nvidia.nvct.service.event.EventService.STATUS_CHANGE_EVENT_MESSAGE;
import static com.nvidia.nvct.util.MockIcmsServer.InstancesState.NO_CAPACITY;
import static com.nvidia.nvct.util.TestConstants.EVENT_ID_1;
import static com.nvidia.nvct.util.TestConstants.EVENT_ID_2;
import static com.nvidia.nvct.util.TestConstants.EVENT_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.event.TestEventService;
import com.nvidia.nvct.service.event.EventService;
import com.nvidia.nvct.service.icms.IcmsService;
import com.nvidia.nvct.service.task.TaskMapperService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.MockNvcfServer;
import java.net.URL;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.stream.Stream;
import lombok.SneakyThrows;
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
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class MonitorWorkerHeartbeatRoutineTest {

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private MonitorWorkerHeartbeatRoutine monitorWorkerHeartbeatRoutine;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private TestEventService testEventService;

    @Autowired
    private EventService eventService;

    @Autowired
    private TaskService taskService;

    @Autowired
    private IcmsService icmsService;

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
    void testNonRunningTaskRun() {
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);

        // Explicitly set the task status to non-running such as launched, so that background
        // thread won't monitor it.
        taskService.updateTask(TEST_TASK_ID_1, LAUNCHED);
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);

        monitorWorkerHeartbeatRoutine.runUnchecked();

        // Verify task entity did not change
        var updatedTaskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(updatedTaskEntity).isEqualTo(taskEntity);

        // Verify entry in the EventsByTaskRepository did not change.
        var updatedEvents = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isEqualTo(updatedEvents);
    }

    @Test
    @SneakyThrows
    void testNoHeartbeatRun() {
        var contexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                       .instanceState(NO_CAPACITY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);

        // Explicitly set the task status to running and set lastHeartbeatAt to more than 4m ago
        // so that background thread will monitor it.
        taskService.updateTask(TEST_TASK_ID_1, RUNNING, Instant.now().minus(Duration.ofMinutes(5)));
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();

        monitorWorkerHeartbeatRoutine.runUnchecked();

        // Verify health and status of task is updated.
        var updatedTask = taskService.fetchTask(TEST_TASK_ID_1);
        var healthInfo = taskMapperService.deserializeHealth(updatedTask.getHealth())
                .orElseThrow();
        assertThat(healthInfo).isNotNull();
        var expectedErrorLog = "Instance terminated due to to capacity constraint";
        assertThat(healthInfo.error()).contains(expectedErrorLog);
        assertThat(updatedTask.getStatus()).isEqualTo(ERRORED);

        // Verify entry in the EventsByTaskRepository is updated.
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isNotNull();
        assertThat(events.getLast().message()).contains(expectedErrorLog);
    }

    @Test
    @SneakyThrows
    void testHeartbeatNotTimedOutRun() {
        var contexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                       .instanceState(NO_CAPACITY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, contexts);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);

        // Explicitly set the task status to running, but set lastHeartbeatAt to now
        // so that background thread won't monitor it.
        taskService.updateTask(TEST_TASK_ID_1, RUNNING, Instant.now());
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isNotNull();
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);

        monitorWorkerHeartbeatRoutine.runUnchecked();

        // Verify task entity did not change.
        var updatedTaskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(updatedTaskEntity).isEqualTo(taskEntity);

        // Verify entry in the EventsByTaskRepository did not change.
        var updatedEvents = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).isEqualTo(updatedEvents);
    }

    Stream<Arguments> usingTerminalStatusFromLatestEventArgs() {
        return Stream.of(
                Arguments.of(COMPLETED,
                             STATUS_CHANGE_EVENT_MESSAGE.formatted(RUNNING, COMPLETED)),
                Arguments.of(CANCELED,
                             STATUS_CHANGE_EVENT_MESSAGE.formatted(RUNNING, CANCELED))
        );
    }

    @ParameterizedTest
    @MethodSource("usingTerminalStatusFromLatestEventArgs")
    void testUsingTerminalStatusFromLatestEvent(
            TaskStatus terminalStatus,
            String eventMessage) {
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, List.of());

        // Create a Task by faking the creation time to be 10 minutes in the past.
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   Instant.now().minus(Duration.ofMinutes(10)));

        // Simulate events for TEST_TASK_ID_1.
        testEventService.populateEventForTask(EVENT_ID_1, TEST_NCA_ID, TEST_TASK_ID_1,
                                              STATUS_CHANGE_EVENT_MESSAGE.formatted(QUEUED, LAUNCHED));
        testEventService.populateEventForTask(EVENT_ID_2, TEST_NCA_ID, TEST_TASK_ID_1,
                                              STATUS_CHANGE_EVENT_MESSAGE.formatted(LAUNCHED, RUNNING));

        // Explicitly set the Task status to RUNNING and the heartbeat timestamp to be 6 min
        // in the past.
        taskService.updateTask(TEST_TASK_ID_1, RUNNING, Instant.now().minus(Duration.ofMinutes(6)));

        // Verify that the status of the Task is RUNNING.
        var taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isPresent();
        assertThat(taskEntity.get().getStatus()).isEqualTo(RUNNING);

        // Simulate race condition where the Task is marked with terminal status and then
        // immediately marked as RUNNING because of heartbeat. When the Task is marked with
        // terminal status, an event is generated and the corresponding ICMS request is
        // terminated/deleted. Let's set the heartbeat timestamp to be 5min in the past so
        // that async monitoring routine picks it up.
        taskService.updateTask(TEST_TASK_ID_1, terminalStatus);
        testEventService.populateEventForTask(EVENT_ID_3, TEST_NCA_ID, TEST_TASK_ID_1,
                                              eventMessage);
        icmsService.terminateInstanceByTaskId(TEST_NCA_ID, TEST_TASK_ID_1);
        taskService.updateTask(TEST_TASK_ID_1, RUNNING,               // Back to RUNNING
                               Instant.now().minus(Duration.ofMinutes(5)));

        // Verify that the status of the Task is back to RUNNING.
        taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isPresent();
        assertThat(taskEntity.get().getStatus()).isEqualTo(RUNNING);

        // Verify that there are 3 events now.
        var events = eventService.fetchEvents(TEST_NCA_ID, TEST_TASK_ID_1);
        assertThat(events).hasSize(3);

        // Run the monitoring routine that processes Tasks with status RUNNING and
        // last heartbeat timestamp more than 4 minutes in the past. The routine should
        // restore the status of the Task using the terminal status from the last event.
        monitorWorkerHeartbeatRoutine.runUnchecked();

        // Verify that the Task now has a terminal status.
        taskEntity = tasksRepository.findById(TEST_TASK_ID_1);
        assertThat(taskEntity).isPresent();
        assertThat(taskEntity.get().getStatus()).isEqualTo(terminalStatus);
    }
}
