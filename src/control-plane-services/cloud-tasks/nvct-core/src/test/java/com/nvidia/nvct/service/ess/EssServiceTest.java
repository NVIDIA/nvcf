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
package com.nvidia.nvct.service.ess;

import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;
import static com.nvidia.nvct.util.NvctConstants.NGC_API_KEY;
import static com.nvidia.nvct.util.TestConstants.REGISTRY_CRED_ID_DOCKER;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.node.StringNode;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.MockEssServer;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.assertj.core.api.Assertions;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@ExtendWith(MockitoExtension.class)
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class EssServiceTest {


    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private EssService essService;

    @Autowired
    private TaskService taskService;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockEssServer.start(essBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockEssServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        MockEssServer.clearSecrets();
        testTaskService.clearAll();
    }

    @Test
    void testSavingSecrets() {
        // Create a task
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        
        // Save secrets for a task.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());
        var version = essService.saveSecrets(TEST_TASK_ID_1, secrets);
        assertThat(version).isNotNull();
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        // Verify saved secrets for the task.
        essService.getSecrets(TEST_TASK_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(1);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isEqualTo("AWS_SECRET_ACCESS_KEY");
                        assertThat(dto.value()).isEqualTo(new StringNode("shhh!"));
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secrets"));
    }

    @Test
    void testUpdatingSecrets() {
        // Create a task
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        
        // Save secrets for a task.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());
        var version = essService.saveSecrets(TEST_TASK_ID_1, secrets);
        assertThat(version).isNotNull();
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        // Verify saved secrets for the task.
        essService.getSecrets(TEST_TASK_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(1);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isEqualTo("AWS_SECRET_ACCESS_KEY");
                        assertThat(dto.value()).isEqualTo(new StringNode("shhh!"));
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secrets"));

        // Update secrets for the task.
        var updatedSecrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("confidential!"))
                                     .build(),
                             SecretDto.builder()
                                     .name(NGC_API_KEY)
                                     .value(new StringNode("shhh!shhh!"))
                                     .build());
        var newVersion = essService.saveSecrets(TEST_TASK_ID_1, updatedSecrets);
        assertThat(newVersion).isNotNull();

        // Verify updated secrets for the task.
        var expectedSecretValues = Set.of(new StringNode("confidential!"),
                                          new StringNode("shhh!shhh!"));
        essService.getSecrets(TEST_TASK_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(2);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isIn(Set.of("AWS_SECRET_ACCESS_KEY", NGC_API_KEY));
                        assertThat(dto.value()).isIn(expectedSecretValues);
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to update secrets"));
    }

    @Test
    void testFetchingSecretNames() {
        // Create a task
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        
        // Save secrets for a task.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build(),
                             SecretDto.builder()
                                     .name(NGC_API_KEY)
                                     .value(new StringNode("shhh!shhh!"))
                                     .build());
        var version = essService.saveSecrets(TEST_TASK_ID_1, secrets);
        assertThat(version).isNotNull();
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        // Get secret names for the task.
        var result = essService.getSecretNames(TEST_TASK_ID_1)
                .orElse(null);
        assertThat(result).isNotNull().hasSize(2);
        assertThat(result).containsExactlyInAnyOrder("AWS_SECRET_ACCESS_KEY", NGC_API_KEY);
    }

    @Test
    void testFetchingNonExistingSecretNames() {
        // Fetch secret names without saving secrets for a task.
        var result = essService.getSecretNames(TEST_TASK_ID_1);
        assertThat(result).isNotNull().isEmpty();
    }

    @Test
    void testFetchingNonExistingSecrets() {
        // Fetch secrets without saving secrets for a task.
        var secretDtos = essService.getSecrets(TEST_TASK_ID_1);
        assertThat(secretDtos).isNotNull().isEmpty();
    }

    @Test
    void testRegistryCredentialSecretServedFromCache() {
        // The EssService cache is a shared singleton, so the credential may already be cached by
        // another test. Assert on the delta of the second read instead of an absolute request count.
        var essRequest = getRequestedFor(urlPathMatching(
                "/v1/accounts/[^/]+/registry-credentials/" + REGISTRY_CRED_ID_DOCKER)).build();

        var first = essService.getRegistryCredentialSecret(TEST_NCA_ID, REGISTRY_CRED_ID_DOCKER);
        var essRequestsAfterFirstRead =
                MockEssServer.getMockEssServer().countRequestsMatching(essRequest).getCount();

        var second = essService.getRegistryCredentialSecret(TEST_NCA_ID, REGISTRY_CRED_ID_DOCKER);
        var essRequestsAfterSecondRead =
                MockEssServer.getMockEssServer().countRequestsMatching(essRequest).getCount();

        assertThat(first).isPresent();
        assertThat(second).isPresent();
        assertThat(second.get().value()).isEqualTo(first.get().value());

        // The second read is served from the cache and does not hit ESS again.
        assertThat(essRequestsAfterSecondRead).isEqualTo(essRequestsAfterFirstRead);
    }

    @Test
    void testMissingRegistryCredentialSecretIsNotCached() {
        // A random id is never cached by another test, so counts start from zero.
        var unknownRegistryCredentialId = UUID.randomUUID();
        var essRequest = getRequestedFor(urlPathMatching(
                "/v1/accounts/[^/]+/registry-credentials/" + unknownRegistryCredentialId)).build();

        var first = essService.getRegistryCredentialSecret(TEST_NCA_ID, unknownRegistryCredentialId);
        var essRequestsAfterFirstRead =
                MockEssServer.getMockEssServer().countRequestsMatching(essRequest).getCount();

        var second = essService.getRegistryCredentialSecret(TEST_NCA_ID, unknownRegistryCredentialId);
        var essRequestsAfterSecondRead =
                MockEssServer.getMockEssServer().countRequestsMatching(essRequest).getCount();

        assertThat(first).isEmpty();
        assertThat(second).isEmpty();

        // A missing secret is not cached, so each read hits ESS.
        assertThat(essRequestsAfterFirstRead).isEqualTo(1);
        assertThat(essRequestsAfterSecondRead).isEqualTo(2);
    }

    @Test
    void testDeletingSecrets() {
        // Create a task
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        
        // Save secrets for a task.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());
        var version = essService.saveSecrets(TEST_TASK_ID_1, secrets);
        assertThat(version).isNotNull();
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        // Verify saved secrets for the task.
        essService.getSecrets(TEST_TASK_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(1);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isEqualTo("AWS_SECRET_ACCESS_KEY");
                        assertThat(dto.value()).isEqualTo(new StringNode("shhh!"));
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secrets"));

        // Delete secrets saved for the task.
        essService.deleteSecrets(TEST_TASK_ID_1);

        // Fetch secrets after deleting them.
        var secretDtos = essService.getSecrets(TEST_TASK_ID_1);
        assertThat(secretDtos).isNotNull().isEmpty();
    }

    @Test
    void testDeletingSecretsPath() {
        // Create a task
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        
        // Save secrets for a task.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());
        var version = essService.saveSecrets(TEST_TASK_ID_1, secrets);
        assertThat(version).isNotNull();
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        // Verify saved secrets for the task.
        essService.getSecrets(TEST_TASK_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(1);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isEqualTo("AWS_SECRET_ACCESS_KEY");
                        assertThat(dto.value()).isEqualTo(new StringNode("shhh!"));
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secrets"));

        // Delete secrets path.
        essService.deleteSecretsPath(TEST_TASK_ID_1);

        // Fetch secrets after deleting the path.
        var secretDtos = essService.getSecrets(TEST_TASK_ID_1);
        assertThat(secretDtos).isNotNull().isEmpty();
    }

}
