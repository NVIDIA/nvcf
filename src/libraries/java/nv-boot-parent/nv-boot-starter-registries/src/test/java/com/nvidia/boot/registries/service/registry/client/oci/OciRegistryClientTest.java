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

package com.nvidia.boot.registries.service.registry.client.oci;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.registries.service.registry.client.oci.OciRegistryClient.IMAGE_MEDIA_TYPES;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciArtifactComponents;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciAuthToken;
import java.net.URI;
import java.time.Duration;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.web.reactive.function.client.WebClient;

@ExtendWith(MockitoExtension.class)
class OciRegistryClientTest {

    private static final String VALID_DIGEST =
            "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef";

    @Mock
    private OciRegistryStubService mockRegistryStubService;

    @Mock
    private OciRegistryAuthClient mockOciRegistryAuthClient;

    // Create a concrete test implementation of the abstract OciRegistryClient
    private static class TestOciRegistryClient extends OciRegistryClient {

        public TestOciRegistryClient(Duration callTimeout, OciRegistryAuthClient authClient) {
            super(WebClientUtils.builder(), callTimeout, authClient);
        }

        @Override
        protected String getRegistryBaseUrl(String registryHost) {
            return "https://" + registryHost;
        }
    }

    @Test
    void ociRegistryClient_WithAuthService_CanBeCreated() {
        var client = new TestOciRegistryClient(Duration.ofSeconds(30), mockOciRegistryAuthClient);

        assertNotNull(client);
        assertEquals("https://registry.io", client.getRegistryBaseUrl("registry.io"));
    }

    static Stream<Arguments> validValidateArtifactScenarios() {
        return Stream.of(
                Arguments.of("registry.io/test/image:v1.0.0",
                             "https://registry.io/v2/test/image/manifests/v1.0.0"),
                Arguments.of("registry.io/test/image@" + VALID_DIGEST,
                             "https://registry.io/v2/test/image/manifests/" + VALID_DIGEST),
                Arguments.of("registry.io/test/image",
                             "https://registry.io/v2/test/image/manifests/latest")
        );
    }

    @ParameterizedTest
    @MethodSource("validValidateArtifactScenarios")
    void validateArtifact_WithValidInput_CompletesSuccessfully(String imageUrl,
                                                               String expectedManifestUrl)
            throws Exception {
        var client = new TestOciRegistryClient(Duration.ofSeconds(30), mockOciRegistryAuthClient);
        injectMockField(client, "ociRegistryStubService", mockRegistryStubService);

        String base64Secret = "base64secret";

        when(mockOciRegistryAuthClient.getToken(any(OciArtifactComponents.class),
                                                any(String.class)))
                .thenReturn(new OciAuthToken("test-token", Duration.ofSeconds(3600)));

        doNothing().when(mockRegistryStubService).doesManifestExist(
                any(URI.class),
                eq("Bearer test-token"),
                eq(IMAGE_MEDIA_TYPES)
        );

        assertDoesNotThrow(() -> {
            client.validateArtifact(imageUrl, base64Secret);
        });

        verify(mockRegistryStubService).doesManifestExist(
                URI.create(expectedManifestUrl),
                "Bearer test-token",
                IMAGE_MEDIA_TYPES
        );
    }

    @ParameterizedTest
    @ValueSource(strings = {"invalid-url", "", "image:tag", "registry.io", "registry.io/"})
    void validateArtifact_WithInvalidUrl_ThrowsBadRequestException(String invalidUrl)
            throws Exception {
        var client = new TestOciRegistryClient(Duration.ofSeconds(30), mockOciRegistryAuthClient);
        injectMockField(client, "ociRegistryStubService", mockRegistryStubService);

        String base64Secret = "base64secret";

        assertThrows(BadRequestException.class, () -> {
            client.validateArtifact(invalidUrl, base64Secret);
        });
    }

    @Test
    void validateArtifact_WithNullUrl_ThrowsBadRequestException() throws Exception {
        var client = new TestOciRegistryClient(Duration.ofSeconds(30), mockOciRegistryAuthClient);
        injectMockField(client, "ociRegistryStubService", mockRegistryStubService);

        String base64Secret = "base64secret";

        assertThrows(BadRequestException.class, () -> {
            client.validateArtifact(null, base64Secret);
        });
    }

    static Stream<Arguments> mediaTypeScenarios() {
        return Stream.of(
                Arguments.of("application/vnd.oci.image.manifest.v1+json",
                             "application/vnd.oci.image.manifest.v1+json"),
                Arguments.of(null, IMAGE_MEDIA_TYPES),
                Arguments.of("", IMAGE_MEDIA_TYPES)
        );
    }

