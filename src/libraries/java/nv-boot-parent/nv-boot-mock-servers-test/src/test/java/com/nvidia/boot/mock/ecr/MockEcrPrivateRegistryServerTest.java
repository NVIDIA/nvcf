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

package com.nvidia.boot.mock.ecr;

import static com.nvidia.boot.mock.TestConstants.MOCK_ECR_REGISTRY_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class MockEcrPrivateRegistryServerTest {

    private static final String TEST_CUSTOM_REPOSITORY = "test-custom/repository";
    private static final String TEST_CUSTOM_DIGEST =
            "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890";
    private static final String TEST_CUSTOM_RESPONSE = """
            {
                "imageDetails": [
                    {
                        "imageDigest": "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
                        "imageManifestMediaType": "application/vnd.oci.image.manifest.v1+json",
                        "imageTags": [
                            "custom-tag"
                        ],
                        "registryId": "123456789012",
                        "repositoryName": "test-custom/repository"
                    }
                ]
            }
            """;

    @BeforeEach
    void setUp() {
        // Clean up any existing servers
        MockEcrPrivateRegistryServer.stop();
    }

    @AfterEach
    void tearDown() {
        MockEcrPrivateRegistryServer.stop();
    }

    @Test
    void testStartAndStop() {
        // Test that start doesn't throw any exceptions
        assertDoesNotThrow(() -> MockEcrPrivateRegistryServer.start(MOCK_ECR_REGISTRY_URL));

        // Test that stop doesn't throw any exceptions
        assertDoesNotThrow(() -> MockEcrPrivateRegistryServer.stop());
    }

    @Test
    void testStartMultipleTimes() {
        // Test that starting multiple times doesn't cause issues
        assertDoesNotThrow(() -> MockEcrPrivateRegistryServer.start(MOCK_ECR_REGISTRY_URL));
        assertDoesNotThrow(() -> MockEcrPrivateRegistryServer.start(MOCK_ECR_REGISTRY_URL));
        assertDoesNotThrow(() -> MockEcrPrivateRegistryServer.stop());
    }

    @Test
    void testStopWithoutStart() {
        // Test that stopping without starting doesn't cause issues
        assertDoesNotThrow(() -> MockEcrPrivateRegistryServer.stop());
    }

    @Test
    void testSetCustomResponse() {
        MockEcrPrivateRegistryServer.start(MOCK_ECR_REGISTRY_URL);

        // Test setting a custom response
        assertDoesNotThrow(() -> MockEcrPrivateRegistryServer.setCustomResponse(
                TEST_CUSTOM_REPOSITORY,
                TEST_CUSTOM_DIGEST,
                TEST_CUSTOM_RESPONSE));

        MockEcrPrivateRegistryServer.stop();
    }

    @Test
    void testSetCustomResponseWithoutStart() {
        // Test that setting custom response without starting doesn't cause issues
        assertDoesNotThrow(() -> MockEcrPrivateRegistryServer.setCustomResponse(
                TEST_CUSTOM_REPOSITORY,
                TEST_CUSTOM_DIGEST,
                TEST_CUSTOM_RESPONSE));
    }

    @Test
    void testMultipleCustomResponses() {
        MockEcrPrivateRegistryServer.start(MOCK_ECR_REGISTRY_URL);

        // Test setting multiple custom responses
        assertDoesNotThrow(() -> {
            MockEcrPrivateRegistryServer.setCustomResponse(
                    TEST_CUSTOM_REPOSITORY,
                    TEST_CUSTOM_DIGEST,
                    TEST_CUSTOM_RESPONSE);

            MockEcrPrivateRegistryServer.setCustomResponse(
                    "another-repo/test",
                    "sha256:fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321",
                    TEST_CUSTOM_RESPONSE);
        });

        MockEcrPrivateRegistryServer.stop();
    }
}
