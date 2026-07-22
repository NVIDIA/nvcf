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

package com.nvidia.boot.properties;

import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.ENABLED;
import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.FILE;
import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.POLL_DURATION;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.boot.properties.refresher.FileBasedPropertiesRefresher;
import java.io.File;
import java.io.FileReader;
import java.io.IOException;
import java.nio.file.Files;
import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.ApplicationContext;
import org.springframework.test.context.ActiveProfiles;
import org.springframework.util.FileCopyUtils;

@SpringBootTest(
        classes = TestApplication.class,
        properties = {
                ENABLED + "=true",
                FILE + "=file:target/test-reloadable.json",
                POLL_DURATION + "=300s"
        })
@ActiveProfiles("test")
abstract class BaseReloadablePropertiesIntegrationTest {

    protected static File tempFile;

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();
    private static final File TEST_RELOADABLE_FILE = new File("target/test-reloadable.json");

    @Autowired
    protected ApplicationContext context;

    @Autowired
    protected TestApplication.TestComponent testComponent;

    @Autowired
    protected FileBasedPropertiesRefresher refresher;

    public static void setUp() throws IOException {
        TEST_RELOADABLE_FILE.getParentFile().mkdirs();
        FileCopyUtils.copy(new File("src/test/resources/kv_secret_example.json"),
                           TEST_RELOADABLE_FILE);
        tempFile = TEST_RELOADABLE_FILE;
    }

    public static void cleanUp() {
        if (tempFile != null && tempFile.exists()) {
            tempFile.delete();
        }
        tempFile = null;
    }

    @Test
    void verifyReload() throws Exception {
        assertThat(testComponent.getUsername()).isEqualTo("myUser");

        JsonNode root = JSON_MAPPER.readTree(tempFile);
        ((ObjectNode) root.get("kv").get("exampleSecretNoHistoryJson")
                .get("current").get("value"))
                .put("username", "myUser2");
        Files.write(tempFile.toPath(), JSON_MAPPER.writerWithDefaultPrettyPrinter()
                .writeValueAsBytes(root));

        refresher.forceTrigger(10_000, TimeUnit.SECONDS);
        assertThat(testComponent.getUsername()).isEqualTo("myUser2");
    }

    @Test
    void propertyValueLoadedCorrectly() {
        assertThat(testComponent.getUsername()).isEqualTo("myUser");
        assertThat(testComponent.getUsernamePrevious()).isEqualTo("myUser");
    }
}
