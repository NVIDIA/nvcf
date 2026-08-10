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

package com.nvidia.boot.mock.volcengine;

import static com.nvidia.boot.mock.BootTestConstants.TEST_IMAGE_TYPE;
import static com.nvidia.boot.mock.TestConstants.MOCK_VOLCENGINE_REGISTRY_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class MockVolcengineRegistryServerTest {

    private static final String TEST_CUSTOM_REGISTRY = "test-custom-registry";
    private static final String TEST_CUSTOM_NAMESPACE = "test-custom-namespace";
    private static final String TEST_CUSTOM_REPOSITORY = "test-custom/repository";
    private static final String TEST_CUSTOM_TAG = "custom-tag";
    private static final String TEST_CUSTOM_RESPONSE = """
            {
                "ResponseMetadata": {
                    "RequestId": "test-custom-request-id",
                    "Action": "ListTags",
                    "Version": "2022-05-12",
                    "Service": "cr",
                    "Region": "cn-beijing"
                },
                "Result": {
                    "Registry": "test-custom-registry",
                    "Namespace": "test-custom-namespace",
                    "Repository": "test-custom/repository",
                    "Items": [
                        {
                            "Name": "custom-tag",
                            "Type": "Image",
                            "Digest": "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
                            "ImageAttributes": [
                                {
                                    "Architecture": "amd64",
                                    "Os": "linux",
                                    "Digest": "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
                                }
                            ]
                        }
                    ]
                }
            }
            """;

    @BeforeEach
    void setUp() {
        // Clean up any existing servers
        MockVolcengineRegistryServer.stop();
    }

    @AfterEach
    void tearDown() {
        MockVolcengineRegistryServer.stop();
    }

    @Test
    void testStartAndStop() {
        assertDoesNotThrow(
                () -> MockVolcengineRegistryServer.start(MOCK_VOLCENGINE_REGISTRY_URL));

        assertDoesNotThrow(() -> MockVolcengineRegistryServer.stop());
    }

    @Test
    void testStartMultipleTimes() {
        assertDoesNotThrow(
                () -> MockVolcengineRegistryServer.start(MOCK_VOLCENGINE_REGISTRY_URL));
        assertDoesNotThrow(
                () -> MockVolcengineRegistryServer.start(MOCK_VOLCENGINE_REGISTRY_URL));
        assertDoesNotThrow(() -> MockVolcengineRegistryServer.stop());
    }

    @Test
    void testStopWithoutStart() {
        // Test that stopping without starting doesn't cause issues
        assertDoesNotThrow(() -> MockVolcengineRegistryServer.stop());
    }

    @Test
    void testSetCustomResponse() {
        MockVolcengineRegistryServer.start(MOCK_VOLCENGINE_REGISTRY_URL);

        // Test setting a custom response
        assertDoesNotThrow(() -> MockVolcengineRegistryServer.setCustomResponse(
                TEST_CUSTOM_REGISTRY,
                TEST_CUSTOM_NAMESPACE,
                TEST_CUSTOM_REPOSITORY,
                TEST_IMAGE_TYPE,
                TEST_CUSTOM_TAG,
                TEST_CUSTOM_RESPONSE));

        MockVolcengineRegistryServer.stop();
    }

    @Test
    void testSetCustomResponseWithoutStart() {
        // Test that setting custom response without starting doesn't cause issues
        assertDoesNotThrow(() -> MockVolcengineRegistryServer.setCustomResponse(
                TEST_CUSTOM_REGISTRY,
                TEST_CUSTOM_NAMESPACE,
                TEST_CUSTOM_REPOSITORY,
                TEST_IMAGE_TYPE,
                TEST_CUSTOM_TAG,
                TEST_CUSTOM_RESPONSE));
    }

    @Test
    void testMultipleCustomResponses() {
        MockVolcengineRegistryServer.start(MOCK_VOLCENGINE_REGISTRY_URL);

        // Test setting multiple custom responses
        assertDoesNotThrow(() -> {
            MockVolcengineRegistryServer.setCustomResponse(
                    TEST_CUSTOM_REGISTRY,
                    TEST_CUSTOM_NAMESPACE,
                    TEST_CUSTOM_REPOSITORY,
                    TEST_IMAGE_TYPE,
                    TEST_CUSTOM_TAG,
                    TEST_CUSTOM_RESPONSE);

            MockVolcengineRegistryServer.setCustomResponse(
                    "another-registry",
                    "another-namespace",
                    "another-repo/test",
                    TEST_IMAGE_TYPE,
                    "another-tag",
                    TEST_CUSTOM_RESPONSE);
        });

        MockVolcengineRegistryServer.stop();
    }
}
