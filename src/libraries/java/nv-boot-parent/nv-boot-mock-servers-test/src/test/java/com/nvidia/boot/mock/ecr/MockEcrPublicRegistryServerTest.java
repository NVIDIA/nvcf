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

import static com.nvidia.boot.mock.TestConstants.MOCK_ECR_PUBLIC_REGISTRY_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class MockEcrPublicRegistryServerTest {

    private static final String TEST_CUSTOM_REPOSITORY = "test-custom/public-repository";
    private static final String TEST_CUSTOM_DIGEST = "sha256:fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321";
    private static final String TEST_CUSTOM_RESPONSE = """
            {
                "imageDetails": [
                    {
                        "imageDigest": "sha256:fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321",
                        "imageManifestMediaType": "application/vnd.oci.image.manifest.v1+json",
                        "imageTags": [
                            "custom-public-tag"
                        ],
                        "repositoryName": "test-custom/public-repository"
                    }
                ]
            }
            """;

    @BeforeEach
    void setUp() {
        // Clean up any existing servers
        MockEcrPublicRegistryServer.stop();
    }

    @AfterEach
    void tearDown() {
        MockEcrPublicRegistryServer.stop();
    }

    @Test
    void testStartAndStop() {
        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL));
        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.stop());
    }

    @Test
    void testStartMultipleTimes() {
        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL));
        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL));
        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.stop());
    }

    @Test
    void testStopWithoutStart() {
        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.stop());
    }

    @Test
    void testSetCustomResponse() {
        MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL);

        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.setCustomResponse(
                TEST_CUSTOM_REPOSITORY, 
                TEST_CUSTOM_DIGEST, 
                TEST_CUSTOM_RESPONSE));
        
        MockEcrPublicRegistryServer.stop();
    }

    @Test
    void testSetCustomResponseWithoutStart() {
        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.setCustomResponse(
                TEST_CUSTOM_REPOSITORY, 
                TEST_CUSTOM_DIGEST, 
                TEST_CUSTOM_RESPONSE));
    }

    @Test
    void testMultipleCustomResponses() {
        MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL);

        assertDoesNotThrow(() -> {
            MockEcrPublicRegistryServer.setCustomResponse(
                    TEST_CUSTOM_REPOSITORY, 
                    TEST_CUSTOM_DIGEST, 
                    TEST_CUSTOM_RESPONSE);
            
            MockEcrPublicRegistryServer.setCustomResponse(
                    "another-public-repo/test", 
                    "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", 
                    TEST_CUSTOM_RESPONSE);
        });
        
        MockEcrPublicRegistryServer.stop();
    }

    @Test
    void testServerLifecycle() {
        assertDoesNotThrow(() -> {
            MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL);

            MockEcrPublicRegistryServer.setCustomResponse(
                    TEST_CUSTOM_REPOSITORY, 
                    TEST_CUSTOM_DIGEST, 
                    TEST_CUSTOM_RESPONSE);
            
            // Restart server (should handle cleanup properly)
            MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL);
            
            // Stop server
            MockEcrPublicRegistryServer.stop();
        });
    }

    @Test
    void testMultipleStopsAreSafe() {
        // Test that multiple stops don't cause issues
        MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL);
        
        assertDoesNotThrow(() -> {
            MockEcrPublicRegistryServer.stop();
            MockEcrPublicRegistryServer.stop();
            MockEcrPublicRegistryServer.stop();
        });
    }

    @Test
    void testSetCustomResponseAfterRestart() {
        // Test setting custom response after restart
        MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL);
        MockEcrPublicRegistryServer.setCustomResponse(
                TEST_CUSTOM_REPOSITORY, 
                TEST_CUSTOM_DIGEST, 
                TEST_CUSTOM_RESPONSE);
        
        // Restart and set again
        MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_URL);
        
        assertDoesNotThrow(() -> MockEcrPublicRegistryServer.setCustomResponse(
                "new-repo/after-restart", 
                "sha256:1111111111111111111111111111111111111111111111111111111111111111", 
                TEST_CUSTOM_RESPONSE));
        
        MockEcrPublicRegistryServer.stop();
    }
}
