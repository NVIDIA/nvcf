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

package com.nvidia.boot.registries.service.registry.client.ngc;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.absent;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.head;
import static com.github.tomakehurst.wiremock.client.WireMock.headRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.matching;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_HASH;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ORG_NAME;
import static com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer.MOCK_BEARER_TOKEN;
import static com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer.MOCK_INVALID_REGISTRY_CRED;
import static com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer.MOCK_TOKEN_ENDPOINT_URL;
import static com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer.V2_PING_URL;
import static com.nvidia.boot.registries.service.registry.client.ngc.NgcContainerRegistryClient.parseContainerImageUrl;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_CONTAINER_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_CONTAINER_REGISTRY_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_2;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_UNKNOWN_ORG;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITH_INVALID_TAG;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.stubbing.Scenario;
import com.github.tomakehurst.wiremock.stubbing.StubMapping;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.reactive.function.client.WebClient;

class NgcContainerRegistryClientTest {

    private static final String TEST_IMAGE_SCOPE =
            "repository:" + TEST_VALID_ORG_NAME + "/" + TEST_VALID_CONTAINER_NAME + ":pull";
    private static final String TEST_IMAGE_MANIFEST_PATH =
            "/v2/" + TEST_VALID_ORG_NAME + "/" + TEST_VALID_CONTAINER_NAME + "/manifests/"
                    + TEST_VALID_CONTAINER_TAG;
    private static final String EXPIRY_SCENARIO = "token expiry";
    private static final String TOKEN_REFRESHED_STATE = "token refreshed";

    private static NgcContainerRegistryClient ngcContainerRegistryClient;

    @BeforeAll
    static void beforeAll() {
        ngcContainerRegistryClient = new NgcContainerRegistryClient(
                WebClientUtils.builder(),
                MOCK_NGC_CONTAINER_REGISTRY_URL,
                MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT
        );
        MockNgcContainerRegistryServer.start(MOCK_NGC_CONTAINER_REGISTRY_URL);
    }

    @AfterAll
    static void cleanup() {
        MockNgcContainerRegistryServer.stop();
    }

    @AfterEach
    void reset() {
        ngcContainerRegistryClient.resetAuthTokenCache();
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().resetRequests();
    }


    static Stream<Arguments> createValidImageUrl() {
        return Stream.of(
                Arguments.of(TEST_NGC_CONTAINER_IMAGE.toString(),
                             TEST_NGC_CONTAINER_REGISTRY, TEST_VALID_ORG_NAME,
                             TEST_VALID_CONTAINER_NAME, TEST_VALID_CONTAINER_TAG, null),
                Arguments.of(TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST.toString(),
                             TEST_NGC_CONTAINER_REGISTRY, TEST_VALID_ORG_NAME,
                             TEST_VALID_CONTAINER_NAME, null,
                             TEST_VALID_CONTAINER_HASH),
                Arguments.of("docker.io/test-container-image:latest",
                             "docker.io", "", TEST_VALID_CONTAINER_NAME, TEST_VALID_CONTAINER_TAG,
                             null));
    }

    @ParameterizedTest
    @MethodSource("createValidImageUrl")
    void parseContainerImageUrl_WithValidFormat_Success(String imageUrl,
                                                        String registryHost,
                                                        String repository,
                                                        String imageName,
                                                        String tag,
                                                        String digest) {
        // When
        NgcContainerRegistryClient.ContainerImageComponents components =
                parseContainerImageUrl(imageUrl);

        // Then
        assertNotNull(components);
        assertEquals(registryHost, components.registryHost());
        assertEquals(repository, components.repository());
        assertEquals(imageName, components.imageName());
        assertEquals(tag, components.tag());
        assertEquals(digest, components.digest());
    }

    static Stream<Arguments> createResolvableImageUrl() {
        return Stream.of(Arguments.of(TEST_NGC_CONTAINER_IMAGE.toString()),
                         Arguments.of(TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST.toString()));
    }

