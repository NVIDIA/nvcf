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

import com.nvidia.boot.properties.property.ReloadablePropertiesConfigurationProvider;
import jakarta.annotation.PostConstruct;
import jakarta.annotation.PreDestroy;
import java.io.File;
import java.util.Set;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.cloud.context.refresh.ContextRefresher;

/**
 * Polls the properties file and triggers Spring Cloud context refresh when it changes.
 */
@Slf4j
@RequiredArgsConstructor
public class FileBasedPropertiesRefresher {

    private static final String FILE_SCHEME = "file:";

    private final ScheduledExecutorService executor = Executors
            .newSingleThreadScheduledExecutor(r -> {
                var t = new Thread(r, "properties-refresher");
                t.setDaemon(true);
                return t;
            });

    private final ContextRefresher contextRefresher;
    private final ReloadablePropertiesConfigurationProvider configurationProvider;

    private volatile long lastModified = 0;

    @PostConstruct
    public void start() {
        var filePath = configurationProvider.getPropertiesFilePath();
        if (StringUtils.isNotBlank(filePath) && filePath.startsWith(FILE_SCHEME)) {
            filePath = filePath.substring(5);
        }
        var delaySeconds = configurationProvider.getDelaySeconds();
        var file = new File(filePath);

        if (file.exists()) {
            lastModified = file.lastModified();
            log.info("Starting file watcher for: {} (polling every {}s)", filePath, delaySeconds);

            // Initial delay is set to delaySeconds as well to avoid first execution
            // happening immediately.
            executor.scheduleWithFixedDelay(
                    this::checkAndRefresh,
                    delaySeconds,
                    delaySeconds,
                    TimeUnit.SECONDS
            );
        } else {
            log.warn("Properties file not found, file watcher disabled: {}", filePath);
        }
    }

    @PreDestroy
    public void stop() {
        log.info("Stopping file watcher");
        executor.shutdown();
        try {
            if (!executor.awaitTermination(5, TimeUnit.SECONDS)) {
                executor.shutdownNow();
            }
        } catch (InterruptedException e) {
            executor.shutdownNow();
            Thread.currentThread().interrupt();
        }
    }

    /**
     * Force trigger for testing.
     */
    public void forceTrigger(long timeout, TimeUnit timeUnit) throws Exception {
        executor.submit(this::checkAndRefresh).get(timeout, timeUnit);
    }

    private void checkAndRefresh() {
        try {
            var filePath = configurationProvider.getPropertiesFilePath();
            if (StringUtils.isNotBlank(filePath) && filePath.startsWith(FILE_SCHEME)) {
                filePath = filePath.substring(FILE_SCHEME.length());
            }
            var file = new File(filePath);
            var currentLastModified = file.lastModified();

            if (currentLastModified > lastModified) {
                log.info("Properties file changed, triggering refresh...");
                Set<String> refreshedKeys = contextRefresher.refresh();
                lastModified = currentLastModified;
                log.info("{} refreshed properties successfully. Updated keys: {}",
                         this.getClass().getSimpleName(), refreshedKeys);
            } else {
                log.info("{} finished without refresh", this.getClass().getSimpleName());
            }
        } catch (Throwable t) {
            log.error("{} error during properties refresh", this.getClass().getSimpleName(), t);
        }
    }
}
