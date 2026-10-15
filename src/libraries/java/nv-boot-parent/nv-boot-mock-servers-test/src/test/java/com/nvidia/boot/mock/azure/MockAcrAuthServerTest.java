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

package com.nvidia.boot.mock.azure;

import static com.nvidia.boot.mock.TestConstants.MOCK_ACR_AUTH_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import com.nvidia.boot.mock.docker.MockDockerRegistryAuthServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class MockAcrAuthServerTest {

    private static final String TEST_RESPONSE = "{\"test\": \"data\"}";

    @BeforeEach
    void setUp() {
        MockAcrAuthServer.stop();
    }

    @AfterEach
    void tearDown() {
        MockAcrAuthServer.stop();
    }

    @Test
    void testStartAndStop() {
        assertDoesNotThrow(() -> MockAcrAuthServer.start(MOCK_ACR_AUTH_URL));
        assertDoesNotThrow(MockAcrAuthServer::stop);
    }

    @Test
    void testStartMultipleTimes() {
        assertDoesNotThrow(() -> MockAcrAuthServer.start(MOCK_ACR_AUTH_URL));
        assertDoesNotThrow(() -> MockAcrAuthServer.start(MOCK_ACR_AUTH_URL));
        assertDoesNotThrow(MockAcrAuthServer::stop);
    }

    @Test
    void testStopWithoutStart() {
        assertDoesNotThrow(MockAcrAuthServer::stop);
    }

    @Test
    void testSetCustomResponse() {
        MockDockerRegistryAuthServer.start(MOCK_ACR_AUTH_URL);

        String customUrl = "/custom/test/endpoint";
        byte[] customBody = TEST_RESPONSE.getBytes();

        assertDoesNotThrow(() -> MockDockerRegistryAuthServer.setResponse(customUrl, customBody));

        MockDockerRegistryAuthServer.stop();
    }
}
