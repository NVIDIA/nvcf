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

package com.nvidia.boot.properties.property;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.assertj.core.api.SoftAssertions.assertSoftly;

import java.io.File;
import java.util.Arrays;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.core.io.Resource;
import org.springframework.core.io.support.PathMatchingResourcePatternResolver;
import org.springframework.util.FileCopyUtils;

class YamlPropertySourceFactoryTest {

    private static final String EXPECTED_USERNAME = "myUser";
    private static final String EXPECTED_PASSWORD = "myPass";
    private static final String EXPECTED_TEST_VALUE = "myTestValue";

    private final YamlPropertySourceFactory factory = new YamlPropertySourceFactory();

    @Test
    void createsPropertySourceFromJsonFile() {
        var path = "src/test/resources/kv_secret_example.json";
        var source = factory.createPropertySource("test", path);

        assertThat(source).isNotNull();
        assertThat(source.getProperty("kv.testValue.current.value")).isEqualTo(EXPECTED_TEST_VALUE);
        assertThat(source.getProperty("kv.exampleSecretNoHistoryJson.current.value.username"))
                .isEqualTo(EXPECTED_USERNAME);
    }

    @Test
    void createsFileBasedPropertySourceForPhysicalFile() {
        var path = "src/test/resources/kv_secret_example.json";
        var source = factory.createPropertySource("test", path);

        assertThat(source).isInstanceOf(FileBasedMapPropertySource.class);
        var fileBased = (FileBasedMapPropertySource) source;
        assertThat(fileBased.getFile()).exists();
        assertThat(fileBased.getLastModified()).isPositive();
    }

    @Test
    void createsPropertySourceFromFileAbsolutePath(@TempDir File tempDir) throws Exception {
        var sourceFile = new File(tempDir, "secrets.json");
        FileCopyUtils.copy(new File("src/test/resources/kv_secret_example.json"), sourceFile);

        var path = "file:" + sourceFile.getAbsolutePath();
        var source = factory.createPropertySource("test", path);

        assertThat(source).isNotNull();
        assertThat(source.getProperty("kv.testValue.current.value")).isEqualTo(EXPECTED_TEST_VALUE);
        assertThat(source.getProperty("kv.exampleSecretNoHistoryJson.current.value.username"))
                .isEqualTo(EXPECTED_USERNAME);
    }

    @Test
    void createsPropertySourceFromFileUriWithThreeSlashes(@TempDir File tempDir) throws Exception {
        var sourceFile = new File(tempDir, "secrets.json");
        FileCopyUtils.copy(new File("src/test/resources/kv_secret_example.json"), sourceFile);

        var path = "file://" + sourceFile.getAbsolutePath();
        var source = factory.createPropertySource("test", path);

        assertThat(source).isNotNull();
        assertThat(source.getProperty("kv.testValue.current.value")).isEqualTo(EXPECTED_TEST_VALUE);
        assertThat(source.getProperty("kv.exampleSecretNoHistoryJson.current.value.username"))
                .isEqualTo(EXPECTED_USERNAME);
    }

    @Test
    void createsPropertySourceFromFileUriWithLocalhost(@TempDir File tempDir) throws Exception {
        var sourceFile = new File(tempDir, "secrets.json");
        FileCopyUtils.copy(new File("src/test/resources/kv_secret_example.json"), sourceFile);

        var path = "file://localhost" + sourceFile.getAbsolutePath();
        var source = factory.createPropertySource("test", path);

        assertThat(source).isNotNull();
        assertThat(source.getProperty("kv.testValue.current.value")).isEqualTo(EXPECTED_TEST_VALUE);
        assertThat(source.getProperty("kv.exampleSecretNoHistoryJson.current.value.username"))
                .isEqualTo(EXPECTED_USERNAME);
    }

    @Test
    void createsPropertySourceFromFileRelativePath() {
        var path = "file:src/test/resources/kv_secret_example.json";
        var source = factory.createPropertySource("test", path);

        assertThat(source).isNotNull();
        assertThat(source.getProperty("kv.testValue.current.value")).isEqualTo(EXPECTED_TEST_VALUE);
        assertThat(source.getProperty("kv.exampleSecretNoHistoryJson.current.value.username"))
                .isEqualTo(EXPECTED_USERNAME);
    }

