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
package com.nvidia.icms.configuration.bean;

import static org.junit.jupiter.api.Assertions.assertEquals;

import com.nvidia.icms.integration.IntegrationTest;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

class InstanceTypeConfigurationPropertiesTest extends IntegrationTest {

    @Autowired
    private InstanceTypeConfigurationProperties properties;

    @Test
    void testInstanceTypeGpuCountMapping() {
        // Single GPU instance type
        assertEquals(1, properties.getGpuCountForInstanceType("dummy-single-gpu.large"));
        // Dual GPU instance type
        assertEquals(2, properties.getGpuCountForInstanceType("dummy-dual-gpu.xlarge"));

        // Unknown instance type should return default
        assertEquals(1, properties.getGpuCountForInstanceType("unknown.instance"));
    }
}
