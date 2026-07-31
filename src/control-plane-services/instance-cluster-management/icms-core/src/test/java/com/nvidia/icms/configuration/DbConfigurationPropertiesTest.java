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
package com.nvidia.icms.configuration;

import com.nvidia.icms.configuration.bean.DbConfigurationProperties;
import com.nvidia.icms.integration.IntegrationTest;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

public class DbConfigurationPropertiesTest extends IntegrationTest {

    @Autowired
    private DbConfigurationProperties dbConfigurationProperties;

    @Test
    void dbPropertiesTest_success() {
        Assertions.assertEquals(3, dbConfigurationProperties.getQueryDurationMonths());
        Assertions.assertEquals(1, dbConfigurationProperties.getCloudHealthTtlInSec());
    }
}
