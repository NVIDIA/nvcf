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

package com.nvidia.boot.core.env;

import java.io.IOException;
import java.util.HashMap;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.EnvironmentPostProcessor;
import org.springframework.core.Ordered;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.PropertiesPropertySource;
import org.springframework.core.io.ClassPathResource;
import org.springframework.core.io.support.PropertiesLoaderUtils;

/**
 * Environment post processor that loads default properties and
 * spring.application.version from git.properties when present.
 *
 * <p>Order is:  "git.closest.tag.name" -> "git.build.version" -> "git.commit.id.abbrev"
 *
 *
 * <p>Logback configuration has been moved to nv-boot-starter-observability.
 */
@Slf4j
public class BootCoreEnvironmentPostProcessor implements EnvironmentPostProcessor, Ordered {

    private static final String CORE_DEFAULTS_FILE = "nv-boot-core-defaults.properties";
    private static final String GIT_PROPERTIES_FILE = "git.properties";

    @Override
    public void postProcessEnvironment(
            ConfigurableEnvironment environment,
            SpringApplication application) {
        loadDefaultProperties(environment);
        loadAppVersionFromGitProperties(environment);
    }

    private void loadDefaultProperties(ConfigurableEnvironment environment) {
        loadPropertiesFile(environment, CORE_DEFAULTS_FILE, "nv-boot-core-defaults");
    }

    private void loadPropertiesFile(
            ConfigurableEnvironment environment,
            String filename,
            String sourceName) {
        try {
            var resource = new ClassPathResource(filename);
            if (resource.exists()) {
                var properties = PropertiesLoaderUtils.loadProperties(resource);
                environment.getPropertySources().addLast(
                        new PropertiesPropertySource(sourceName, properties)
                );
            }
        } catch (IOException e) {
            // Log warning but don't fail startup.
            log.warn("Failed to load properties file '{}'", filename);
        }
    }

    private void loadAppVersionFromGitProperties(ConfigurableEnvironment environment) {
        try {
            var resource = new ClassPathResource(GIT_PROPERTIES_FILE);
            if (resource.exists()) {
                var gitProperties = PropertiesLoaderUtils.loadProperties(resource);
                var versionProps = new HashMap<String, Object>();

                // Only set spring.application.version from git when not already present.
                // Spring Boot 3.4+ populates it from MANIFEST.MF via ApplicationInfoPropertySource
                // (https://github.com/spring-projects/spring-boot/commit/f4b4f4f).
                // moveToEnd() seems to be called after this post-processor runs, so ApplicationInfoPropertySource never overrides this
                // endresult is always [..., "nv-boot-git-properties", "applicationInfo", "..."]
                var existingVersion = environment.getProperty("spring.application.version");
                if (StringUtils.isBlank(existingVersion)) {
                    var version = gitProperties.getProperty("git.closest.tag.name");

                    if (StringUtils.isBlank(version)) {
                        version = gitProperties.getProperty("git.build.version");
                    }

                    if (StringUtils.isBlank(version)) {
                        version = gitProperties.getProperty("git.commit.id.abbrev", "unknown");
                    }

                    versionProps.put("spring.application.version", version);
                }

                var commitId = gitProperties.getProperty("git.commit.id.abbrev");
                if (StringUtils.isNotBlank(commitId)) {
                    versionProps.put("app.git.commit", commitId);
                }

                var branch = gitProperties.getProperty("git.branch");
                if (StringUtils.isNotBlank(branch)) {
                    versionProps.put("app.git.branch", branch);
                }

                var tag = gitProperties.getProperty("git.closest.tag.name");
                if (StringUtils.isNotBlank(tag)) {
                    versionProps.put("app.git.tag", tag);
                }

                if (!versionProps.isEmpty()) {
                    environment.getPropertySources().addLast(
                            new MapPropertySource("nv-boot-git-properties", versionProps)
                    );
                }
            }
        } catch (IOException e) {
            // git.properties not available - this is fine
        }
    }

    @Override
    public int getOrder() {
        return Ordered.LOWEST_PRECEDENCE - 10;
    }
}
