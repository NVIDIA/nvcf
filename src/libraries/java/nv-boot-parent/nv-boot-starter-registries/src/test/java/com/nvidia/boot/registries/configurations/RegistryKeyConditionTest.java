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

package com.nvidia.boot.registries.configurations;

import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_KEY;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.when;

import java.time.Duration;
import java.util.HashMap;
import java.util.Map;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.config.ConfigurableListableBeanFactory;
import org.springframework.boot.autoconfigure.condition.ConditionOutcome;
import org.springframework.context.annotation.ConditionContext;
import org.springframework.core.env.Environment;
import org.springframework.core.type.AnnotatedTypeMetadata;

@ExtendWith(MockitoExtension.class)
class RegistryKeyConditionTest {

    private static final String TEST_CONFIG_PREFIX = "test.registries";

    @Mock
    private ConditionContext context;

    @Mock
    private AnnotatedTypeMetadata metadata;

    @Mock
    private ConfigurableListableBeanFactory beanFactory;

    @Mock
    private RegistryConfigPathProvider configPathProvider;

    @Mock
    private Environment environment;

    private RegistryKeyCondition registryKeyCondition;
    private RegistryConfigurationProperties registryProps;

    @BeforeEach
    void setUp() {
        registryKeyCondition = new RegistryKeyCondition();
        registryProps = new RegistryConfigurationProperties();

        // Mock BeanFactory and Environment
        lenient().when(context.getBeanFactory()).thenReturn(beanFactory);
        lenient().when(context.getEnvironment()).thenReturn(environment);

        // Mock RegistryConfigPathProvider setup
        lenient().when(beanFactory.getBeanNamesForType(
                        RegistryConfigPathProvider.class, false, false))
                .thenReturn(new String[]{"registryConfigPathProvider"});
        lenient().when(beanFactory.getBean(RegistryConfigPathProvider.class))
                .thenReturn(configPathProvider);
        lenient().when(configPathProvider.getConfigPath())
                .thenReturn(TEST_CONFIG_PREFIX);

        // Keep existing bean setup for backwards compatibility
        lenient().when(beanFactory.getBean(RegistryConfigurationProperties.class))
                .thenReturn(registryProps);
    }

    @Test
    @DisplayName("Should return no match when annotation is not found")
    void shouldReturnNoMatchWhenAnnotationNotFound() {
        // Given
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(null);

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isFalse();
        assertThat(result.getMessage()).isEqualTo("No @ConditionalOnRegistryKey annotation found");
    }

