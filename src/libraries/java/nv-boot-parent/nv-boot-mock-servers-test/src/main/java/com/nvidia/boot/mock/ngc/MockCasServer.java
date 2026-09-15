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

package com.nvidia.boot.mock.ngc;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.delete;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.matching;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;
import static com.nvidia.boot.mock.BootTestConstants.TEST_UNKNOWN_HELM_CHART_VERSION;
import static com.nvidia.boot.mock.BootTestConstants.TEST_UNKNOWN_ORG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_UNKNOWN_TEAM_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_HELM_CHART_VERSION;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_MODEL_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_MODEL_NAME_2;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ORG_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ORG_NAME_INVALID_KEY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_RESOURCE_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_RESOURCE_NAME_2;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_SMALL_MODEL_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_TEAM_NAME;
import static org.springframework.http.HttpHeaders.CONTENT_TYPE;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import tools.jackson.databind.json.JsonMapper;
import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.client.MappingBuilder;
import com.github.tomakehurst.wiremock.matching.AnythingPattern;
import java.net.URI;
import java.util.List;
import lombok.Data;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;

public class MockCasServer {

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();
    private static WireMockServer casMockServer;
    private static WireMockServer authnMockServer;

    // Model Artifacts
    public static final String MODEL_SIZE_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/" + TEST_VALID_MODEL_NAME + "/versions/0.1";
    public static final String MODEL_FILES_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/" + TEST_VALID_MODEL_NAME + "/0.1/files";
    public static final String MODEL_FILES_URL_WITH_VERSION =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/" + TEST_VALID_MODEL_NAME +
                    "/versions/0.1/files";
    public static final String MODEL_FILES_URL_2 =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/" + TEST_VALID_MODEL_NAME_2 + "/0.1/files";
    public static final String MODEL_FILES_NOT_EXIST_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/" + TEST_VALID_MODEL_NAME + "/0.5/files";
    public static final String MODEL_FILE_PERMISSION_DENIED_URL =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/models/" + TEST_VALID_MODEL_NAME + "/0.1/files";
    public static final String MODEL_SIZE_NOT_EXISTS_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/" + TEST_VALID_MODEL_NAME + "/versions/0.5";
    public static final String MODEL_SIZE_PERMISSION_DENIED_URL =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/models/" + TEST_VALID_MODEL_NAME +
                    "/versions/0.1";
    public static final String MODEL_SMALL_SIZE_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/" + TEST_VALID_SMALL_MODEL_NAME +
                    "/versions/0.1";
    public static final String MODEL_SMALL_SIZE_FILES_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/" + TEST_VALID_SMALL_MODEL_NAME +
                    "/0.1/files";
    public static final String MODEL_SIZE_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_MODEL_NAME + "/versions/0.1";
    public static final String MODEL_SIZE_URL_WITH_TEAM_2 =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_MODEL_NAME_2 + "/versions/0.1";
    public static final String MODEL_FILES_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_MODEL_NAME + "/0.1/files";
    public static final String MODEL_FILES_URL_WITH_TEAM_2 =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_MODEL_NAME_2 + "/0.1/files";
    public static final String MODEL_FILES_NOT_EXIST_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_MODEL_NAME + "/0.5/files";
    public static final String MODEL_FILE_PERMISSION_DENIED_URL_WITH_TEAM =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_MODEL_NAME + "/0.1/files";
    public static final String MODEL_SIZE_NOT_EXISTS_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_MODEL_NAME + "/versions/0.5";
    public static final String MODEL_SIZE_PERMISSION_DENIED_URL_WITH_TEAM =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_MODEL_NAME + "/versions/0.1";
    public static final String MODEL_SMALL_SIZE_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_SMALL_MODEL_NAME + "/versions/0.1";
    public static final String MODEL_SMALL_SIZE_FILES_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/models/" + TEST_VALID_SMALL_MODEL_NAME + "/0.1/files";
    // Resource artifacts
    public static final String RESOURCE_SIZE_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/resources/" + TEST_VALID_RESOURCE_NAME +
                    "/versions/0.1";
    public static final String RESOURCE_FILES_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/resources/" + TEST_VALID_RESOURCE_NAME +
                    "/0.1/files";
    public static final String RESOURCE_FILES_NOT_EXISTS_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/resources/" + TEST_VALID_RESOURCE_NAME +
                    "/0.5/files";
    public static final String RESOURCE_SIZE_NOT_EXISTS_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/resources/" + TEST_VALID_RESOURCE_NAME +
                    "/versions/0.5";
    public static final String RESOURCE_SIZE_PERMISSION_DENIED_URL =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/resources/" + TEST_VALID_RESOURCE_NAME +
                    "/versions/0.1";
    public static final String RESOURCE_SIZE_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/resources/" + TEST_VALID_RESOURCE_NAME + "/versions/0.1";
    public static final String RESOURCE_SIZE_URL_WITH_TEAM_2 =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/resources/" + TEST_VALID_RESOURCE_NAME_2 + "/versions/0.1";
    public static final String RESOURCE_FILES_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/resources/" + TEST_VALID_RESOURCE_NAME + "/0.1/files";
    public static final String RESOURCE_FILES_URL_WITH_TEAM_2 =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/resources/" + TEST_VALID_RESOURCE_NAME_2 + "/0.1/files";
    public static final String RESOURCE_FILES_URL_WITH_VERSION =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/resources/" + TEST_VALID_RESOURCE_NAME +
                    "/versions/0.1/files";
    public static final String RESOURCE_SIZE_NOT_EXISTS_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/resources/" + TEST_VALID_RESOURCE_NAME + "/versions/0.5";
    public static final String RESOURCE_FILE_NOT_EXISTS_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/resources/" + TEST_VALID_RESOURCE_NAME + "/0.5/files";
    public static final String RESOURCE_SIZE_PERMISSION_DENIED_URL_WITH_TEAM =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/resources/" + TEST_VALID_RESOURCE_NAME + "/versions/0.1";
    public static final String RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/resources/" + TEST_VALID_RESOURCE_NAME + "/0.1/files";
    // Helm chart artifacts
    public static final String HELM_SIZE_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/helm-charts/" + TEST_VALID_HELM_CHART_NAME + "/versions/" +
                    TEST_VALID_HELM_CHART_VERSION;
    public static final String HELM_SIZE_NOT_EXISTS_URL_WITH_TEAM =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/helm-charts/" + TEST_VALID_HELM_CHART_NAME + "/versions/" +
                    TEST_UNKNOWN_HELM_CHART_VERSION;
    public static final String HELM_SIZE_PERMISSION_DENIED_URL_WITH_TEAM =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/helm-charts/" + TEST_VALID_HELM_CHART_NAME + "/versions/" +
                    TEST_UNKNOWN_HELM_CHART_VERSION;
    public static final String HELM_SIZE_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/helm-charts/" + TEST_VALID_HELM_CHART_NAME + "/versions/" +
                    TEST_VALID_HELM_CHART_VERSION;
    public static final String HELM_SIZE_NOT_EXISTS_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/helm-charts/" + TEST_VALID_HELM_CHART_NAME + "/versions/" +
                    TEST_UNKNOWN_HELM_CHART_VERSION;
    public static final String HELM_SIZE_PERMISSION_DENIED_URL =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/team/" + TEST_VALID_TEAM_NAME +
                    "/helm-charts/" + TEST_VALID_HELM_CHART_NAME + "/versions/" +
                    TEST_UNKNOWN_HELM_CHART_VERSION;
    // 429 retry test paths
    public static final String MODEL_FILES_RATE_LIMITED_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/rate-limited-model/0.1/files";
    public static final String MODEL_FILES_PAGINATED_429_URL =
            "/v2/org/" + TEST_VALID_ORG_NAME + "/models/paginated-429-model/0.1/files";

