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
package com.nvidia.nvct.rest.event;

import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.LAUNCHED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.QUEUED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.RUNNING;
import static com.nvidia.nvct.service.event.EventService.STATUS_CHANGE_EVENT_MESSAGE;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_EVENTS;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_RESULTS;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.TestConstants.EVENT_ID_1;
import static com.nvidia.nvct.util.TestConstants.EVENT_ID_2;
import static com.nvidia.nvct.util.TestConstants.EVENT_ID_3;
import static com.nvidia.nvct.util.TestConstants.EVENT_ID_4;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT_2;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_2;
import static org.apache.commons.lang3.StringUtils.EMPTY;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.event.EventsByTaskRepository;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.rest.event.dto.EventDto;
import com.nvidia.nvct.rest.event.dto.ListEventsResponse;
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
class EventControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private TestEventService testEventService;

    @Autowired
    private AccountService accountService;

    @Autowired
    private ApiKeysService apiKeysService;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private EventsByTaskRepository eventsByTaskRepository;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvct.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockNvcfServer.start(nvcfBaseUrl);
        tasksRepository.deleteAll();
        eventsByTaskRepository.deleteAll();
    }

    @AfterAll
    void cleanup() {
        MockApiKeysServer.stop();
        MockNvcfServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void beforeEach() {
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID.
        var task1 = TestUtil.createTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                              jsonMapper);
        var task2 = TestUtil.createTaskEntity(TEST_TASK_ID_2, TEST_NCA_ID, TEST_TASK_NAME_2,
                                              jsonMapper);

        // Save tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);

        // Events for TEST_TASK_ID_1.
        testEventService.populateEventForTask(EVENT_ID_1, TEST_NCA_ID, TEST_TASK_ID_1,
                                              STATUS_CHANGE_EVENT_MESSAGE.formatted(EMPTY, QUEUED));
        testEventService.populateEventForTask(EVENT_ID_2, TEST_NCA_ID, TEST_TASK_ID_1,
                                              STATUS_CHANGE_EVENT_MESSAGE.formatted(QUEUED, LAUNCHED));
        testEventService.populateEventForTask(EVENT_ID_3, TEST_NCA_ID, TEST_TASK_ID_1,
                                              STATUS_CHANGE_EVENT_MESSAGE.formatted(LAUNCHED, RUNNING));

        // Event for TEST_TASK_ID_2 to make sure there is no cross contamination query
        testEventService.populateEventForTask(EVENT_ID_4, TEST_NCA_ID, TEST_TASK_ID_2,
                                              STATUS_CHANGE_EVENT_MESSAGE.formatted(EMPTY, RUNNING));
    }

    @AfterEach
    void reset() {
        MockApiKeysServer.resetToDefault();
        // use of MockApiKeysServer with different scopes causes the api-keys cache to dirty
        apiKeysService.invalidateCache();
        accountService.invalidateCache();
        tasksRepository.deleteAll();
        eventsByTaskRepository.deleteAll();
        testTaskService.clearAll();
    }

    Stream<Arguments> taskEventsArgs() {
        var jwtCases = Stream.of(
                // TEST_TASK_ID_1 belongs to TEST_NCA_ID.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(EVENT_ID_1, EVENT_ID_2, EVENT_ID_3),
                             HttpStatus.OK),
                // TEST_TASK_ID_2 belongs to TEST_NCA_ID.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_2,
                             Set.of(EVENT_ID_4),
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

        var apiKeysCases = Stream.of(
                // api-key with list_events scope and account-tasks resource
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("account-tasks", "*")),
                                                               List.of("list_events"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(EVENT_ID_1, EVENT_ID_2, EVENT_ID_3),
                             HttpStatus.OK),
                // api-key with list_events scope and specific task resource (matching)
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_1.toString())),
                                                               List.of("list_events"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(EVENT_ID_1, EVENT_ID_2, EVENT_ID_3),
                             HttpStatus.OK),
                // api-key with list_events scope and specific task resource (not matching)
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_2.toString())),
                                                               List.of("list_events"));
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
                                                               List.of("list_events"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.FORBIDDEN),
                // api-key with missing scope
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(), List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             Set.of(),
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeysCases);
    }

    @ParameterizedTest
    @MethodSource("taskEventsArgs")
    void shouldListEvents(Object tokenSupplier,
                          String ncaId,
                          UUID taskId,
                          Set<UUID> eventIds,
                          HttpStatus expectedStatus) {
        var token = TestUtil.getToken(tokenSupplier);
        var requestEntity =
                RequestEntity.get(URI.create("/v1/nvct/tasks/" + taskId + "/events"))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListEventsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.events()).hasSize(eventIds.size());
        assertThat(responseBody.cursor()).isNull();
        assertThat(responseBody.limit()).isNull();
        verifyReturnedEventsIntegrity(eventIds, responseBody.events(), ncaId, taskId);
    }

    void verifyReturnedEventsIntegrity(
            Set<UUID> expectedEvents, List<EventDto> events, String ncaId, UUID taskId) {
        for (var eventDto : events) {
            assertThat(eventDto.createdAt()).isNotNull();
            assertThat(eventDto.ncaId()).isEqualTo(ncaId);
            assertThat(eventDto.eventId()).isIn(expectedEvents);
            assertThat(eventDto.taskId()).isEqualTo(taskId);
            assertThat(eventDto.message()).isNotBlank();
        }
    }

    Stream<Arguments> listEventsWithPaginationLimitArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             1,
                             Set.of(EVENT_ID_1, EVENT_ID_2, EVENT_ID_3)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_EVENTS,
                                                          SCOPE_LIST_TASKS,
                                                          SCOPE_LIST_RESULTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_TASK_ID_1,
                             100,
                             Set.of(EVENT_ID_1, EVENT_ID_2, EVENT_ID_3))

                );
    }

    @ParameterizedTest
    @MethodSource("listEventsWithPaginationLimitArgs")
    void shouldListEventsWithPaginationLimit(String token,
                                              String ncaId,
                                              UUID taskId,
                                              int limit,
                                              Set<UUID> expectedEventIds) {
        var requestEntity =
                RequestEntity.get(URI.create("/v1/nvct/tasks/" + taskId + "/events?limit=" + limit))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListEventsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        if (limit < 3) {
            assertThat(responseBody.events()).hasSize(limit);
            assertThat(responseBody.cursor()).isNotNull();
            assertThat(responseBody.limit()).isEqualTo(limit);
        } else {
            assertThat(responseBody.events()).hasSize(3);
            assertThat(responseBody.cursor()).isNull();
            assertThat(responseBody.limit()).isNull();
        }

        verifyReturnedEventsIntegrity(expectedEventIds, responseBody.events(), ncaId, taskId);
    }

    @Test
    void shouldListEventsWithPaginationCursor() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LIST_EVENTS,
                                                 SCOPE_LIST_TASKS,
                                                 SCOPE_LIST_RESULTS),
                                                    100);
        var expectedResults = Set.of(EVENT_ID_1, EVENT_ID_2, EVENT_ID_3);

        // get the first 2 out of 3 events first
        var url = "/v1/nvct/tasks/" + TEST_TASK_ID_1 + "/events?limit=2";
        var requestEntity =
                RequestEntity.get(URI.create(url))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListEventsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var currentCursor = responseBody.cursor();
        assertThat(currentCursor).isNotNull();
        var firstPageEvent = responseBody.events();
        assertThat(firstPageEvent).hasSize(2);
        assertThat(responseBody.limit()).isEqualTo(2);

        // using the cursor from previous response, continue to listing the rest of the results
        url = "/v1/nvct/tasks/" + TEST_TASK_ID_1 + "/events?limit=2&cursor=" + currentCursor;
        requestEntity =
                RequestEntity.get(URI.create(url))
                        .header("Authorization", "Bearer " + token)
                        .build();
        responseEntity =
                testRestTemplate.exchange(requestEntity, ListEventsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // make sure the last event is correct
        responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        currentCursor = responseBody.cursor();
        assertThat(currentCursor).isNull();
        assertThat(responseBody.limit()).isNull();
        var secondPageEvent = responseBody.events();
        assertThat(secondPageEvent).hasSize(1);

        // make sure all 3 results have the right metadata
        verifyReturnedEventsIntegrity(expectedResults,
                                       Stream.concat(firstPageEvent.stream(), secondPageEvent.stream()).toList(),
                                       TEST_NCA_ID,
                                       TEST_TASK_ID_1);
    }
}
