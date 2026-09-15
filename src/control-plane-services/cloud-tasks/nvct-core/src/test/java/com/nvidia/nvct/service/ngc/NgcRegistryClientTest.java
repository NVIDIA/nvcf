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
package com.nvidia.nvct.service.ngc;

import static com.nvidia.nvct.util.TestConstants.TEST_NGC_API_KEY;
import static com.nvidia.nvct.util.TestConstants.TEST_UNKNOWN_ORG_NAME;
import static com.nvidia.nvct.util.TestConstants.TEST_UNKNOWN_TEAM_NAME;
import static com.nvidia.nvct.util.TestConstants.TEST_VALID_ORG_NAME;
import static com.nvidia.nvct.util.TestConstants.TEST_VALID_ORG_NAME_INVALID_KEY;
import static com.nvidia.nvct.util.TestConstants.TEST_VALID_TEAM_NAME;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.assertj.core.api.Assertions;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
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
class NgcRegistryClientTest {

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private NgcRegistryClient ngcRegistryClient;

    @Autowired
    private TestTaskService testTaskService;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockCasServer.start(authnBaseUrl, casBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockCasServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testTaskService.clearAll();
    }


    Stream<Arguments> jsonNodeArgs() {
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        return Stream.of(
                Arguments.of(secretJsonNodeValue),
                Arguments.of(new StringNode(TEST_NGC_API_KEY)));
    }

    @ParameterizedTest
    @MethodSource("jsonNodeArgs")
    void testValidOrg(JsonNode jsonNode) {
        var resultsLocation = TEST_VALID_ORG_NAME + "/test-model-2";

        try {
            ngcRegistryClient.validate(jsonNode, resultsLocation);
        } catch (Exception ex) {
            Assertions.fail(ex.getMessage());
        }
    }

    @ParameterizedTest
    @MethodSource("jsonNodeArgs")
    void testValidOrgAndValidTeam(JsonNode jsonNode) {
        var resultsLocation = TEST_VALID_ORG_NAME + "/" + TEST_VALID_TEAM_NAME + "/test-model-2";

        try {
            ngcRegistryClient.validate(jsonNode, resultsLocation);
        } catch (Exception ex) {
            Assertions.fail(ex.getMessage());
        }
    }

    @ParameterizedTest
    @MethodSource("jsonNodeArgs")
    void testNonExistentOrg(JsonNode jsonNode) {
        var resultsLocation = TEST_UNKNOWN_ORG_NAME + "/test-model-2";

        assertThatExceptionOfType(NotFoundException.class)
                .isThrownBy(() -> ngcRegistryClient.validate(jsonNode, resultsLocation));
    }

    @ParameterizedTest
    @MethodSource("jsonNodeArgs")
    void testNonExistentTeam(JsonNode jsonNode) {
        var resultsLocation = TEST_VALID_ORG_NAME + "/" + TEST_UNKNOWN_TEAM_NAME + "/test-model-2";

        assertThatExceptionOfType(NotFoundException.class)
                .isThrownBy(() -> ngcRegistryClient.validate(jsonNode, resultsLocation));
    }

    @ParameterizedTest
    @MethodSource("jsonNodeArgs")
    void testInvalidApiKey(JsonNode jsonNode) {
        var resultsLocation = TEST_VALID_ORG_NAME_INVALID_KEY +  "/test-model-2";

        assertThatExceptionOfType(UnauthorizedException.class)
                .isThrownBy(() -> ngcRegistryClient.validate(jsonNode, resultsLocation));
    }

    @ParameterizedTest
    @MethodSource("jsonNodeArgs")
    void testModelNameShort(JsonNode jsonNode) {
        // Model name with 15 characters (well within the limit)
        // Should not require truncation
        var shortModelName = "short";
        var resultsLocation = TEST_VALID_ORG_NAME + "/" + shortModelName;

        // Should not throw exception - no truncation needed
        ngcRegistryClient.validate(jsonNode, resultsLocation);
    }

    @ParameterizedTest
    @MethodSource("jsonNodeArgs")
    void testModelNameAtMaxLength(JsonNode jsonNode) {
        // Model name with exactly 64 characters (at the limit)
        // Should be automatically truncated to fit UUID
        var maxLengthModelName = "a".repeat(64);
        var resultsLocation = TEST_VALID_ORG_NAME + "/" + maxLengthModelName;

        // Should not throw exception - will be truncated to fit UUID
        ngcRegistryClient.validate(jsonNode, resultsLocation);
    }

    @ParameterizedTest
    @MethodSource("jsonNodeArgs")
    void testModelNameExceedsMaxLength(JsonNode jsonNode) {
        // Model name with 65 characters (exceeds the 64 character limit)
        var tooLongModelName = "a".repeat(65);
        var resultsLocation = TEST_VALID_ORG_NAME + "/" + tooLongModelName;

        assertThatExceptionOfType(BadRequestException.class)
                .isThrownBy(() -> ngcRegistryClient.validate(jsonNode, resultsLocation))
                .withMessageContaining("exceeds the maximum allowed length of 64 characters");
    }

}