    // For NVCT checkpoints/results creation
    public static final String CHECKPOINT_URL = "/v2/org/" + TEST_VALID_ORG_NAME + "/models";
    public static final String CHECKPOINT_URL_WITH_TEAM = "/v2/org/" + TEST_VALID_ORG_NAME
            + "/team/" + TEST_VALID_TEAM_NAME + "/models";
    public static final String CHECKPOINT_URL_WITH_UNKNOWN_ORG =
            "/v2/org/" + TEST_UNKNOWN_ORG_NAME + "/models";
    public static final String CHECKPOINT_URL_WITH_UNKNOW_TEAM = "/v2/org/" + TEST_VALID_ORG_NAME
            + "/team/" + TEST_UNKNOWN_TEAM_NAME + "/models";
    public static final String CHECKPOINT_URL_WITH_INVALID_KEY =
            "/v2/org/" + TEST_VALID_ORG_NAME_INVALID_KEY + "/models";
    private static final String RESOURCE_SIZE = """
            {
                "recipeVersion": {
                    "status": "UPLOAD_COMPLETE",
                    "description": "",
                    "totalSizeInBytes": 69753543449,
                    "totalFileCount": 138,
                    "versionId": "1",
                    "createdByUser": "nvssa-stg-PzDg7EYCG6JIQoVHt-dvDbwEO87wafx0kWwX7R6oYAk",
                    "createdDate": "2024-01-22T06:59:33.191Z",
                    "id": 1
                  },
                  "recipe": {
                    "orgName": "zq9tgrjzrfpo",
                    "latestVersionId": 1,
                    "shortDescription": "models/Mixtral-8x7B-Instruct-v0.1-offloading-demo",
                    "isReadOnly": true,
                    "publicDatasetUsed": {},
                    "application": "Other",
                    "latestVersionSizeInBytes": 59753543449,
                    "isPublic": false,
                    "description": "",
                    "latestVersionIdStr": "1",
                    "canGuestDownload": false,
                    "precision": "FP16",
                    "framework": "Other",
                    "createdDate": "2024-01-22T06:57:41.704Z",
                    "name": "mixtral",
                    "displayName": "models/Mixtral-8x7B-Instruct-v0.1-offloading-demo",
                    "modelFormat": "PY_TORCH_PTH",
                    "updatedDate": "2024-01-22T08:06:15.240Z"
                  },
                  "requestStatus": {
                    "statusCode": "SUCCESS",
                    "requestId": "de003843-4d8a-4713-bc86-d4e4da096172"
                  }
                }
            """;
    private static final String MODEL_SIZE = """
            {
                "modelVersion": {
                    "status": "UPLOAD_COMPLETE",
                    "description": "",
                    "totalSizeInBytes": 46484790292,
                    "totalFileCount": 556,
                    "versionId": "1",
                    "createdByUser": "nvssa-stg-PzDg7EYCG6JIQoVHt-dvDbwEO87wafx0kWwX7R6oYAk",
                    "createdDate": "2024-01-22T06:59:33.191Z",
                    "id": 1
                  },
                  "model": {
                    "orgName": "zq9tgrjzrfpo",
                    "latestVersionId": 1,
                    "shortDescription": "models/Mixtral-8x7B-Instruct-v0.1-offloading-demo",
                    "isReadOnly": true,
                    "publicDatasetUsed": {},
                    "application": "Other",
                    "latestVersionSizeInBytes": 36484790292,
                    "isPublic": false,
                    "description": "",
                    "latestVersionIdStr": "1",
                    "canGuestDownload": false,
                    "precision": "FP16",
                    "framework": "Other",
                    "createdDate": "2024-01-22T06:57:41.704Z",
                    "name": "mixtral",
                    "displayName": "models/Mixtral-8x7B-Instruct-v0.1-offloading-demo",
                    "modelFormat": "PY_TORCH_PTH",
                    "updatedDate": "2024-01-22T08:06:15.240Z"
                  },
                  "requestStatus": {
                    "statusCode": "SUCCESS",
                    "requestId": ""
                  }
                }
            """;
    private static final String MODEL_SMALL_SIZE = """
            {
                "modelVersion": {
                    "status": "UPLOAD_COMPLETE",
                    "description": "",
                    "totalSizeInBytes": 36,
                    "totalFileCount": 556,
                    "versionId": "1",
                    "createdByUser": "nvssa-stg-PzDg7EYCG6JIQoVHt-dvDbwEO87wafx0kWwX7R6oYAk",
                    "createdDate": "2024-01-22T06:59:33.191Z",
                    "id": 1
                  },
                  "model": {
                    "orgName": "zq9tgrjzrfpo",
                    "latestVersionId": 1,
                    "shortDescription": "models/Mixtral-8x7B-Instruct-v0.1-offloading-demo",
                    "isReadOnly": true,
                    "publicDatasetUsed": {},
                    "application": "Other",
                    "latestVersionSizeInBytes": 36,
                    "isPublic": false,
                    "description": "",
                    "latestVersionIdStr": "1",
                    "canGuestDownload": false,
                    "precision": "FP16",
                    "framework": "Other",
                    "createdDate": "2024-01-22T06:57:41.704Z",
                    "name": "mixtral",
                    "displayName": "models/Mixtral-8x7B-Instruct-v0.1-offloading-demo",
                    "modelFormat": "PY_TORCH_PTH",
                    "updatedDate": "2024-01-22T08:06:15.240Z"
                  },
                  "requestStatus": {
                    "statusCode": "SUCCESS",
                    "requestId": ""
                  }
                }
            """;
    private static final String HELM_SIZE = """
            {
              "requestStatus": {
                "statusCode": "SUCCESS",
                "requestId": "842391fc-d7aa-4a5b-8360-71241e76c513"
              },
              "artifactVersion": {
                "status": "UPLOAD_COMPLETE",
                "createdDate": "2025-05-14T08:44:07.471Z",
                "totalSizeInBytes": 1903394,
                "storageVersion": "V1",
                "totalFileCount": 577,
                "updatedDate": "2025-05-14T08:44:07.471Z",
                "createdByUser": "q2irs51sak51ei9g8fcjbor13l",
                "customMetrics": [],
                "isSigned": false,
                "attributes": [],
                "id": "" + TEST_VALID_HELM_CHART_VERSION + ""
              },
              "artifact": {
                "orgName": "0539907589386975",
                "latestVersionId": "" + TEST_VALID_HELM_CHART_VERSION + "",
                "isReadOnly": true,
                "teamName": "mega-dev",
                "latestVersionSizeInBytes": 1903394,
                "hasSignedVersion": false,
                "canGuestDownload": false,
                "isPublic": false,
                "createdDate": "2024-10-29T20:20:42.150Z",
                "name": "mega-simulation-app",
                "updatedDate": "2025-05-14T08:44:07.425Z",
                "attributes": [],
                "artifactType": "HELM_CHART"
              }
            }
            """;