    @Test
    void createsPropertySourceFromClasspath() {
        var path = "classpath:kv_secret_example.json";
        var source = factory.createPropertySource("test", path);

        assertThat(source).isNotNull();
        assertThat(source.getProperty("kv.testValue.current.value")).isEqualTo(EXPECTED_TEST_VALUE);
        assertThat(source.getProperty("kv.exampleSecretNoHistoryJson.current.value.username"))
                .isEqualTo(EXPECTED_USERNAME);
    }

    @Test
    void createsPropertySourceFromRelativePathWithoutPrefix() {
        var path = "src/test/resources/kv_secret_example.json";
        var source = factory.createPropertySource("test", path);

        assertThat(source).isNotNull();
        assertThat(source.getProperty("kv.testValue.current.value")).isEqualTo(EXPECTED_TEST_VALUE);
        assertThat(source.getProperty("kv.exampleSecretNoHistoryJson.current.value.username"))
                .isEqualTo(EXPECTED_USERNAME);
    }

    @Test
    void throwsForInvalidPath() {
        assertThatThrownBy(() -> factory.createPropertySource("test", "nonexistent.json"))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("Failed to create property source");
    }

    // --- Tests using validator fixtures ---

    @ParameterizedTest
    @MethodSource("wellFormedPropertySourcesArguments")
    void wellFormedPropertySources(String absolutePath) {
        var source = factory.createPropertySource("test", "file:" + absolutePath);

        assertSoftly(assertions -> {
            assertions.assertThat(source.getProperty("kv.exampleSecret.current.value.username"))
                    .isEqualTo(EXPECTED_USERNAME);
            assertions.assertThat(source.getProperty("kv.exampleSecret.current.value.password"))
                    .isEqualTo(EXPECTED_PASSWORD);
        });
    }

    @ParameterizedTest
    @MethodSource("wellFormedPropertySourcesArguments")
    void wellFormedPropertySourcesCreatesFileBasedPropertySource(String absolutePath) {
        var file = new File(absolutePath);
        var source = factory.createPropertySource("test", "file:" + absolutePath);

        assertSoftly(assertions -> {
            assertions.assertThat(source).isInstanceOf(FileBasedMapPropertySource.class);
            var fileBased = (FileBasedMapPropertySource) source;
            assertions.assertThat(fileBased.getFile()).isEqualTo(file);
            assertions.assertThat(fileBased.getLastModified()).isEqualTo(file.lastModified());
        });
    }

    @ParameterizedTest
    @MethodSource("malformedPropertySourcesArguments")
    void malformedPropertySources(String absolutePath) {
        assertThatThrownBy(() -> factory.createPropertySource("test", "file:" + absolutePath))
                .isInstanceOf(IllegalArgumentException.class);
    }

    @ParameterizedTest
    @MethodSource("emptyPropertySourcesArguments")
    void emptyPropertySources(String absolutePath) {
        assertThatThrownBy(() -> factory.createPropertySource("test", "file:" + absolutePath))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("empty");
    }

    private static Stream<Arguments> wellFormedPropertySourcesArguments() throws Exception {
        return fetchFilePaths("classpath*:fixtures/validator/yaml/valid/*");
    }

    private static Stream<Arguments> malformedPropertySourcesArguments() throws Exception {
        return fetchFilePaths("classpath*:fixtures/validator/yaml/invalid/*");
    }

    private static Stream<Arguments> emptyPropertySourcesArguments() throws Exception {
        return fetchFilePaths("classpath*:fixtures/validator/yaml/empty/*");
    }

    // classpath*: searches all classpath roots (every JAR and directory); classpath: would stop
    // at the first match. Using classpath*: ensures we find all fixture files when the same path
    // exists in multiple roots, or when the classpath structure varies (e.g. IDE vs Maven).
    private static Stream<Arguments> fetchFilePaths(String path) throws Exception {
        var resolver = new PathMatchingResourcePatternResolver();
        Resource[] resources = resolver.getResources(path);
        return Arrays.stream(resources)
                .filter(Resource::isFile)
                .map(r -> {
                    try {
                        return r.getFile().getAbsolutePath();
                    } catch (Exception e) {
                        throw new RuntimeException(e);
                    }
                })
                .map(Arguments::of);
    }
}