    @Test
    @DisplayName("Should return no match when RegistryConfigPathProvider is not found")
    void shouldReturnNoMatchWhenProviderNotFound() {
        // Given
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes("container", NGC_PRIVATE_REGISTRY_KEY, true);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);
        when(beanFactory.getBeanNamesForType(RegistryConfigPathProvider.class, false, false))
                .thenReturn(new String[]{});

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isFalse();
        assertThat(result.getMessage()).contains("RegistryConfigPathProvider bean not found");
    }

    @Test
    @DisplayName("Should return no match for unknown registry types")
    void shouldReturnNoMatchForUnknownRegistryType() {
        // Given
        var registryType = "invalid";
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes(registryType, NGC_PRIVATE_REGISTRY_KEY, true);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);

        setupRecognizedRegistries();

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isFalse();
        assertThat(result.getMessage()).contains("(no property found at");
    }

    @ParameterizedTest
    @MethodSource("provideValidRegistryTypes")
    @DisplayName("Should return no match when registry key is not found")
    void shouldReturnNoMatchWhenRegistryKeyNotFound(String registryType) {
        // Given
        String unknownKey = "unknown-registry";
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes(registryType, unknownKey, true);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);

        setupRecognizedRegistries();

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isFalse();
        assertThat(result.getMessage()).contains(
                "Registry key '" + unknownKey + "' not found in " + registryType + " registries");
    }

    @ParameterizedTest
    @MethodSource("provideValidRegistryTypes")
    @DisplayName("Should return no match when hostname is required but not configured")
    void shouldReturnNoMatchWhenHostnameRequiredButNotConfigured(String registryType) {
        // Given
        String key = NGC_PRIVATE_REGISTRY_KEY;
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes(registryType, key, true);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);

        setupRecognizedRegistriesWithoutHostname();

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isFalse();
        assertThat(result.getMessage()).isEqualTo(
                "Registry key '" + key + "' found but hostname is not configured");
    }

    @ParameterizedTest
    @MethodSource("provideValidRegistryTypes")
    @DisplayName("Should return match when registry key is found with hostname configured")
    void shouldReturnMatchWhenRegistryKeyFoundWithHostname(String registryType) {
        // Given
        String key = NGC_PRIVATE_REGISTRY_KEY;
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes(registryType, key, true);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);

        setupRecognizedRegistries();

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isTrue();
        assertThat(result.getMessage()).isEqualTo(
                "Registry key '" + key + "' found in " + registryType +
                        " registries with hostname configured");
    }

    @ParameterizedTest
    @MethodSource("provideValidRegistryTypes")
    @DisplayName("Should return match when registry key is found and hostname not required")
    void shouldReturnMatchWhenRegistryKeyFoundAndHostnameNotRequired(String registryType) {
        // Given
        String key = NGC_PRIVATE_REGISTRY_KEY;
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes(registryType, key, false);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);

        setupRecognizedRegistriesWithoutHostname();

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isTrue();
        assertThat(result.getMessage()).isEqualTo(
                "Registry key '" + key + "' found in " + registryType + " registries");
    }

    @Test
    @DisplayName("Should handle exception when getting registry configuration provider")
    void shouldHandleExceptionWhenGettingRegistryBean() {
        // Given
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes("container", NGC_PRIVATE_REGISTRY_KEY, true);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);

        when(beanFactory.getBean(RegistryConfigPathProvider.class))
                .thenThrow(new RuntimeException("Bean not found"));

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isFalse();
        assertThat(result.getMessage()).contains("Failed to get RegistryConfigPathProvider");
    }

    @Test
    @DisplayName("Should handle case-insensitive registry types")
    void shouldHandleCaseInsensitiveRegistryTypes() {
        // Given
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes("CONTAINER", NGC_PRIVATE_REGISTRY_KEY, true);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);

        setupRecognizedRegistries();

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isTrue();
        assertThat(result.getMessage()).contains("CONTAINER");
    }

    @ParameterizedTest
    @MethodSource("provideComplexRegistryScenarios")
    @DisplayName("Should handle complex registry configuration scenarios")
    void shouldHandleComplexRegistryScenarios(String registryType, String key,
                                              boolean requireHostname, boolean hasHostname,
                                              boolean expectedMatch) {
        // Given
        Map<String, Object> annotationAttributes =
                createAnnotationAttributes(registryType, key, requireHostname);
        when(metadata.getAnnotationAttributes(ConditionalOnRegistryKey.class.getName()))
                .thenReturn(annotationAttributes);

        if (hasHostname) {
            setupRecognizedRegistries();
        } else {
            setupRecognizedRegistriesWithoutHostname();
        }

        // When
        ConditionOutcome result = registryKeyCondition.getMatchOutcome(context, metadata);

        // Then
        assertThat(result.isMatch()).isEqualTo(expectedMatch);
    }

    private static Stream<Arguments> provideValidRegistryTypes() {
        return Stream.of(
                Arguments.of("container"),
                Arguments.of("model"),
                Arguments.of("resource"),
                Arguments.of("helm"));
    }

    private static Stream<Arguments> provideComplexRegistryScenarios() {
        return Stream.of(
                // registryType, key, requireHostname, hasHostname, expectedMatch
                Arguments.of("container", NGC_PRIVATE_REGISTRY_KEY, true, true, true),
                Arguments.of("container", NGC_PRIVATE_REGISTRY_KEY, true, false, false),
                Arguments.of("container", NGC_PRIVATE_REGISTRY_KEY, false, false, true),
                Arguments.of("container", NGC_PRIVATE_REGISTRY_KEY, false, true, true),
                Arguments.of("model", "custom", true, true, true),
                Arguments.of("resource", "custom", false, false, true),
                Arguments.of("helm", "custom", true, false, false));
    }

    private Map<String, Object> createAnnotationAttributes(String registryType, String key,
                                                           boolean requireHostname) {
        Map<String, Object> attributes = new HashMap<>();
        attributes.put("registryType", registryType);
        attributes.put("key", key);
        attributes.put("requireHostname", requireHostname);
        return attributes;
    }

    private void setupRecognizedRegistries() {
        // Setup environment properties for container registries
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.container." +
                NGC_PRIVATE_REGISTRY_KEY + ".hostname")))
                .thenReturn("nvcr.io");
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.container.custom.hostname")))
                .thenReturn("custom.registry.com");

        // Setup environment properties for model registries
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.model." +
                NGC_PRIVATE_REGISTRY_KEY + ".hostname")))
                .thenReturn("api.ngc.nvidia.com");
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.model.custom.hostname")))
                .thenReturn("custom.model.com");

        // Setup environment properties for resource registries
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.resource." +
                NGC_PRIVATE_REGISTRY_KEY + ".hostname")))
                .thenReturn("api.ngc.nvidia.com");
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.resource.custom.hostname")))
                .thenReturn("custom.resource.com");

        // Setup environment properties for helm registries
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.helm." +
                NGC_PRIVATE_REGISTRY_KEY + ".hostname")))
                .thenReturn("helm.ngc.nvidia.com");
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.helm.custom.hostname")))
                .thenReturn("custom.helm.com");
    }

    private void setupRecognizedRegistriesWithoutHostname() {
        // Setup environment properties without hostnames (empty strings)
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.container." +
                NGC_PRIVATE_REGISTRY_KEY + ".hostname")))
                .thenReturn("");
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.container.custom.hostname")))
                .thenReturn("");

        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.model." +
                NGC_PRIVATE_REGISTRY_KEY + ".hostname")))
                .thenReturn("");
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.model.custom.hostname")))
                .thenReturn("");

        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.resource." +
                NGC_PRIVATE_REGISTRY_KEY + ".hostname")))
                .thenReturn("");
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.resource.custom.hostname")))
                .thenReturn("");

        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.helm." +
                NGC_PRIVATE_REGISTRY_KEY + ".hostname")))
                .thenReturn("");
        lenient().when(environment.getProperty(eq(TEST_CONFIG_PREFIX + ".recognized.helm.custom.hostname")))
                .thenReturn("");
    }

    private RegistryConfigurationProperties.RegistryConfiguration createRegistryConfig(String name,
                                                                                       String hostname) {
        RegistryConfigurationProperties.RegistryConfiguration config =
                new RegistryConfigurationProperties.RegistryConfiguration();
        config.setName(name);
        config.setHostname(hostname);
        config.setCallTimeout(Duration.parse("PT10S"));

        if (hostname != null && hostname.contains(NGC_PRIVATE_REGISTRY_KEY)) {
            RegistryConfigurationProperties.OAuth2Configuration oauth2 =
                    new RegistryConfigurationProperties.OAuth2Configuration();
            oauth2.setBaseUrl("https://authn.nvidia.com");
            oauth2.setGroupScope(NGC_PRIVATE_REGISTRY_KEY);
            config.setOauth2(oauth2);
        }

        return config;
    }
}
