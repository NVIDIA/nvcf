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

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assumptions.assumeThat;

import java.io.ByteArrayInputStream;
import java.io.InputStream;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.Executors;
import java.util.function.BiFunction;
import org.junit.jupiter.api.Test;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.WebApplicationType;
import org.springframework.cloud.autoconfigure.RefreshAutoConfiguration.RefreshProperties;
import org.springframework.cloud.context.config.ContextRefreshedWithApplicationEvent;
import org.springframework.cloud.context.refresh.ConfigDataContextRefresher;
import org.springframework.cloud.context.refresh.ContextRefresher;
import org.springframework.cloud.context.refresh.LegacyContextRefresher;
import org.springframework.cloud.context.scope.refresh.RefreshScope;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Configuration;

class BootCoreEnvironmentPostProcessorRefreshTest {

    // Same shape as a release build: the build version matches the manifest, and the closest
    // tag is the previous release.
    private static final String GIT_PROPERTIES = """
            git.closest.tag.name=previous-release
            git.build.version=%s
            git.commit.id.abbrev=ef68fe0
            git.commit.id.full=ef68fe00a24234000478bf2c1109b01b5554fa3d
            """;

    @Test
    void versionSurvivesLegacyContextRefresh() throws Exception {
        assertVersionSurvivesRefresh((application, context) ->
                new LegacyContextRefresher(context, new RefreshScope(), new RefreshProperties()));
    }

    @Test
    void versionSurvivesConfigDataContextRefresh() throws Exception {
        assertVersionSurvivesRefresh((application, context) -> {
            var refresher = new ConfigDataContextRefresher(context, new RefreshScope(), new RefreshProperties());
            refresher.onApplicationEvent(new ContextRefreshedWithApplicationEvent(application, new String[0], context));
            return refresher;
        });
    }

    private void assertVersionSurvivesRefresh(
            BiFunction<SpringApplication, ConfigurableApplicationContext, ContextRefresher> refresherFactory)
            throws Exception {
        // Stands in for the application jar's MANIFEST.MF Implementation-Version.
        var manifestVersion = SpringApplication.class.getPackage().getImplementationVersion();
        assumeThat(manifestVersion).isNotBlank();

        var originalCl = Thread.currentThread().getContextClassLoader();
        var gitCl = classLoaderWithGitProperties(originalCl, GIT_PROPERTIES.formatted(manifestVersion));
        Thread.currentThread().setContextClassLoader(gitCl);
        var application = new SpringApplication(TestConfig.class);
        application.setWebApplicationType(WebApplicationType.NONE);
        application.setMainApplicationClass(SpringApplication.class);
        try (var context = application.run(
                "--spring.application.name=test-app", "--spring.profiles.active=refresh")) {
            assertThat(context.getEnvironment().getProperty("spring.application.version"))
                    .as("version at startup")
                    .isEqualTo(manifestVersion);

            var refresher = refresherFactory.apply(application, context);
            // Kubernetes config reload refreshes on a watcher thread, which has no main() frame.
            var executor = Executors.newSingleThreadExecutor();
            try {
                executor.submit(() -> {
                    Thread.currentThread().setContextClassLoader(gitCl);
                    return refresher.refreshEnvironment();
                }).get();
            } finally {
                executor.shutdownNow();
            }

            assertThat(context.getEnvironment().getProperty("spring.application.version"))
                    .as("version after refresh")
                    .isEqualTo(manifestVersion);
        } finally {
            Thread.currentThread().setContextClassLoader(originalCl);
        }
    }

    private static ClassLoader classLoaderWithGitProperties(ClassLoader parent, String gitProperties) {
        var bytes = gitProperties.getBytes(StandardCharsets.UTF_8);
        return new ClassLoader(parent) {
            @Override
            public URL getResource(String name) {
                if ("git.properties".equals(name)) {
                    return Object.class.getResource("Object.class");
                }
                return super.getResource(name);
            }

            @Override
            public InputStream getResourceAsStream(String name) {
                if ("git.properties".equals(name)) {
                    return new ByteArrayInputStream(bytes);
                }
                return super.getResourceAsStream(name);
            }
        };
    }

    @Configuration(proxyBeanMethods = false)
    static class TestConfig {
    }
}