    @SneakyThrows
    public static void start(String authnBaseUrl, String casBaseUrl) {
        stop();
        authnMockServer = new WireMockServer(URI.create(authnBaseUrl).getPort());
        authnMockServer.start();
        var tokenResponse = """
                {
                     "access_token": "token",
                     "expires_in": 3600
                }
                """;

        // The expected Authorization header format is Basic base64($oauthtoken:<api-key>).
        // The mock server is configured to not catch requests where the `api-key` starts with "nvapi-".
        // This is achieved by checking that the Base64-encoded string in the `Authorization` header
        // does not contain the pattern `$oauthtoken:nvapi-`.
        authnMockServer.stubFor(post(urlPathEqualTo("/token"))
                                        .withHeader(HttpHeaders.AUTHORIZATION, matching(
                                                "^Basic (?!.*JG9hdXRodG9rZW46bnZhcGkt).+$"))
                                        .willReturn(aResponse().withStatus(200)
                                                            .withHeader(CONTENT_TYPE,
                                                                        APPLICATION_JSON_VALUE)
                                                            .withBody(tokenResponse)));
        
        casMockServer = new WireMockServer(URI.create(casBaseUrl).getPort());
        casMockServer.start();

        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SIZE_URL))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(MODEL_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SIZE_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(MODEL_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SIZE_URL_WITH_TEAM_2))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(MODEL_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SMALL_SIZE_URL))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(MODEL_SMALL_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SMALL_SIZE_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(MODEL_SMALL_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SIZE_NOT_EXISTS_URL))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SIZE_NOT_EXISTS_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SIZE_PERMISSION_DENIED_URL))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SIZE_PERMISSION_DENIED_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_SIZE_URL))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(RESOURCE_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_SIZE_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(RESOURCE_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_SIZE_URL_WITH_TEAM_2))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(RESOURCE_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_SIZE_NOT_EXISTS_URL))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_FILES_NOT_EXISTS_URL))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_SIZE_NOT_EXISTS_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_FILE_NOT_EXISTS_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_SIZE_PERMISSION_DENIED_URL))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_FILE_PERMISSION_DENIED_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_SIZE_PERMISSION_DENIED_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(HELM_SIZE_URL))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(HELM_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(HELM_SIZE_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(HELM_SIZE)));
        casMockServer.stubFor(get(urlPathEqualTo(HELM_SIZE_NOT_EXISTS_URL))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(HELM_SIZE_NOT_EXISTS_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(HELM_SIZE_PERMISSION_DENIED_URL))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(HELM_SIZE_PERMISSION_DENIED_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_URL))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getModelFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_URL_WITH_VERSION))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getModelFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_URL_2))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getModelFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getModelFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_URL_WITH_TEAM_2))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getModelFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILE_PERMISSION_DENIED_URL))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILE_PERMISSION_DENIED_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(403)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_NOT_EXIST_URL))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_NOT_EXIST_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SMALL_SIZE_FILES_URL))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getModelFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_SMALL_SIZE_FILES_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getModelFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_FILES_URL))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getResourceFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_FILES_URL_WITH_VERSION))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_FILES_URL_WITH_TEAM))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getResourceFileResponse())));
        casMockServer.stubFor(get(urlPathEqualTo(RESOURCE_FILES_URL_WITH_TEAM_2))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getResourceFileResponse())));
        // 429 retry: always rate-limited
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_RATE_LIMITED_URL))
                                      .willReturn(aResponse().withStatus(429)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)));
        // 429 retry: paginated — first page succeeds (totalPages=2); second page uses scenario stubs
        casMockServer.stubFor(get(urlPathEqualTo(MODEL_FILES_PAGINATED_429_URL))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(getModelFilesFirstPageResponse())));

        // For NVCT testing results/checkpoints creation
        var ignoredResponseBody = """
                {"ignored": "true"}
                """;

        casMockServer.stubFor(post(urlPathMatching(CHECKPOINT_URL))
                                      .withHeader(HttpHeaders.AUTHORIZATION, new AnythingPattern())
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(ignoredResponseBody)));
        casMockServer.stubFor(post(urlPathMatching(CHECKPOINT_URL_WITH_TEAM))
                                      .withHeader(HttpHeaders.AUTHORIZATION, new AnythingPattern())
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(ignoredResponseBody)));
        casMockServer.stubFor(post(urlPathMatching(CHECKPOINT_URL_WITH_UNKNOWN_ORG))
                                      .withHeader(HttpHeaders.AUTHORIZATION, new AnythingPattern())
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(ignoredResponseBody)));
        casMockServer.stubFor(post(urlPathMatching(CHECKPOINT_URL_WITH_UNKNOW_TEAM))
                                      .withHeader(HttpHeaders.AUTHORIZATION, new AnythingPattern())
                                      .willReturn(aResponse().withStatus(404)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(ignoredResponseBody)));
        casMockServer.stubFor(post(urlPathMatching(CHECKPOINT_URL_WITH_INVALID_KEY))
                                      .withHeader(HttpHeaders.AUTHORIZATION, new AnythingPattern())
                                      .willReturn(aResponse().withStatus(401)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(ignoredResponseBody)));
        casMockServer.stubFor(delete(urlPathMatching("/v2/org/(.+)?/models/.+"))
                                      .withHeader(HttpHeaders.AUTHORIZATION, new AnythingPattern())
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(ignoredResponseBody)));
        casMockServer.stubFor(delete(urlPathMatching("/v2/org/(.+)?/team/(.+)?/models/.+"))
                                      .withHeader(HttpHeaders.AUTHORIZATION, new AnythingPattern())
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(ignoredResponseBody)));
    }

    @SneakyThrows
    public static byte[] getModelFileResponse() {
        var response = new ArtifactRegistryFilesResponse();
        response.setPaginationInfo(new PaginationInfo());
        response.setRequestStatus(new RequestStatus());
        response.setUrls(List.of("https://api.stg.ngc.nvidia.com/file1",
                                 "https://api.stg.ngc.nvidia.com/file2"));
        response.setFilepath(List.of("/file1", "/file2"));

        return JSON_MAPPER.writeValueAsBytes(response);
    }

    @SneakyThrows
    public static byte[] getModelFilesFirstPageResponse() {
        var paginationInfo = new PaginationInfo();
        paginationInfo.setTotalPages(2);
        var response = new ArtifactRegistryFilesResponse();
        response.setPaginationInfo(paginationInfo);
        response.setRequestStatus(new RequestStatus());
        response.setUrls(List.of("https://api.stg.ngc.nvidia.com/file1",
                                 "https://api.stg.ngc.nvidia.com/file2"));
        response.setFilepath(List.of("/file1", "/file2"));
        return JSON_MAPPER.writeValueAsBytes(response);
    }

    @SneakyThrows
    public static byte[] getModelFilesSecondPageResponse() {
        var paginationInfo = new PaginationInfo();
        paginationInfo.setTotalPages(2);
        var response = new ArtifactRegistryFilesResponse();
        response.setPaginationInfo(paginationInfo);
        response.setRequestStatus(new RequestStatus());
        response.setUrls(List.of("https://api.stg.ngc.nvidia.com/file3",
                                 "https://api.stg.ngc.nvidia.com/file4"));
        response.setFilepath(List.of("/file3", "/file4"));
        return JSON_MAPPER.writeValueAsBytes(response);
    }

    @SneakyThrows
    private static byte[] getResourceFileResponse() {
        var response = new ArtifactRegistryFilesResponse();
        response.setPaginationInfo(new PaginationInfo());
        response.setRequestStatus(new RequestStatus());
        response.setUrls(List.of("https://api.stg.ngc.nvidia.com/image1",
                                 "https://api.stg.ngc.nvidia.com/image2"));
        response.setFilepath(List.of("/image1", "/image2"));
        return JSON_MAPPER.writeValueAsBytes(response);
    }

    public static void setResponse(String url, byte[] body) {
        casMockServer.stubFor(get(urlPathEqualTo(url))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(body)));
    }

    public static void stubFor(MappingBuilder mappingBuilder) {
        casMockServer.stubFor(mappingBuilder);
    }

    public static void resetScenarios() {
        casMockServer.resetScenarios();
    }

    public static void stop() {
        if (casMockServer != null) {
            casMockServer.stop();
        }
        if (authnMockServer != null) {
            authnMockServer.stop();
        }
    }

    @Data
    static class PaginationInfo {

        private int totalPages;
        private int index;
        private int totalResults;
        private String nextPage;
        private int size;
    }

    @Data
    static class RequestStatus {

        private String serverID;
        private String statusCode;
        private String statusDescription;
        private String requestID;
    }

    @Data
    static class ArtifactRegistryFilesResponse {

        private PaginationInfo paginationInfo;
        private RequestStatus requestStatus;
        private List<String> urls;
        private List<String> filepath;
    }
}
