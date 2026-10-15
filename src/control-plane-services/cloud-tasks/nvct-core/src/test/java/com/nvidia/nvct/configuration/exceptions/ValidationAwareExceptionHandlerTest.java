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
package com.nvidia.nvct.configuration.exceptions;

import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAG_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.NGC_API_KEY;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LAUNCH_TASK;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ENVIRONMENT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static org.assertj.core.api.AssertionsForClassTypes.assertThat;

import tools.jackson.databind.node.StringNode;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import java.net.URI;
import java.time.Duration;
import java.util.List;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
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
class ValidationAwareExceptionHandlerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
    }

    @AfterAll
    void cleanup() {
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @Test
    void validationErrorShouldContainDetails() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK), 100);
        var tags = Set.of(StringUtils.repeat("tag1", MAX_TAG_LENGTH),
                          StringUtils.repeat("tag2", MAX_TAG_LENGTH));
        var secrets = Set.of(SecretDto.builder()
                                     .name(NGC_API_KEY)
                                     .value(new StringNode("shhh!shhh!"))
                                     .build(),
                             SecretDto.builder()
                                     .name("secret2")
                                     .value(new StringNode("confidential"))
                                     .build());
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .tags(tags)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(3))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .secrets(secrets)
                .containerEnvironment(TEST_CONTAINER_ENVIRONMENT)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        log.info(responseEntity.getBody());
        // Error message should contain the failed value, the max correct value, and the
        // field in error
        assertThat(responseEntity.getBody()).contains(Integer.toString(MAX_TAG_LENGTH), "tags");
    }
}
