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
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ACR_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.MACSigner;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.mock.azure.MockAcrAuthServer;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciArtifactComponents;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciAuthKey;
import java.net.URI;
import java.time.Duration;
import java.time.Instant;
import java.util.Date;
import java.util.Optional;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.web.reactive.function.client.WebClient;

@ExtendWith(MockitoExtension.class)
class OciRegistryAuthClientTest {

    @Mock
    private OciAuthStubService mockOciAuthStubService;

    private static TestOciRegistryAuthClient ociAuthClient;

    private static final String AUTH_TOKEN_RESPONSE = """
            {
                "access_token": "%s"
            }
            """;

    @BeforeAll
    static void beforeAll() throws JOSEException {
        MockAcrAuthServer.start(MOCK_AZURE_REGISTRY_AUTH_URL);
        MockAcrAuthServer.setResponse("/oauth2/token",
                                      AUTH_TOKEN_RESPONSE.formatted(
                                                      createValidJWT(Instant.now().plusSeconds(3600)))
                                              .getBytes());

        ociAuthClient = new TestOciRegistryAuthClient(MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT);
        ociAuthClient.setAuthBaseUrl(MOCK_AZURE_REGISTRY_AUTH_URL);
    }

    @AfterAll
    static void afterAll() {
        MockAcrAuthServer.stop();
    }

    @BeforeEach
    void beforeEach() {
        if (ociAuthClient != null) {
            ociAuthClient.invalidateCache();
        }
    }

    // Create a concrete test implementation of the abstract OciRegistryAuthClient
    private static class TestOciRegistryAuthClient extends OciRegistryAuthClient {

        private String authBaseUrl;

        public TestOciRegistryAuthClient(Duration callTimeout) {
            super(WebClientUtils.builder(), callTimeout);
        }

        @Override
        protected String getCanonicalAuthTokenUrl(String registryHost, String name) {
            var baseUrl = getRegistryBaseUrl(registryHost) + "/oauth2/token";
            return String.format("%s?service=%s&scope=repository:%s:pull", baseUrl, registryHost,
                                 name);
        }

        private String getRegistryBaseUrl(String registryHost) {
            // Use the same logic as AzureRegistryAuthClient for URL replacement
            if (authBaseUrl != null && authBaseUrl.contains("localhost")) {
                return authBaseUrl.replaceAll("localhost-[^:]+:", "localhost:");
            }
            return authBaseUrl != null ? authBaseUrl : "https://" + registryHost;
        }

        private void setAuthBaseUrl(String authBaseUrl) {
            this.authBaseUrl = authBaseUrl;
        }
    }

    @Test
    void constructor_WithValidTimeout_Success() {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        assertNotNull(authClient);
    }

    @Test
    void constructor_WithNullTimeout_UsesDefaultTimeout() {
        var authClient = new TestOciRegistryAuthClient(null);
        assertNotNull(authClient);
    }

    @Test
    void invalidateCache_WithValidAuthKey_DoesNotThrow() {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        var authKey = new OciAuthKey("registry.io", "test/image", "credentials");
        assertDoesNotThrow(() -> authClient.invalidateCache(authKey));
    }

    @Test
    void invalidateAllCache_DoesNotThrow() {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        assertDoesNotThrow(() -> authClient.invalidateCache());
    }

    @Test
    void fetchToken_WithValidAuthKey_ReturnsOciAuthToken() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var authKey = new OciAuthKey("registry.io", "test/image", "dGVzdDpzZWNyZXQ=");
        var expectedUrl = URI.create(
                "https://registry.io/oauth2/token?service=registry.io&scope=repository:test/image:pull");
        var expectedAuth = "Basic dGVzdDpzZWNyZXQ=";

        // Create mock response with valid JWT
        var mockResponse = new OciAuthStubService.OciAuthTokenResponse();
        String validJwt = createValidJWT(Instant.now().plusSeconds(3600));
        mockResponse.setAccessToken(validJwt);

        when(mockOciAuthStubService.fetchToken(eq(expectedUrl), eq(expectedAuth)))
                .thenReturn(mockResponse);

        var result = authClient.fetchToken(authKey);

