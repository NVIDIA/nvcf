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

import java.io.ByteArrayInputStream;
import java.io.InputStream;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import org.junit.jupiter.api.Test;
import org.springframework.boot.SpringApplication;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.mock.env.MockEnvironment;

class BootCoreEnvironmentPostProcessorTest {

    private final BootCoreEnvironmentPostProcessor processor = new BootCoreEnvironmentPostProcessor();

    @Test
    void loadsDefaultPropertiesWhenPresent() {
        var environment = new MockEnvironment();

        processor.postProcessEnvironment(environment, new SpringApplication());

        // nv-boot-core-defaults.properties exists in resources
        assertThat(environment.getProperty("server.shutdown")).isEqualTo("graceful");
        assertThat(environment.getProperty("springdoc.api-docs.path")).isEqualTo("/v3/openapi");
    }

    @Test
    void hasCorrectOrder() {
        assertThat(processor.getOrder()).isEqualTo(Integer.MAX_VALUE - 10);
    }

    @Test
    void usesClosestTagNameAsVersionWhenPresent() {
        var env = processWithGitProperties("""
                git.closest.tag.name=v2.5.0
                git.build.version=2.5.0-SNAPSHOT
                git.commit.id.abbrev=abc1234
                """);

        assertThat(env.getProperty("spring.application.version")).isEqualTo("v2.5.0");
        assertThat(env.getProperty("app.git.tag")).isEqualTo("v2.5.0");
        assertThat(env.getProperty("app.git.commit")).isEqualTo("abc1234");
        assertThat(env.getProperty("app.git.branch")).isNull();
    }

    @Test
    void fallsBackToBuildVersionWhenTagAbsent() {
        var env = processWithGitProperties("""
                git.build.version=2.5.0-SNAPSHOT
                git.commit.id.abbrev=abc1234
                """);

        assertThat(env.getProperty("spring.application.version")).isEqualTo("2.5.0-SNAPSHOT");
        assertThat(env.getProperty("app.git.tag")).isNull();
        assertThat(env.getProperty("app.git.commit")).isEqualTo("abc1234");
        assertThat(env.getProperty("app.git.branch")).isNull();
    }

    @Test
    void fallsBackToCommitIdWhenTagAndBuildVersionAbsent() {
        var env = processWithGitProperties("""
                git.commit.id.abbrev=abc1234
                """);

        assertThat(env.getProperty("spring.application.version")).isEqualTo("abc1234");
        assertThat(env.getProperty("app.git.tag")).isNull();
        assertThat(env.getProperty("app.git.commit")).isEqualTo("abc1234");
        assertThat(env.getProperty("app.git.branch")).isNull();
    }

    @Test
    void fallsBackToUnknownWhenAllVersionFieldsAbsent() {
        var env = processWithGitProperties("""
                git.branch=main
                """);

        assertThat(env.getProperty("spring.application.version")).isEqualTo("unknown");
        assertThat(env.getProperty("app.git.tag")).isNull();
        assertThat(env.getProperty("app.git.commit")).isNull();
        assertThat(env.getProperty("app.git.branch")).isEqualTo("main");
    }

    @Test
    void populatesAllGitMetadataProperties() {
        var env = processWithGitProperties("""
                git.closest.tag.name=v2.5.0
                git.build.version=2.5.0-SNAPSHOT
                git.commit.id.abbrev=abc1234
                git.branch=main
                """);

        assertThat(env.getProperty("app.git.commit")).isEqualTo("abc1234");
        assertThat(env.getProperty("app.git.branch")).isEqualTo("main");
        assertThat(env.getProperty("app.git.tag")).isEqualTo("v2.5.0");
    }

    @Test
    void doesNotOverwriteExistingApplicationVersion() {
        var env = processWithGitProperties("""
                git.closest.tag.name=v2.4.0
                git.commit.id.abbrev=abc1234
                git.branch=main
                """,
                "v2.5.0");

        assertThat(env.getProperty("spring.application.version")).isEqualTo("v2.5.0");
        assertThat(env.getProperty("app.git.commit")).isEqualTo("abc1234");
        assertThat(env.getProperty("app.git.branch")).isEqualTo("main");
        assertThat(env.getProperty("app.git.tag")).isEqualTo("v2.4.0");
    }

    private ConfigurableEnvironment processWithGitProperties(String content) {
        return processWithGitProperties(content, null);
    }

    private ConfigurableEnvironment processWithGitProperties(String content, String existingVersion) {
        var bytes = content.getBytes(StandardCharsets.UTF_8);
        var originalCl = Thread.currentThread().getContextClassLoader();
        try {
            Thread.currentThread().setContextClassLoader(new ClassLoader(originalCl) {
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
            });

            var environment = new MockEnvironment();
            if (existingVersion != null) {
                environment.setProperty("spring.application.version", existingVersion);
            }
            processor.postProcessEnvironment(environment, new SpringApplication());
            return environment;
        } finally {
            Thread.currentThread().setContextClassLoader(originalCl);
        }
    }
}
