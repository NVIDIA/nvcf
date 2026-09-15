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

package com.nvidia.boot.properties.refresher;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nvidia.boot.properties.property.ReloadablePropertiesConfigurationProvider;
import java.io.File;
import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import org.springframework.cloud.context.refresh.ContextRefresher;
import org.springframework.util.FileCopyUtils;

@ExtendWith(OutputCaptureExtension.class)
class FileBasedPropertiesRefresherTest {

    @Test
    void refreshOnFileChange(CapturedOutput output) throws Exception {
        File tempFile = File.createTempFile("reloadable", ".json");
        tempFile.deleteOnExit();
        FileCopyUtils.copy(new File("src/test/resources/kv_secret_example.json"), tempFile);

        var mockPathProvider = mock(ReloadablePropertiesConfigurationProvider.class);
        var mockRefresher = mock(ContextRefresher.class);

        when(mockPathProvider.getPropertiesFilePath()).thenReturn(tempFile.getAbsolutePath());
        when(mockPathProvider.getDelaySeconds()).thenReturn(2);
        when(mockRefresher.refresh()).thenReturn(java.util.Set.of("test.key"));

        var refresher = new FileBasedPropertiesRefresher(mockRefresher, mockPathProvider);
        refresher.start();

        // Touch file to trigger change detection
        tempFile.setLastModified(System.currentTimeMillis() + 1000);
        refresher.forceTrigger(10_000, TimeUnit.SECONDS);

        assertThat(output.getOut()).contains("refreshed properties successfully");
    }
}
