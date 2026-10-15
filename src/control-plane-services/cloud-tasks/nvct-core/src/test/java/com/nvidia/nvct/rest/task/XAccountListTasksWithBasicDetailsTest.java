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
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_3;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.rest.task.dto.BasicTaskDto;
import com.nvidia.nvct.rest.task.dto.BulkTaskDetailsRequest;
import com.nvidia.nvct.rest.task.dto.ListBasicTaskDetailsResponse;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.util.MockNvcfServer;
import java.net.URI;
import java.net.URL;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
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
class XAccountListTasksWithBasicDetailsTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private AccountService accountService;

    @Autowired
    private TestTaskService testTaskService;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);

        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID.
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1, QUEUED);
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_2, TEST_ICMS_REQ_ID_2, RUNNING);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        accountService.invalidateCache();
        testTaskService.clearAll();
    }

    Stream<Arguments> listTasksWithBasicDetailsArgs() {
        return Stream.of(
                // List tasks with basic details in TEST_NCA_ID account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_TASKS),
                                                             100),
                             Set.of(TEST_TASK_ID_1, TEST_TASK_ID_2),
                             HttpStatus.OK),
                // Empty set of taskIds
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_TASKS),
                                                             100),
                             Set.of(),
                             HttpStatus.BAD_REQUEST),
                // List basic details for a non-existent TEST_TASK_ID_3 in TEST_NCA_ID account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_TASKS),
                                                             100),
                             Set.of(TEST_TASK_ID_1, TEST_TASK_ID_3),
                             HttpStatus.NOT_FOUND),
                // Missing scope.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(),
                                                             100),
                             Set.of(TEST_TASK_ID_1, TEST_TASK_ID_2),
                             HttpStatus.FORBIDDEN),
                // Missing token.
                Arguments.of(null,
                             Set.of(TEST_TASK_ID_1, TEST_TASK_ID_2),
                             HttpStatus.UNAUTHORIZED)
        );
    }

    @ParameterizedTest
    @MethodSource("listTasksWithBasicDetailsArgs")
    void shouldListTasksWithBasicDetails(String token,
                                         Set<UUID> expectedTaskIds,
                                         HttpStatus expectedStatus) {
        var requestBody = BulkTaskDetailsRequest.builder().taskIds(expectedTaskIds).build();
        var requestEntity =
                RequestEntity.post(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks/bulk"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListBasicTaskDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.tasks()).isNotNull().isNotEmpty();
        assertThat(responseBody.tasks()).hasSize(expectedTaskIds.size());
        var tasks = responseBody.tasks().stream()
                .collect(Collectors.toMap(BasicTaskDto::id, Function.identity()));
        assertThat(tasks.keySet()).containsExactlyInAnyOrderElementsOf(expectedTaskIds);
        var dtos = responseBody.tasks();
        dtos.forEach(dto -> {
            assertThat(dto.id()).isIn(expectedTaskIds);
            assertThat(dto.name()).isNotBlank();
            assertThat(dto.status()).isNotNull();
        });
        assertThat(responseBody.ncaId()).isEqualTo(TEST_NCA_ID);
    }
}