    @MethodSource("createResolvableImageUrl")
    @ParameterizedTest
    void validateContainerImage_Success(String containerUrl) {
        ngcContainerRegistryClient.validateContainerImage(containerUrl,
                                                          MOCK_NGC_CONTAINER_REGISTRY_CRED);

        verifyTokenRequests(1, TEST_IMAGE_SCOPE);
    }

    @Test
    void validateContainerImage_PermissionDenied_Fail() {
        assertThrows(ForbiddenException.class, () -> {
            ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE_PERMISSION_DENIED.toString(),
                    MOCK_NGC_CONTAINER_REGISTRY_CRED);
        });
    }

    @Test
    void validateContainerImage_NotExist_Fail() {
        assertThrows(NotFoundException.class, () -> {
            ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE_NOT_EXISTS.toString(),
                    MOCK_NGC_CONTAINER_REGISTRY_CRED);
        });
    }

    @Test
    void validateContainerImage_InvalidTag_Fail() {
        assertThrows(BadRequestException.class, () -> {
            ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE_WITH_INVALID_TAG.toString(),
                    MOCK_NGC_CONTAINER_REGISTRY_CRED);
        });
    }

    @Test
    void validateContainerImage_CachesBearerToken_Success() {
        String containerUrl = TEST_NGC_CONTAINER_IMAGE.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;

        ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey);
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);

        ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey);
        // Should use cached authentication token without additional API call
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);
    }

    @Test
    void validateContainerImage_DifferentApiKeys_SeparateCacheEntries() {
        String containerUrl = TEST_NGC_CONTAINER_IMAGE.toString();
        String apiKey1 = MOCK_NGC_CONTAINER_REGISTRY_CRED;
        String apiKey2 = "different-" + MOCK_NGC_CONTAINER_REGISTRY_CRED;

        ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey1);
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);

        ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey2);
        verifyTokenRequests(2, TEST_IMAGE_SCOPE);
    }

    @Test
    void validateContainerImage_DifferentContainerImageTag_CachesBearerToken_Success() {
        String containerUrl1 = TEST_NGC_CONTAINER_IMAGE.toString();
        String containerUrl2 = TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;

        ngcContainerRegistryClient.validateContainerImage(containerUrl1, apiKey);
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);

        // Tag and digest address the same repository, so they share one token.
        ngcContainerRegistryClient.validateContainerImage(containerUrl2, apiKey);
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);
    }

    @Test
    void validateContainerImage_DifferentContainerImageName_SeparateCacheEntries() {
        String containerUrl1 = TEST_NGC_CONTAINER_IMAGE.toString();
        String containerUrl2 = TEST_NGC_CONTAINER_IMAGE_2.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;
        String otherImageScope =
                "repository:" + TEST_VALID_ORG_NAME + "/" + TEST_VALID_CONTAINER_NAME + "-2:pull";

        ngcContainerRegistryClient.validateContainerImage(containerUrl1, apiKey);
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);

        assertThrows(NotFoundException.class, () ->
                ngcContainerRegistryClient.validateContainerImage(containerUrl2, apiKey));
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);
        verifyTokenRequests(1, otherImageScope);
    }

    @Test
    void validateContainerImage_DifferentOrg_SeparateCacheEntries() {
        String containerUrl1 = TEST_NGC_CONTAINER_IMAGE.toString();
        String containerUrl2 = TEST_NGC_CONTAINER_IMAGE_UNKNOWN_ORG.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;
        String otherOrgScope =
                "repository:someone-org/" + TEST_VALID_CONTAINER_NAME + ":pull";

        ngcContainerRegistryClient.validateContainerImage(containerUrl1, apiKey);
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);

        assertThrows(NotFoundException.class, () ->
                ngcContainerRegistryClient.validateContainerImage(containerUrl2, apiKey));
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);
        verifyTokenRequests(1, otherOrgScope);
    }

    @Test
    void validateContainerImage_StaleToken_FailsUntilCacheEvicted() {
        String containerUrl = TEST_NGC_CONTAINER_IMAGE.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;
        ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey);
        verifyTokenRequests(1, TEST_IMAGE_SCOPE);

        // The cached token is now rejected, as it would be after out-of-band invalidation.
        var stale = mockServer().stubFor(
                head(urlPathEqualTo(TEST_IMAGE_MANIFEST_PATH))
                        .withHeader(HttpHeaders.AUTHORIZATION, matching(".+"))
                        .atPriority(1)
                        .inScenario(EXPIRY_SCENARIO)
                        .whenScenarioStateIs(Scenario.STARTED)
                        .willSetStateTo(TOKEN_REFRESHED_STATE)
                        .willReturn(aResponse().withStatus(401)
                                            .withHeader(HttpHeaders.WWW_AUTHENTICATE,
                                                        challengeFor(TEST_IMAGE_SCOPE))));
        var refreshed = mockServer().stubFor(
                head(urlPathEqualTo(TEST_IMAGE_MANIFEST_PATH))
                        .withHeader(HttpHeaders.AUTHORIZATION, matching(".+"))
                        .atPriority(1)
                        .inScenario(EXPIRY_SCENARIO)
                        .whenScenarioStateIs(TOKEN_REFRESHED_STATE)
                        .willReturn(aResponse().withStatus(200)));
        withTemporaryStubs(() -> {
            // No retry: a live cached token the registry rejects fails the call outright.
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateContainerImage(
                    containerUrl, apiKey))
                    .isInstanceOf(UnauthorizedException.class);
            verifyTokenRequests(1, TEST_IMAGE_SCOPE);

            // Eviction (forced here; the TTL in production) rediscovers and recovers.
            ngcContainerRegistryClient.resetAuthTokenCache();
            ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey);
            verifyTokenRequests(2, TEST_IMAGE_SCOPE);
        }, stale, refreshed);
    }

    @Test
    void validateContainerImage_TokenRejected_FailsWithoutRetry() {
        var alwaysRejects = mockServer().stubFor(
                head(urlPathEqualTo(TEST_IMAGE_MANIFEST_PATH))
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(401)
                                            .withHeader(HttpHeaders.WWW_AUTHENTICATE,
                                                        challengeFor(TEST_IMAGE_SCOPE))));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE.toString(), MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(UnauthorizedException.class);

            // One discovery, one token fetch, one authorized attempt - no re-challenge loop.
            verifyTokenRequests(1, TEST_IMAGE_SCOPE);
            mockServer().verify(2, headRequestedFor(urlPathEqualTo(TEST_IMAGE_MANIFEST_PATH)));
        }, alwaysRejects);
    }

    /**
     * NGC requires a credential for every pull, so validation must never pass without
     * exercising the supplied one: a probe answer that skips the challenge - success and
     * redirect alike - leaves the credential unverified and is an upstream error, mirroring
     * {@code validateCredential}.
     */
    @ParameterizedTest
    @ValueSource(ints = { 200, 302 })
    void validateContainerImage_ProbeDoesNotChallenge_Fail(int probeStatus) {
        var noChallenge = mockServer().stubFor(
                head(urlPathEqualTo(TEST_IMAGE_MANIFEST_PATH))
                        .withHeader(HttpHeaders.AUTHORIZATION, absent())
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(probeStatus)));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE.toString(), MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(UpstreamException.class);

            mockServer().verify(0, getRequestedFor(urlPathEqualTo(MOCK_TOKEN_ENDPOINT_URL)));
        }, noChallenge);
    }

    @Test
    void validateContainerImage_ProbeForbidden_Fail() {
        // A probe 4xx maps through the standard handlers; widening the probe client's 401
        // pass-through to other statuses would break exactly here.
        var forbidden = mockServer().stubFor(
                head(urlPathEqualTo(TEST_IMAGE_MANIFEST_PATH))
                        .withHeader(HttpHeaders.AUTHORIZATION, absent())
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(403)));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE.toString(), MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(ForbiddenException.class);

            mockServer().verify(0, getRequestedFor(urlPathEqualTo(MOCK_TOKEN_ENDPOINT_URL)));
        }, forbidden);
    }

    @Test
    void validateContainerImage_RejectedByTokenEndpoint_Fail() {
        assertThatThrownBy(() -> ngcContainerRegistryClient.validateContainerImage(
                TEST_NGC_CONTAINER_IMAGE.toString(), MOCK_INVALID_REGISTRY_CRED))
                .isInstanceOf(UnauthorizedException.class);
    }

    @Test
    void validateContainerImage_UnauthorizedWithoutChallenge_Fail() {
        // The probe carries no credential, so a 401 without a Bearer challenge cannot mean the
        // credential was rejected - it is the registry speaking broken protocol, an upstream
        // failure just like not challenging at all.
        var noChallenge = mockServer().stubFor(
                head(urlPathEqualTo(TEST_IMAGE_MANIFEST_PATH))
                        .withHeader(HttpHeaders.AUTHORIZATION, absent())
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(401)));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE.toString(), MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(UpstreamException.class);

            mockServer().verify(0, getRequestedFor(urlPathEqualTo(MOCK_TOKEN_ENDPOINT_URL)));
        }, noChallenge);
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = { "   " })
    void validateContainerImage_BlankSecret_FailsWithoutCallingRegistry(String blankSecret) {
        assertThatThrownBy(() -> ngcContainerRegistryClient.validateContainerImage(
                TEST_NGC_CONTAINER_IMAGE.toString(), blankSecret))
                .isInstanceOf(BadRequestException.class);

        mockServer().verify(0, headRequestedFor(urlPathEqualTo(TEST_IMAGE_MANIFEST_PATH)));
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
    void parseContainerImageUrl_WithInvalidFormats_Fail(String invalidUrl) {
        // When/Then
        assertThrows(BadRequestException.class, () -> parseContainerImageUrl(invalidUrl));
    }

    @Test
    void validateCredential_Success() {
        String token = ngcContainerRegistryClient.validateCredential(
                TEST_NGC_CONTAINER_REGISTRY, MOCK_NGC_CONTAINER_REGISTRY_CRED);

        assertThat(token).isEqualTo(MOCK_BEARER_TOKEN);
        // The token endpoint is discovered from the challenge, and the /v2/ challenge advertises
        // an empty scope, so the token request carries no query parameters at all.
        mockServer().verify(1, getRequestedFor(urlEqualTo(V2_PING_URL)));
        mockServer().verify(1, getRequestedFor(urlEqualTo(MOCK_TOKEN_ENDPOINT_URL)));
    }

    @Test
    void validateCredential_CalledRepeatedly_IsNotCached() {
        ngcContainerRegistryClient.validateCredential(
                TEST_NGC_CONTAINER_REGISTRY, MOCK_NGC_CONTAINER_REGISTRY_CRED);
        ngcContainerRegistryClient.validateCredential(
                TEST_NGC_CONTAINER_REGISTRY, MOCK_NGC_CONTAINER_REGISTRY_CRED);

        mockServer().verify(2, getRequestedFor(urlEqualTo(MOCK_TOKEN_ENDPOINT_URL)));
    }

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = { "   " })
    void validateCredential_BlankSecret_FailsWithoutCallingRegistry(String blankSecret) {
        assertThatThrownBy(() -> ngcContainerRegistryClient.validateCredential(
                TEST_NGC_CONTAINER_REGISTRY, blankSecret))
                .isInstanceOf(BadRequestException.class);

        mockServer().verify(0, getRequestedFor(urlEqualTo(V2_PING_URL)));
    }

    @Test
    void validateCredential_RejectedByTokenEndpoint_Fail() {
        assertThatThrownBy(() -> ngcContainerRegistryClient.validateCredential(
                TEST_NGC_CONTAINER_REGISTRY, MOCK_INVALID_REGISTRY_CRED))
                .isInstanceOf(UnauthorizedException.class);
    }

    @Test
    void validateCredential_BlankTokenInResponse_Fail() {
        // A 200 carrying no token proves nothing about the credential.
        var blankToken = mockServer().stubFor(
                get(urlPathEqualTo(MOCK_TOKEN_ENDPOINT_URL))
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(200)
                                            .withHeader(HttpHeaders.CONTENT_TYPE,
                                                        MediaType.APPLICATION_JSON_VALUE)
                                            .withBody("{\"expires_in\": 3600}")));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateCredential(
                    TEST_NGC_CONTAINER_REGISTRY, MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(UnauthorizedException.class);
        }, blankToken);
    }

    @ParameterizedTest
    @ValueSource(strings = { "Basic realm=\"basic-zone\"", "" })
    void validateCredential_UnusableChallengeOnProbe_Fail(String wwwAuthenticate) {
        // A 401 whose WWW-Authenticate carries no usable Bearer challenge is the registry
        // speaking broken protocol, not a credential rejection - the probe sent no credential.
        var unusableChallenge = mockServer().stubFor(
                get(urlPathEqualTo(V2_PING_URL))
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(401)
                                            .withHeader(HttpHeaders.WWW_AUTHENTICATE,
                                                        wwwAuthenticate)));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateCredential(
                    TEST_NGC_CONTAINER_REGISTRY, MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(UpstreamException.class);

            mockServer().verify(0, getRequestedFor(urlPathEqualTo(MOCK_TOKEN_ENDPOINT_URL)));
        }, unusableChallenge);
    }

    @ParameterizedTest
    @ValueSource(strings = { "{oops}", "100%" })
    void validateCredential_UnusableRealmInChallenge_Fail(String realm) {
        // A challenge whose realm cannot form a URL is the registry speaking broken protocol.
        var badRealm = mockServer().stubFor(
                get(urlPathEqualTo(V2_PING_URL))
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(401)
                                            .withHeader(HttpHeaders.WWW_AUTHENTICATE,
                                                        "Bearer realm=\"%s\"".formatted(realm))));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateCredential(
                    TEST_NGC_CONTAINER_REGISTRY, MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(UpstreamException.class);
        }, badRealm);
    }

    @Test
    void validateCredential_TokenEndpointServerError_Fail() {
        var serverError = mockServer().stubFor(
                get(urlPathEqualTo(MOCK_TOKEN_ENDPOINT_URL))
                        .atPriority(1)
                        .willReturn(aResponse().withStatus(500)));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateCredential(
                    TEST_NGC_CONTAINER_REGISTRY, MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(UpstreamException.class);
        }, serverError);
    }

    @Test
    void validateCredential_RegistryDoesNotChallenge_Fail() {
        // Without a challenge there is nothing to authenticate against, so the credential is
        // unproven - reporting success would be a false positive.
        var noChallenge = mockServer().stubFor(get(urlPathEqualTo(V2_PING_URL))
                                                       .atPriority(1)
                                                       .willReturn(aResponse().withStatus(200)));
        withTemporaryStubs(() -> {
            assertThatThrownBy(() -> ngcContainerRegistryClient.validateCredential(
                    TEST_NGC_CONTAINER_REGISTRY, MOCK_NGC_CONTAINER_REGISTRY_CRED))
                    .isInstanceOf(UpstreamException.class);
        }, noChallenge);
    }

    private static WireMockServer mockServer() {
        return MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer();
    }

    /**
     * Runs a test body against stubs that exist only for that body, then removes them and
     * resets scenario state so no test leaks fixtures into the next.
     */
    private static void withTemporaryStubs(Runnable body, StubMapping... stubs) {
        try {
            body.run();
        } finally {
            for (var stub : stubs) {
                mockServer().removeStub(stub);
            }
            mockServer().resetScenarios();
        }
    }

    /**
     * Counts token requests by the scope the challenge advertised, which is what partitions the
     * token cache. Matching on the decoded query parameter keeps the assertion independent of
     * how the scope happens to be percent-encoded on the wire.
     */
    private static void verifyTokenRequests(int expectedCount, String scope) {
        mockServer().verify(expectedCount,
                            getRequestedFor(urlPathEqualTo(MOCK_TOKEN_ENDPOINT_URL))
                                    .withQueryParam("scope", equalTo(scope)));
    }

    private static String challengeFor(String scope) {
        return "Bearer realm=\"%s%s\",scope=\"%s\""
                .formatted(MOCK_NGC_CONTAINER_REGISTRY_URL, MOCK_TOKEN_ENDPOINT_URL, scope);
    }
}
