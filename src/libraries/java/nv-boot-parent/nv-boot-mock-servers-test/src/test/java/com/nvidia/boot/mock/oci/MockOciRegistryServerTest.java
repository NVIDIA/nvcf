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

package com.nvidia.boot.mock.oci;

import static com.nvidia.boot.mock.TestConstants.MOCK_OCI_REGISTRY_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class MockOciRegistryServerTest {

    MockOciRegistryServer mockOciRegistryServer = new MockOciRegistryServer();

    @BeforeEach
    void setUp() {
        mockOciRegistryServer.stop();
    }

    @AfterEach
    void tearDown() {
        mockOciRegistryServer.stop();
    }

    @Test
    void testStartAndStop() {
        assertDoesNotThrow(() -> mockOciRegistryServer.start(MOCK_OCI_REGISTRY_URL));
        assertDoesNotThrow(() -> mockOciRegistryServer.stop());
    }

    @Test
    void testStartMultipleTimes() {
        assertDoesNotThrow(() -> mockOciRegistryServer.start(MOCK_OCI_REGISTRY_URL));
        assertDoesNotThrow(() -> mockOciRegistryServer.start(MOCK_OCI_REGISTRY_URL));
        assertDoesNotThrow(() -> mockOciRegistryServer.stop());
    }

    @Test
    void testStopWithoutStart() {
        assertDoesNotThrow(() -> mockOciRegistryServer.stop());
    }
}
