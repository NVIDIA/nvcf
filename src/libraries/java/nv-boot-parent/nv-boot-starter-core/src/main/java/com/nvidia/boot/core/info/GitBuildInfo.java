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

package com.nvidia.boot.core.info;

import java.io.IOException;
import java.util.Properties;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.core.io.ClassPathResource;
import org.springframework.core.io.support.PropertiesLoaderUtils;

/**
 * Reads {@code git.properties} directly off the classpath for {@code GET /info}, rather than
 * from the {@link org.springframework.core.env.Environment}. This avoids depending on
 * {@code BootCoreEnvironmentPostProcessor}'s property-source ordering relative to Spring Boot's
 * {@code ApplicationInfoPropertySource} (see that class for details).
 *
 * <p>Fallback order for version matches {@code BootCoreEnvironmentPostProcessor}:
 * "git.closest.tag.name" -&gt; "git.commit.id.abbrev" -&gt; "unknown".
 */
@Slf4j
public class GitBuildInfo {

    private static final String GIT_PROPERTIES_FILE = "git.properties";
    private static final String UNKNOWN = "unknown";

    private final String version;
    private final String commit;

    public GitBuildInfo() {
        this(loadGitProperties());
    }

    GitBuildInfo(Properties gitProperties) {
        this.version = resolveVersion(gitProperties);
        this.commit = StringUtils.defaultIfBlank(
                gitProperties.getProperty("git.commit.id.full"), UNKNOWN);
    }

    public String version() {
        return version;
    }

    public String commit() {
        return commit;
    }

    private static String resolveVersion(Properties gitProperties) {
        var version = gitProperties.getProperty("git.closest.tag.name");
        if (StringUtils.isBlank(version)) {
            version = gitProperties.getProperty("git.commit.id.abbrev");
        }
        return StringUtils.isNotBlank(version) ? version : UNKNOWN;
    }

    private static Properties loadGitProperties() {
        try {
            var resource = new ClassPathResource(GIT_PROPERTIES_FILE);
            if (resource.exists()) {
                return PropertiesLoaderUtils.loadProperties(resource);
            }
        } catch (IOException e) {
            log.warn("Failed to load '{}'", GIT_PROPERTIES_FILE, e);
        }
        return new Properties();
    }
}
