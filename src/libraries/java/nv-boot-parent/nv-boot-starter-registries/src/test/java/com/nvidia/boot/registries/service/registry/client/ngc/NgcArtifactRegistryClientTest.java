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

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.stubbing.Scenario.STARTED;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_NOT_EXIST_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_PAGINATED_429_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_RATE_LIMITED_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILES_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.MODEL_FILE_PERMISSION_DENIED_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_NOT_EXISTS_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILES_URL;
import static com.nvidia.boot.mock.ngc.MockCasServer.RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM;
import static com.nvidia.boot.registries.util.TestConstants.BASE_NGC_ARTIFACT_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_API_KEY;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_BASE_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_OAUTH2_BASE_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_OAUTH2_GROUP_SCOPE;
import static com.nvidia.boot.registries.util.TestConstants.TEST_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_CHART;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_CHART_PERMISSION_DENIED;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.springframework.http.HttpHeaders.CONTENT_TYPE;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.registries.service.registry.dto.ArtifactFile;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.reactive.function.client.WebClient;

class NgcArtifactRegistryClientTest {
    private static NgcArtifactRegistryClient ngcArtifactRegistryClient;

    @BeforeAll
    static void beforeAll() {
        ngcArtifactRegistryClient = new NgcArtifactRegistryClient(
                WebClientUtils.builder(),
                MOCK_NGC_REGISTRY_BASE_URL,
                MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT,
                MOCK_NGC_REGISTRY_OAUTH2_BASE_URL,
                MOCK_NGC_REGISTRY_OAUTH2_GROUP_SCOPE
        );
        MockCasServer.start(MOCK_NGC_REGISTRY_OAUTH2_BASE_URL, MOCK_NGC_REGISTRY_BASE_URL);
    }

    @AfterAll
    static void cleanup() {
        MockCasServer.stop();
    }

    @AfterEach
    void resetScenarios() {
        MockCasServer.resetScenarios();
    }

    @Test
    void fetchModelSize_Success() {
        var url = MOCK_NGC_REGISTRY_BASE_URL + MODEL_FILES_URL;
        var modelSize = ngcArtifactRegistryClient.fetchModelSize(url, MOCK_NGC_API_KEY);
        assertEquals(46484790292L, modelSize);
    }

    @Test
    void fetchModelSize_NotExist_Fail() {
        var url = MOCK_NGC_REGISTRY_BASE_URL + MODEL_FILES_NOT_EXIST_URL;
        assertThrows(NotFoundException.class,
                     () -> ngcArtifactRegistryClient.fetchModelSize(url, MOCK_NGC_API_KEY));
    }

    @Test
    void fetchModelSize_PermissionDenied_Fail() {
        var url = MOCK_NGC_REGISTRY_BASE_URL + MODEL_FILE_PERMISSION_DENIED_URL;
        assertThrows(ForbiddenException.class,
                     () -> ngcArtifactRegistryClient.fetchModelSize(url, MOCK_NGC_API_KEY));
    }

    @Test
    void fetchResourceSize_Success() {
        var url = MOCK_NGC_REGISTRY_BASE_URL + RESOURCE_FILES_URL;
        var resourceSize = ngcArtifactRegistryClient.fetchResourceSize(url, MOCK_NGC_API_KEY);
        assertEquals(69753543449L, resourceSize);
    }

    @Test
    void fetchResourceSize_NotExist_Fail() {
        var url = MOCK_NGC_REGISTRY_BASE_URL + RESOURCE_FILES_NOT_EXISTS_URL;
        assertThrows(NotFoundException.class,
                     () -> ngcArtifactRegistryClient.fetchResourceSize(url, MOCK_NGC_API_KEY));
    }

    @Test
    void fetchResourceSize_PermissionDenied_Fail() {
        var url = MOCK_NGC_REGISTRY_BASE_URL + RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM;
        assertThrows(ForbiddenException.class,
                     () -> ngcArtifactRegistryClient.fetchResourceSize(url, MOCK_NGC_API_KEY));
    }

    @Test
    void getPreSignedArtifactURLs_Success() {
        var url = MOCK_NGC_REGISTRY_BASE_URL + MODEL_FILES_URL;
        var artifactFiles =
                ngcArtifactRegistryClient.getPreSignedArtifactURLs(url, MOCK_NGC_API_KEY);
        assertThat(artifactFiles).hasSize(2);
        assertThat(artifactFiles).contains(
                new ArtifactFile("/file1", "https://api.stg.ngc.nvidia.com/file1"));
        assertThat(artifactFiles).contains(
                new ArtifactFile("/file2", "https://api.stg.ngc.nvidia.com/file2"));
    }

    @Test
    void validateArtifact_Success() {
        var url = BASE_NGC_ARTIFACT_URL + MODEL_FILES_URL;
        ngcArtifactRegistryClient.validateArtifact(url, MOCK_NGC_API_KEY, ArtifactTypeEnum.MODEL);
    }

    @Test
    void validateArtifact_NotExist_Fail() {
        var url = BASE_NGC_ARTIFACT_URL + MODEL_FILES_NOT_EXIST_URL;
        assertThrows(NotFoundException.class,
                     () -> ngcArtifactRegistryClient.validateArtifact(url, MOCK_NGC_API_KEY,
                                                                      ArtifactTypeEnum.MODEL));
    }

