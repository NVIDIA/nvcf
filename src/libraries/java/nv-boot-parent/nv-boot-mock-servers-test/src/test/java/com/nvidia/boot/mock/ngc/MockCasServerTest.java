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

package com.nvidia.boot.mock.ngc;

import static com.nvidia.boot.mock.TestConstants.MOCK_NGC_CONTAINER_REGISTRY_URL;
import static com.nvidia.boot.mock.TestConstants.MOCK_NGC_REGISTRY_OAUTH2_BASE_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class MockCasServerTest {

    private static final String TEST_RESPONSE = "{\"test\": \"data\"}";

    @BeforeEach
    void setUp() {
        // Clean up any existing servers
        MockCasServer.stop();
    }

    @AfterEach
    void tearDown() {
        MockCasServer.stop();
    }

    @Test
    void testStartAndStop() {
        // Test that start doesn't throw any exceptions
        assertDoesNotThrow(() -> MockCasServer.start(MOCK_NGC_REGISTRY_OAUTH2_BASE_URL,
                                                     MOCK_NGC_CONTAINER_REGISTRY_URL));
        
        // Test that stop doesn't throw any exceptions
        assertDoesNotThrow(() -> MockCasServer.stop());
    }

    @Test
    void testStartMultipleTimes() {
        // Test that starting multiple times doesn't cause issues
        assertDoesNotThrow(() -> MockCasServer.start(MOCK_NGC_REGISTRY_OAUTH2_BASE_URL,
                                                     MOCK_NGC_CONTAINER_REGISTRY_URL));
        assertDoesNotThrow(() -> MockCasServer.start(MOCK_NGC_REGISTRY_OAUTH2_BASE_URL,
                                                     MOCK_NGC_CONTAINER_REGISTRY_URL));
        assertDoesNotThrow(() -> MockCasServer.stop());
    }

    @Test
    void testStopWithoutStart() {
        // Test that stopping without starting doesn't cause issues
        assertDoesNotThrow(() -> MockCasServer.stop());
    }

    @Test
    void testSetCustomResponse() {
        MockCasServer.start(MOCK_NGC_REGISTRY_OAUTH2_BASE_URL,
                            MOCK_NGC_CONTAINER_REGISTRY_URL);
        
        // Test setting a custom response
        String customUrl = "/custom/test/endpoint";
        byte[] customBody = TEST_RESPONSE.getBytes();
        
        assertDoesNotThrow(() -> MockCasServer.setResponse(customUrl, customBody));
        
        MockCasServer.stop();
    }
}
