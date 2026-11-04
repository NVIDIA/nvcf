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
package com.nvidia.nvct.persistence.result;

import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.result.entity.ResultByTaskEntity;
import com.nvidia.nvct.persistence.result.entity.ResultByTaskKey;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.util.TestUtil;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
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

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ResultsByTaskRepositoryTest {

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private ResultsByTaskRepository resultsByTaskRepository;

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
        resultsByTaskRepository.deleteAll();
    }

    @AfterEach
    void reset() {
        tasksRepository.deleteAll();
        resultsByTaskRepository.deleteAll();
        testTaskService.clearAll();
    }

    @Test
    void testTaskResults() {
        var taskId = UUID.randomUUID();
        var task = TestUtil.createTaskEntity(taskId, TEST_NCA_ID, "task-1", jsonMapper);

        // Create Task.
        tasksRepository.save(task);

        // Create Task Results.
        var resultId1 = UUID.randomUUID();
        var result1 = ResultByTaskEntity.builder()
                .key(ResultByTaskKey.builder().taskId(taskId).resultId(resultId1).build())
                .ncaId(TEST_NCA_ID)
                .name("result-1")
                .metadata("{'foo': 'bar'}")
                .createdAt(Instant.now())
                .build();
        var resultId2 = UUID.randomUUID();
        var result2 = ResultByTaskEntity.builder()
                .key(ResultByTaskKey.builder().taskId(taskId).resultId(resultId2).build())
                .ncaId(TEST_NCA_ID)
                .name("result-2")
                .metadata("{'foo': 'baz'}")
                .createdAt(Instant.now())
                .build();
        resultsByTaskRepository.saveAll(List.of(result1, result2));

        // Verify Task Results.
        assertThat(resultsByTaskRepository.findByKeyTaskId(taskId)).hasSize(2);

        // Delete Task Result.
        resultsByTaskRepository.delete(result1);

        // Verify Task Events.
        assertThat(resultsByTaskRepository.findByKeyTaskId(taskId)).hasSize(1);
        assertThat(resultsByTaskRepository.getByKeyTaskIdAndKeyResultId(taskId, resultId2))
                .isPresent()
                .map(entity -> entity.getKey().getResultId())
                .hasValue(resultId2);
    }
}