    @ParameterizedTest
    @MethodSource("mediaTypeScenarios")
    void validateArtifact_WithMediaTypes_CompletesSuccessfully(String inputMediaTypes,
                                                               String expectedMediaTypes)
            throws Exception {
        var client = new TestOciRegistryClient(Duration.ofSeconds(30), mockOciRegistryAuthClient);
        injectMockField(client, "ociRegistryStubService", mockRegistryStubService);

        String imageUrl = "registry.io/test/image:v1.0.0";
        String base64Secret = "base64secret";

        when(mockOciRegistryAuthClient.getToken(any(OciArtifactComponents.class),
                                                any(String.class)))
                .thenReturn(new OciAuthToken("test-token", Duration.ofSeconds(3600)));

        doNothing().when(mockRegistryStubService).doesManifestExist(
                any(URI.class),
                eq("Bearer test-token"),
                eq(expectedMediaTypes)
        );

        assertDoesNotThrow(() -> {
            client.validateArtifact(imageUrl, base64Secret, inputMediaTypes);
        });

        verify(mockRegistryStubService).doesManifestExist(
                URI.create("https://registry.io/v2/test/image/manifests/v1.0.0"),
                "Bearer test-token",
                expectedMediaTypes
        );
    }

    // ===== UTILITY TESTS =====

    static Stream<Arguments> validArtifactUrls() {
        return Stream.of(
                // Format: (url, expectedHost, expectedName, expectedReference)
                Arguments.of("registry.io/image:tag", "registry.io", "image", "tag"),
                Arguments.of("registry.io/namespace/image:v1.0.0", "registry.io", "namespace/image",
                             "v1.0.0"),
                Arguments.of("registry.io/image@" + VALID_DIGEST, "registry.io", "image",
                             VALID_DIGEST),
                Arguments.of("oci://registry.io/image:tag", "registry.io", "image", "tag"),
                Arguments.of("oci://registry.io/image@" + VALID_DIGEST, "registry.io", "image",
                             VALID_DIGEST),
                Arguments.of("registry.io/image", "registry.io", "image", "latest"),
                // defaults to latest
                Arguments.of("example.com/deep/nested/path/image:stable", "example.com",
                             "deep/nested/path/image", "stable"),
                Arguments.of("example-test-1.com/deep/nested/path/image:1.0.0",
                             "example-test-1.com", "deep/nested/path/image", "1.0.0")
        );
    }

    @ParameterizedTest
    @MethodSource("validArtifactUrls")
    void parseArtifactUrl_WithValidUrls_Success(
            String url,
            String expectedRegistry,
            String expectedName,
            String expectedReference) {
        var components = OciRegistryClient.parseArtifactUrl(url);

        assertNotNull(components);
        assertEquals(expectedRegistry, components.registryHost());
        assertEquals(expectedName, components.name());
        assertEquals(expectedReference, components.reference());
    }

    @Test
    void parseArtifactUrl_WithComplexNamespace_Success() {
        String complexUrl = "my-registry.example.com:8080/org/team/project/service:v1.2.3-alpha";

        var components = OciRegistryClient.parseArtifactUrl(complexUrl);

        assertEquals("my-registry.example.com:8080", components.registryHost());
        assertEquals("org/team/project/service", components.name());
        assertEquals("v1.2.3-alpha", components.reference());
    }

    @Test
    void parseArtifactUrl_WithOciPrefixAndComplexPath_Success() {
        String ociUrl = "oci://harbor.company.com/library/alpine/base:3.14";

        var components = OciRegistryClient.parseArtifactUrl(ociUrl);

        assertEquals("harbor.company.com", components.registryHost());
        assertEquals("library/alpine/base", components.name());
        assertEquals("3.14", components.reference());
    }

    @Test
    void parseArtifactUrl_WithDigest_ReturnsDigestAsReference() {
        String digestUrl = "registry.io/myimage@" + VALID_DIGEST;

        var components = OciRegistryClient.parseArtifactUrl(digestUrl);

        assertEquals("registry.io", components.registryHost());
        assertEquals("myimage", components.name());
        assertEquals(VALID_DIGEST, components.reference());
    }

    @Test
    void parseArtifactUrl_WithoutTagOrDigest_DefaultsToLatest() {
        String simpleUrl = "registry.io/myimage";

        var components = OciRegistryClient.parseArtifactUrl(simpleUrl);

        assertEquals("registry.io", components.registryHost());
        assertEquals("myimage", components.name());
        assertEquals("latest", components.reference());
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",
            "image:tag", // missing registry host
            "oci://image:tag", // missing registry host  
            "registry.io", // missing image name
            "registry.io/", // missing image name
            "registry.io/image@invalid-digest", // invalid digest format
    })
    void parseArtifactUrl_WithInvalidUrls_ThrowsBadRequestException(String invalidUrl) {
        assertThrows(BadRequestException.class,
                     () -> OciRegistryClient.parseArtifactUrl(invalidUrl));
    }

    @Test
    void parseArtifactUrl_WithNullUrl_ThrowsBadRequestException() {
        assertThrows(BadRequestException.class, () -> OciRegistryClient.parseArtifactUrl(null));
    }

    @Test
    void getOciRegistryWebClient_WithValidServiceType_ReturnsClient() {
        Duration timeout = Duration.ofSeconds(30);
        var client = OciRegistryClient.getOciRegistryWebClient(
                WebClientUtils.builder(), timeout, MockStubService.class);
        assertNotNull(client);
    }

    // Mock stub service interface for testing
    private interface MockStubService {
        // Empty interface for testing web client creation
    }

    private void injectMockField(Object target, String fieldName, Object mock) throws Exception {
        var field = target.getClass().getSuperclass().getDeclaredField(fieldName);
        field.setAccessible(true);
        field.set(target, mock);
    }
}