        assertNotNull(result);
        assertEquals(validJwt, result.token());
        assertNotNull(result.expiresIn());
        assertTrue(result.expiresIn().toSeconds() > 3590 && result.expiresIn().toSeconds() <= 3600);
    }

    @Test
    void extractAuthToken_WithAccessToken_ReturnsAuthToken() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        String validJwt = createValidJWT(Instant.now().plusSeconds(3600));
        response.setAccessToken(validJwt);

        var result = authClient.extractAuthToken(response);

        assertNotNull(result);
        assertEquals(validJwt, result.token());
        assertNotNull(result.expiresIn());
        assertTrue(result.expiresIn().toSeconds() > 3590 && result.expiresIn().toSeconds() <= 3600);
    }

    @Test
    void extractAuthToken_WithTokenField_ReturnsAuthToken() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        String validJwt = createValidJWT(Instant.now().plusSeconds(1800));
        response.setToken(validJwt);

        var result = authClient.extractAuthToken(response);

        assertNotNull(result);
        assertEquals(validJwt, result.token());
        assertNotNull(result.expiresIn());
        assertTrue(result.expiresIn().toSeconds() > 1790 && result.expiresIn().toSeconds() <= 1800);
    }

    @Test
    void extractAuthToken_WithExpiresInField_UsesExpiresIn() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        response.setAccessToken("test-token");
        response.setExpiresIn(7200L); // 2 hours

        var result = authClient.extractAuthToken(response);

        assertNotNull(result);
        assertEquals("test-token", result.token());
        assertEquals(Duration.ofSeconds(7200), result.expiresIn());
    }

    @Test
    void extractAuthToken_WithZeroExpiresIn_FallsBackToJWT() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        String validJwt = createValidJWT(Instant.now().plusSeconds(900));
        response.setAccessToken(validJwt);
        response.setExpiresIn(0L); // Zero should fallback to JWT parsing

        var result = authClient.extractAuthToken(response);

        assertNotNull(result);
        assertEquals(validJwt, result.token());
        assertTrue(result.expiresIn().toSeconds() > 890 && result.expiresIn().toSeconds() <= 900);
    }

    @Test
    void extractAuthToken_WithNegativeExpiresIn_FallsBackToJWT() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        String validJwt = createValidJWT(Instant.now().plusSeconds(600));
        response.setAccessToken(validJwt);
        response.setExpiresIn(-1L); // Negative should fallback to JWT parsing

        var result = authClient.extractAuthToken(response);

        assertNotNull(result);
        assertEquals(validJwt, result.token());
        assertTrue(result.expiresIn().toSeconds() > 590 && result.expiresIn().toSeconds() <= 600);
    }

    @Test
    void extractAuthToken_WithInvalidJWT_UsesDefaultExpiry() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        response.setAccessToken("invalid-jwt-token");

        var result = authClient.extractAuthToken(response);

        assertNotNull(result);
        assertEquals("invalid-jwt-token", result.token());
        assertEquals(Duration.ofSeconds(60), result.expiresIn()); // Default fallback
    }

    @Test
    void extractAuthToken_WithEmptyAccessTokenAndToken_ThrowsException() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        // Leave both accessToken and token empty

        assertThrows(IllegalArgumentException.class, () -> {
            authClient.extractAuthToken(response);
        });
    }

    @Test
    void extractAuthToken_WithNullAccessTokenAndToken_ThrowsException() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        response.setAccessToken(null);
        response.setToken(null);

        assertThrows(IllegalArgumentException.class, () -> {
            authClient.extractAuthToken(response);
        });
    }

    @Test
    void extractAuthToken_WithBlankAccessToken_ThrowsException() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        response.setAccessToken("   ");

        assertThrows(IllegalArgumentException.class, () -> {
            authClient.extractAuthToken(response);
        });
    }

    @Test
    void extractAuthToken_PreferAccessTokenOverToken() throws Exception {
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        injectMockStubService(authClient, mockOciAuthStubService);

        var response = new OciAuthStubService.OciAuthTokenResponse();
        String accessTokenJwt = createValidJWT(Instant.now().plusSeconds(3600));
        String tokenJwt = createValidJWT(Instant.now().plusSeconds(1800));

        response.setAccessToken(accessTokenJwt);
        response.setToken(tokenJwt);

        var result = authClient.extractAuthToken(response);

        assertNotNull(result);
        assertEquals(accessTokenJwt, result.token()); // Should prefer accessToken
        assertTrue(result.expiresIn().toSeconds() > 3590); // Should use accessToken expiry
    }

    // ===== UTILITY TESTS =====

    @Test
    void getExpiresInFromJWS_WithValidToken_ReturnsExpiresIn() throws JOSEException {
        String jwt = createValidJWT(Instant.now().plusSeconds(3600));

        var result = OciRegistryAuthClient.getExpClaimFromJwt(jwt);

        assertTrue(result.isPresent() &&
                           result.get().toSeconds() > 3590 && result.get().toSeconds() <= 3600);
    }

    @Test
    void getExpClaimFromJwt_WithExpiredToken_ReturnsZero() throws JOSEException {
        String jwt = createValidJWT(Instant.now().minusSeconds(3600));

        var result = OciRegistryAuthClient.getExpClaimFromJwt(jwt);
        assertEquals(Optional.of(Duration.ZERO), result);
    }

    @Test
    void getExpClaimFromJwt_WithMissingExpClaim_ReturnsNull() throws JOSEException {
        String jwt = createJWTWithoutExpClaim();

        var result = OciRegistryAuthClient.getExpClaimFromJwt(jwt);
        assertThat(result).isEmpty();
    }

    @Test
    void getExpClaimFromJwt_WithNull_ReturnsNull() {
        assertThat(OciRegistryAuthClient.getExpClaimFromJwt(null)).isEmpty();
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",
            "invalid-jwt",
            "only.one.part", // less than 3 parts
            "header.invalid-base64.signature", // invalid base64 payload
            "header.e30.signature" // valid base64 but empty JSON object
    })
    void getExpClaimFromJwt_WithInvalidToken_ReturnsNull(String invalidJwt) {
        var result = OciRegistryAuthClient.getExpClaimFromJwt(invalidJwt);
        assertThat(result).isEmpty();
    }

    private void injectMockStubService(TestOciRegistryAuthClient authClient,
                                       OciAuthStubService mockService) throws Exception {
        var field = OciRegistryAuthClient.class.getDeclaredField("ociAuthStubService");
        field.setAccessible(true);
        field.set(authClient, mockService);
    }

    private static String createValidJWT(Instant expirationTime) throws JOSEException {
        var claimsSet = new JWTClaimsSet.Builder()
                .issuer("test-issuer")
                .subject("test-subject")
                .expirationTime(Date.from(expirationTime))
                .build();

        var signedJWT = new SignedJWT(
                new JWSHeader(JWSAlgorithm.HS256),
                claimsSet);

        var signer = new MACSigner("test-secret-key-that-is-at-least-256-bits-long");
        signedJWT.sign(signer);

        return signedJWT.serialize();
    }

    private String createJWTWithoutExpClaim() throws JOSEException {
        var claimsSet = new JWTClaimsSet.Builder()
                .issuer("test-issuer")
                .subject("test-subject")
                .build();

        var signedJWT = new SignedJWT(
                new JWSHeader(JWSAlgorithm.HS256),
                claimsSet);

        var signer = new MACSigner("test-secret-key-that-is-at-least-256-bits-long");
        signedJWT.sign(signer);

        return signedJWT.serialize();
    }

    // ===== CACHE BEHAVIOR TESTS =====

    @Test
    void cacheSize_InitiallyEmpty() {
        // Cache should be empty at start of each test due to @BeforeEach cleanup
        assertEquals(0, ociAuthClient.getCacheSize());

        // Verify new instances also see the empty shared cache
        var authClient = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        assertEquals(0, authClient.getCacheSize());
    }

    @Test
    void cacheSize_IncreasesAfterTokenFetch() {
        assertEquals(0, ociAuthClient.getCacheSize());

        var components = new OciArtifactComponents("localhost-acr", "cache/test1", "latest");
        assertDoesNotThrow(() -> {
            var token = ociAuthClient.getToken(components, MOCK_ACR_CREDENTIALS);
            assertNotNull(token);
        });

        assertEquals(1, ociAuthClient.getCacheSize());
    }

    @Test
    void cacheSize_RemainsConstantForSameKey() {
        var components = new OciArtifactComponents("localhost-acr", "cache/same-key", "v1.0.0");

        // First fetch
        assertDoesNotThrow(() -> {
            var token = ociAuthClient.getToken(components, MOCK_ACR_CREDENTIALS);
            assertNotNull(token);
        });
        long sizeAfterFirst = ociAuthClient.getCacheSize();

        // Second fetch with same key - should use cache
        assertDoesNotThrow(() -> {
            var token = ociAuthClient.getToken(components, MOCK_ACR_CREDENTIALS);
            assertNotNull(token);
        });
        long sizeAfterSecond = ociAuthClient.getCacheSize();

        // Cache size should remain the same
        assertEquals(sizeAfterFirst, sizeAfterSecond);
    }

    @Test
    void cacheSize_IncreasesForDifferentKeys() {
        assertEquals(0, ociAuthClient.getCacheSize());

        // Fetch tokens with different repository names (different cache keys)
        var components1 = new OciArtifactComponents("localhost-acr", "cache/repo1", "latest");
        var components2 = new OciArtifactComponents("localhost-acr", "cache/repo2", "latest");
        var components3 = new OciArtifactComponents("localhost-acr", "cache/repo3", "v1.0.0");

        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components1, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(1, ociAuthClient.getCacheSize());

        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components2, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(2, ociAuthClient.getCacheSize());

        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components3, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(3, ociAuthClient.getCacheSize());
    }

    @Test
    void cacheSize_SameRepositoryDifferentReferences_SharesCacheEntry() {
        assertEquals(0, ociAuthClient.getCacheSize());

        var repoName = "cache/same-repo";

        // Same repository, different references (tag vs digest) should use same cache entry
        var components1 = new OciArtifactComponents("localhost-acr", repoName, "latest");
        var components2 = new OciArtifactComponents("localhost-acr", repoName, "v1.0.0");
        var digest = "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef";
        var components3 = new OciArtifactComponents("localhost-acr", repoName, digest);

        // First fetch
        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components1, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(1, ociAuthClient.getCacheSize());

        // Second fetch with different tag - should use same cache entry
        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components2, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(1, ociAuthClient.getCacheSize()); // Still 1 entry

        // Third fetch with digest - should still use same cache entry
        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components3, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(1, ociAuthClient.getCacheSize()); // Still 1 entry
    }

    @Test
    void cacheSize_IncreasesForDifferentCredentials() {
        assertEquals(0, ociAuthClient.getCacheSize());

        var components = new OciArtifactComponents("localhost-acr", "cache/creds-test", "latest");

        // Fetch with first credentials
        assertDoesNotThrow(() -> {
            var token = ociAuthClient.getToken(components, MOCK_ACR_CREDENTIALS);
            assertNotNull(token);
        });
        assertEquals(1, ociAuthClient.getCacheSize());

        // Fetch with different credentials (should create new cache entry)
        var differentCredentials = "ZGlmZmVyZW50OnNlY3JldA=="; // different:secret in base64
        assertDoesNotThrow(() -> {
            var token = ociAuthClient.getToken(components, differentCredentials);
            assertNotNull(token);
        });

        assertEquals(2, ociAuthClient.getCacheSize());
    }

    @Test
    void cacheInvalidation_ReducesCacheSize_IntegrationTest() {
        // Cache starts empty due to @BeforeEach cleanup
        assertEquals(0, ociAuthClient.getCacheSize());

        var components =
                new OciArtifactComponents("localhost-acr", "cache/invalidation-test", "v1.0.0");
        var authKey =
                new OciAuthKey(components.registryHost(), components.name(), MOCK_ACR_CREDENTIALS);

        // Populate cache
        assertDoesNotThrow(() -> {
            var token = ociAuthClient.getToken(components, MOCK_ACR_CREDENTIALS);
            assertNotNull(token);
        });
        assertEquals(1, ociAuthClient.getCacheSize());

        // Invalidate specific entry
        ociAuthClient.invalidateCache(authKey);

        // Cache should be empty again
        assertEquals(0, ociAuthClient.getCacheSize());
    }

    @Test
    void cacheInvalidateAll_ClearsCacheCompletely() {
        // Cache starts empty due to @BeforeEach cleanup
        assertEquals(0, ociAuthClient.getCacheSize());

        // Populate cache with multiple entries
        var components1 = new OciArtifactComponents("localhost-acr", "cache/clear1", "latest");
        var components2 = new OciArtifactComponents("localhost-acr", "cache/clear2", "latest");

        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components1, MOCK_ACR_CREDENTIALS);
            ociAuthClient.getToken(components2, MOCK_ACR_CREDENTIALS);
        });

        assertEquals(2, ociAuthClient.getCacheSize());

        // Clear all cache
        ociAuthClient.invalidateCache();

        assertEquals(0, ociAuthClient.getCacheSize());
    }

    @Test
    void cacheSize_MonitoringDuringComplexOperations_IntegrationTest() {
        assertEquals(0, ociAuthClient.getCacheSize());

        // Step 1: Add multiple entries
        var components1 = new OciArtifactComponents("localhost-acr", "monitor/step1", "latest");
        var components2 = new OciArtifactComponents("localhost-acr", "monitor/step2", "latest");

        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components1, MOCK_ACR_CREDENTIALS);
            ociAuthClient.getToken(components2, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(2, ociAuthClient.getCacheSize());

        // Step 2: Fetch same keys (should not increase cache)
        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components1, MOCK_ACR_CREDENTIALS);
            ociAuthClient.getToken(components2, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(2, ociAuthClient.getCacheSize()); // Still 2

        // Step 3: Invalidate one entry
        var authKey1 = new OciAuthKey(components1.registryHost(), components1.name(),
                                      MOCK_ACR_CREDENTIALS);
        ociAuthClient.invalidateCache(authKey1);
        assertEquals(1, ociAuthClient.getCacheSize());

        // Step 4: Add new entry
        var components3 = new OciArtifactComponents("localhost-acr", "monitor/step3", "latest");
        assertDoesNotThrow(() -> {
            ociAuthClient.getToken(components3, MOCK_ACR_CREDENTIALS);
        });
        assertEquals(2, ociAuthClient.getCacheSize()); // Back to 2
    }

    @Test
    void cache_IsIsolatedBetweenDifferentRegistryInstances() {
        // Create two separate registry auth client instances
        var authClient1 = new TestOciRegistryAuthClient(Duration.ofSeconds(30));
        var authClient2 = new TestOciRegistryAuthClient(Duration.ofSeconds(30));

        authClient1.setAuthBaseUrl(MOCK_AZURE_REGISTRY_AUTH_URL);
        authClient2.setAuthBaseUrl(MOCK_AZURE_REGISTRY_AUTH_URL);

        // Both should start with empty caches
        assertEquals(0, authClient1.getCacheSize());
        assertEquals(0, authClient2.getCacheSize());

        // Add entries to first client's cache
        var components1 = new OciArtifactComponents("localhost-acr", "isolation/test1", "latest");
        var components2 = new OciArtifactComponents("localhost-acr", "isolation/test2", "latest");

        assertDoesNotThrow(() -> {
            authClient1.getToken(components1, MOCK_ACR_CREDENTIALS);
            authClient1.getToken(components2, MOCK_ACR_CREDENTIALS);
        });

        // First client should have 2 entries, second client should still have 0
        assertEquals(2, authClient1.getCacheSize());
        assertEquals(0, authClient2.getCacheSize());

        // Add entries to second client's cache
        var components3 = new OciArtifactComponents("localhost-acr", "isolation/test3", "latest");

        assertDoesNotThrow(() -> {
            authClient2.getToken(components3, MOCK_ACR_CREDENTIALS);
        });

        // Each client should have independent cache sizes
        assertEquals(2, authClient1.getCacheSize());
        assertEquals(1, authClient2.getCacheSize());

        // Clear first client's cache - should not affect second client
        authClient1.invalidateCache();

        assertEquals(0, authClient1.getCacheSize());
        assertEquals(1, authClient2.getCacheSize()); // Should remain unchanged

        // Clear second client's cache
        authClient2.invalidateCache();

        assertEquals(0, authClient1.getCacheSize());
        assertEquals(0, authClient2.getCacheSize());
    }
}
