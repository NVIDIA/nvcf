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
package com.nvidia.nvcf.service.registry.client.ngc;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.client.ngc.NgcContainerRegistryClient;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import java.time.Duration;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.web.reactive.function.client.WebClient;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class NgcContainerRegistryClientTest {
    private NgcContainerRegistryClient client;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String containerRegistryUrl;

    @Autowired
    private WebClient.Builder webClientBuilder;

    @BeforeEach
    void setUp() {
        client = new NgcContainerRegistryClient(webClientBuilder, containerRegistryUrl,
                                                Duration.ofSeconds(10), Duration.ofSeconds(10),
                                                Duration.ofSeconds(10), Duration.ofSeconds(10));
    }

    @Test
    void parseContainerImageUrl_WithTag_ShouldParseCorrectly() {
        // Given
        String imageUrl = "nvcr.io/nvidia/nvcf/example:latest";

        // When
        NgcContainerRegistryClient.ContainerImageComponents components =
                client.parseContainerImageUrl(imageUrl);

        // Then
        assertNotNull(components);
        assertEquals("nvcr.io", components.registryHost());
        assertEquals("nvidia/nvcf", components.repository());
        assertEquals("example", components.imageName());
        assertEquals("latest", components.tag());
        assertNull(components.digest());
    }

    @Test
    void parseContainerImageUrl_WithDigest_ShouldParseCorrectly() {
        // Given
        String imageUrl = "nvcr.io/nvidia/nvcf/example@sha256:abc123def456";

        // When
        NgcContainerRegistryClient.ContainerImageComponents components =
                client.parseContainerImageUrl(imageUrl);

        // Then
        assertNotNull(components);
        assertEquals("nvcr.io", components.registryHost());
        assertEquals("nvidia/nvcf", components.repository());
        assertEquals("example", components.imageName());
        assertNull(components.tag());
        assertEquals("sha256:abc123def456", components.digest());
    }

    @Test
    void parseContainerImageUrl_WithSingleLevelRepository_ShouldParseCorrectly() {
        // Given
        String imageUrl = "nvcr.io/example:latest";

        // When
        NgcContainerRegistryClient.ContainerImageComponents components =
                client.parseContainerImageUrl(imageUrl);

        // Then
        assertNotNull(components);
        assertEquals("nvcr.io", components.registryHost());
        assertEquals("", components.repository());
        assertEquals("example", components.imageName());
        assertEquals("latest", components.tag());
        assertNull(components.digest());
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty string
            "invalid",  // no slashes
            "nvcr.io",  // only registry
            "nvcr.io/",  // trailing slash
            "nvcr.io/example:",  // empty tag
            "nvcr.io/example@",  // empty digest
            "nvcr.io/example:tag:extra",  // multiple colons
            "nvcr.io/example@digest@extra"  // multiple @ symbols
    })
    void parseContainerImageUrl_WithInvalidFormats_ShouldThrowException(String invalidUrl) {
        // When/Then
        assertThrows(BadRequestException.class, () -> client.parseContainerImageUrl(invalidUrl));
    }
}
