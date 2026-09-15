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
package com.nvidia.nvct.persistence.task;

import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_2;
import static org.assertj.core.api.Assertions.assertThat;

import com.datastax.oss.driver.api.core.CqlSession;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.task.dto.HealthDto;
import com.nvidia.nvct.service.task.TaskMapperService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.TestUtil;
import java.time.Instant;
import java.util.UUID;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
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
class TasksRepositoryTest {
    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private TaskMapperService taskMapperService;

    @Autowired
    private TaskService taskService;

    @Autowired
    private CqlSession cqlSession;

    @Autowired
    private JsonMapper jsonMapper;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
    }

    @AfterAll
    void cleanup() {
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void init() {
        tasksRepository.deleteAll();
    }

    @AfterEach
    void reset() {
        tasksRepository.deleteAll();
        testTaskService.clearAll();
    }

    @Test
    void createAndDeleteTask() {
        var taskId = UUID.randomUUID();
        var task = TestUtil.createTaskEntity(taskId, TEST_NCA_ID, "task-1", jsonMapper);

        // Save the task.
        tasksRepository.save(task);

        // Make sure that the entry was created in all the tables.
        assertThat(tasksRepository.getByTaskId(taskId))
                .isPresent()
                .map(entity -> entity.getTaskId())
                .hasValue(taskId);
        assertThat(tasksRepository.getByTaskId(taskId))
                .isPresent()
                .map(entity -> entity.getTaskId())
                .hasValue(taskId);

        assertThat(tasksRepository.findAll()).hasSize(1);
        assertThat(tasksRepository.countByNcaId(TEST_NCA_ID)).isEqualTo(1);
        assertThat(tasksRepository.getByTaskId(taskId))
                .isPresent()
                .map(entity -> entity.getTaskId())
                .hasValue(taskId);
        var entity = tasksRepository.getByTaskId(taskId).get();
        assertThat(entity.getMaxRuntimeDuration()).isNotNull();

        // Delete the task using.
        tasksRepository.delete(task);

        // Make sure that the entry was deleted from the table.
        assertThat(tasksRepository.getByTaskId(taskId)).isNotPresent();
    }

    @SneakyThrows
    @Test
    void countTasksByNcaId() {
        var taskId1 = UUID.randomUUID();
        var task1 = TestUtil.createTaskEntity(taskId1, TEST_NCA_ID, "task-1", jsonMapper);

        var taskId2 = UUID.randomUUID();
        var task2 = TestUtil.createTaskEntity(taskId2, TEST_NCA_ID, "task-2", jsonMapper);

        var taskId3 = UUID.randomUUID();
        var task3 = TestUtil.createTaskEntity(taskId3, TEST_NCA_ID_2, "task-3", jsonMapper);

        // Save the tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);
        tasksRepository.save(task3);

        // Confirm that there are three Tasks in the repository.
        assertThat(tasksRepository.count()).isEqualTo(3);

        // Confirm there are 2 Tasks belonging to TEST_NCA_ID account.
        assertThat(tasksRepository.countByNcaId(TEST_NCA_ID)).isEqualTo(2);
    }

    @Test
    void updateTask() throws InterruptedException {
        var taskId = UUID.randomUUID();
        var task = TestUtil.createTaskEntity(taskId, TEST_NCA_ID, "task-1", jsonMapper);

        // Save the task.
        tasksRepository.save(task);

        assertThat(tasksRepository.findAll()).hasSize(1);

        // Check the initial status.
        assertThat(tasksRepository.getByTaskId(taskId))
                .isPresent()
                .map(TaskEntity::getStatus)
                .hasValue(TaskStatus.QUEUED);

        // Change the status to LAUNCHED.
        task.setStatus(TaskStatus.LAUNCHED);

        // Update the task.
        var timestamp = Instant.now(); // Save the timestamp before updating the task.
        Thread.sleep(1000);            // Sleep for a second before updating the task.
        tasksRepository.insert(task);

        // Verify new status.
        assertThat(tasksRepository.getByTaskId(taskId))
                .isPresent()
                .map(TaskEntity::getStatus)
                .hasValue(TaskStatus.LAUNCHED);

        // Verify last_updated_at is after the saved timestamp.
        assertThat(tasksRepository.getByTaskId(taskId))
                .isPresent()
                .map(TaskEntity::getLastUpdatedAt)
                .hasValueSatisfying(updatedAt -> {
                    assertThat(updatedAt).isAfter(timestamp);
                });

        assertThat(tasksRepository.findAll()).hasSize(1);
    }

    @Test
    void writesHealthDetailsAsJsonToHealthColumnOnly() {
        var taskId = UUID.randomUUID();
        var task = TestUtil.createTaskEntity(taskId, TEST_NCA_ID, "task-1", jsonMapper);
        var healthInfo = taskMapperService.deserializeHealth(task.getHealth()).orElseThrow();

        tasksRepository.save(task);

        var row = cqlSession.execute(
                "SELECT health FROM tasks_v2 WHERE task_id = ?", taskId).one();
        assertThat(row).isNotNull();
        assertThat(row.getString(TaskEntity.COLUMN_HEALTH))
                .doesNotContain("sis_request_id")
                .contains("\"gpu\":\"" + healthInfo.gpu() + "\"")
                .contains("\"backend\":\"" + healthInfo.backend() + "\"")
                .contains("\"instanceType\":\"" + healthInfo.instanceType() + "\"")
                .contains("\"error\":\"" + healthInfo.error() + "\"");

        var updatedHealthInfo = HealthDto.builder()
                .backend("GFN")
                .gpu("T10")
                .instanceType("g6.full")
                .error("updated-error")
                .build();

        taskService.updateTask(taskId, TaskStatus.ERRORED, updatedHealthInfo);

        row = cqlSession.execute(
                "SELECT health FROM tasks_v2 WHERE task_id = ?", taskId).one();
        assertThat(row).isNotNull();
        assertThat(row.getString(TaskEntity.COLUMN_HEALTH)).contains("updated-error");

        var reloadedTask = tasksRepository.getByTaskId(taskId).orElseThrow();
        assertThat(taskMapperService.toTaskDto(reloadedTask).healthInfo()).isEqualTo(updatedHealthInfo);
    }
}