    @Test
    void validateArtifact_PermissionDenied_Fail() {
        var url = BASE_NGC_ARTIFACT_URL + MODEL_FILE_PERMISSION_DENIED_URL;
        assertThrows(ForbiddenException.class,
                     () -> ngcArtifactRegistryClient.validateArtifact(url, MOCK_NGC_API_KEY,
                                                                      ArtifactTypeEnum.MODEL));
    }

    @Test
    void validateHelmChart_Success() {
        var url = TEST_NGC_HELM_CHART.toString();
        ngcArtifactRegistryClient.validateHelmChart(url, MOCK_NGC_API_KEY);
    }

    @Test
    void validateHelmChart_NotExistsChart_Fail() {
        var url = TEST_HELM_CHART_NOT_EXISTS.toString();
        assertThrows(NotFoundException.class,
                     () -> ngcArtifactRegistryClient.validateHelmChart(url, MOCK_NGC_API_KEY));
    }

    @Test
    void validateHelmChart_PermissionDenied_Fail() {
        var url = TEST_NGC_HELM_CHART_PERMISSION_DENIED.toString();
        assertThrows(ForbiddenException.class,
                     () -> ngcArtifactRegistryClient.validateHelmChart(url, MOCK_NGC_API_KEY));
    }

    @Test
    void getPreSignedArtifactURLs_RetryOn429_Success() {
        // Simulate NGC returning 429 on the first attempt, then 200 on retry
        MockCasServer.stubFor(get(urlPathEqualTo(MODEL_FILES_URL))
                                      .inScenario("model-files-429-retry")
                                      .whenScenarioStateIs(STARTED)
                                      .willReturn(aResponse().withStatus(429))
                                      .willSetStateTo("retry-success"));
        MockCasServer.stubFor(get(urlPathEqualTo(MODEL_FILES_URL))
                                      .inScenario("model-files-429-retry")
                                      .whenScenarioStateIs("retry-success")
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(MockCasServer.getModelFileResponse())));

        var url = MOCK_NGC_REGISTRY_BASE_URL + MODEL_FILES_URL;
        var artifactFiles = ngcArtifactRegistryClient.getPreSignedArtifactURLs(url, MOCK_NGC_API_KEY);
        assertThat(artifactFiles).hasSize(2);
        assertThat(artifactFiles).contains(new ArtifactFile("/file1",
                                                            "https://api.stg.ngc.nvidia.com/file1"));
    }

    @Test
    void getPreSignedArtifactURLs_ExhaustedRetries429_Fail() {
        // MODEL_FILES_RATE_LIMITED_URL always returns 429 — retries should be exhausted
        var url = MOCK_NGC_REGISTRY_BASE_URL + MODEL_FILES_RATE_LIMITED_URL;
        assertThrows(TooManyRequestsException.class,
                     () -> ngcArtifactRegistryClient.getPreSignedArtifactURLs(url, MOCK_NGC_API_KEY));
    }

    @Test
    void getPreSignedArtifactURLs_Paginated_RetryOn429_Success() {
        // First page is already stubbed in MockCasServer.start() with totalPages=2.
        // Stub the second page (page-number=1) to return 429 once, then 200.
        MockCasServer.stubFor(get(urlPathEqualTo(MODEL_FILES_PAGINATED_429_URL))
                                      .withQueryParam("page-number", equalTo("1"))
                                      .inScenario("paginated-429-retry")
                                      .whenScenarioStateIs(STARTED)
                                      .willReturn(aResponse().withStatus(429))
                                      .willSetStateTo("page2-retry-success"));
        MockCasServer.stubFor(get(urlPathEqualTo(MODEL_FILES_PAGINATED_429_URL))
                                      .withQueryParam("page-number", equalTo("1"))
                                      .inScenario("paginated-429-retry")
                                      .whenScenarioStateIs("page2-retry-success")
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(MockCasServer.getModelFilesSecondPageResponse())));

        var url = MOCK_NGC_REGISTRY_BASE_URL + MODEL_FILES_PAGINATED_429_URL;
        var artifactFiles = ngcArtifactRegistryClient.getPreSignedArtifactURLs(url, MOCK_NGC_API_KEY);
        assertThat(artifactFiles).hasSize(4);
        assertThat(artifactFiles).contains(new ArtifactFile("/file1",
                                                            "https://api.stg.ngc.nvidia.com/file1"));
        assertThat(artifactFiles).contains(new ArtifactFile("/file3",
                                                            "https://api.stg.ngc.nvidia.com/file3"));
    }

    @Test
    void validateCredential_WithLegacyAPIKey_Success() {
        // Legacy API keys (not starting with "nvapi-") should go through AuthN endpoint
        // Format: base64($oauthtoken:api-key)
        String registryHost = "helm.stg.ngc.nvidia.com";
        String apiKey = "legacy-api-key-test-1";

        String token = ngcArtifactRegistryClient.validateCredential(registryHost, apiKey);

        assertThat(token).isNotNull();
        assertThat(token).isEqualTo("token");
    }

    @Test
    void validateCredential_WithAPIKey_Success() {
        // API keys starting with "nvapi-" should skip AuthN
        // Format: base64($oauthtoken:nvapi-xxx)
        String registryHost = "helm.stg.ngc.nvidia.com";
        String apiKey = "nvapi-helm-registry-key-test-1";

        String token = ngcArtifactRegistryClient.validateCredential(registryHost, apiKey);

        assertThat(token).isNotNull();
        assertThat(token).isEqualTo(apiKey);
    }
}
