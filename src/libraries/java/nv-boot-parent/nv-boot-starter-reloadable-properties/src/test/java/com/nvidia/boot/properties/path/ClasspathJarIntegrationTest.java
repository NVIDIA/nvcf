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

package com.nvidia.boot.properties.path;

import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.ENABLED;
import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.FILE;
import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.POLL_DURATION;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.properties.TestApplication;
import com.nvidia.boot.properties.refresher.FileBasedPropertiesRefresher;
import com.nvidia.boot.properties.util.TestJarUtils;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ActiveProfiles;

/**
 * Verifies that when the properties file is only on classpath (inside a JAR, not on
 * filesystem), properties are loaded correctly. FileBasedPropertiesRefresher is still
 * registered but does not start polling since the file is not a physical file.
 */
@SpringBootTest(classes = TestApplication.class)
@ActiveProfiles("test")
class ClasspathJarIntegrationTest extends PathIntegrationTest {

    @BeforeAll
    static void init() {
        System.setProperty(ENABLED, "true");
        System.setProperty(FILE, "classpath:secrets.json");
        System.setProperty(POLL_DURATION, "300s");
        TestJarUtils.loadJar(TestJarUtils.createJar(
                "src/test/resources/kv_secret_example.json",
                "secrets.json"));
    }

    @AfterAll
    static void cleanUp() {
        System.clearProperty(ENABLED);
        System.clearProperty(FILE);
        System.clearProperty(POLL_DURATION);
    }

    @Test
    void refresherLoaded() {
        assertThat(context.getBeansOfType(FileBasedPropertiesRefresher.class)).isNotEmpty();
    }
}
